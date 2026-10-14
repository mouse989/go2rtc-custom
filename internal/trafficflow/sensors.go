package trafficflow

// sensors.go — two related SQLite-backed tables, mirroring
// internal/aievent/categories.go's ObservedCategory/CategoryRule split:
//
//   - ObservedSensor: auto-logged, one row per distinct source_uuid the
//     push endpoint has ever seen, with Hawkwise's own reported coordinates
//     as a convenience suggestion. Needs zero admin setup — exists purely so
//     an admin can see what Hawkwise is actually sending before mapping it.
//   - Sensor: admin-authored, maps a source_uuid to an internal map point
//     (ID, label, lat/lon the admin controls). Per the user's explicit
//     instruction, this is NOT auto-trusted from Hawkwise's per-push
//     lat/lng — an admin must create/confirm it.
//
// A push whose source_uuid has no Sensor mapping is still stored (never
// dropped — see records.go's Ingest) and just excluded from the map layer
// until an admin maps it.

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Sensor is one admin-managed mapping from a Hawkwise camera to a point on
// the map.
type Sensor struct {
	ID           string    `json:"id"`
	SourceUUID   string    `json:"source_uuid"`             // general.camera.source_uuid from Hawkwise — the join key on every push
	HardwareUUID string    `json:"hardware_uuid,omitempty"` // reference only, not required to be unique
	Label        string    `json:"label"`                   // admin-chosen display name
	Lat          float64   `json:"lat"`                     // admin-confirmed — may differ slightly from Hawkwise's own reported coordinate
	Lon          float64   `json:"lon"`
	Enabled      bool      `json:"enabled"` // false = hide from the map layer; data keeps being stored
	CreatedAt    time.Time `json:"created_at"`
}

// ObservedSensor is one distinct source_uuid seen in incoming pushes,
// auto-maintained by observeSensor.
type ObservedSensor struct {
	SourceUUID   string    `json:"source_uuid"`
	HardwareUUID string    `json:"hardware_uuid,omitempty"`
	ReportedLat  *float64  `json:"reported_lat,omitempty"` // Hawkwise's own camera.latitude from the most recent push
	ReportedLng  *float64  `json:"reported_lng,omitempty"`
	Count        int       `json:"count"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
}

// observeSensor records one sighting of sourceUUID — called on every push,
// regardless of whether a Sensor mapping already exists, so the "what has
// Hawkwise actually sent" view stays complete even after sensors are mapped.
// Implemented as a single UPSERT (INSERT ... ON CONFLICT DO UPDATE) — one
// indexed write, not a read-modify-write-whole-file cycle.
func observeSensor(sourceUUID, hardwareUUID string, lat, lng *float64) {
	if sourceUUID == "" {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(`
		INSERT INTO observed_sensors (source_uuid, hardware_uuid, reported_lat, reported_lng, count, first_seen, last_seen)
		VALUES (?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(source_uuid) DO UPDATE SET
			hardware_uuid = CASE WHEN excluded.hardware_uuid != '' THEN excluded.hardware_uuid ELSE observed_sensors.hardware_uuid END,
			reported_lat  = CASE WHEN excluded.reported_lat  IS NOT NULL THEN excluded.reported_lat  ELSE observed_sensors.reported_lat  END,
			reported_lng  = CASE WHEN excluded.reported_lng  IS NOT NULL THEN excluded.reported_lng  ELSE observed_sensors.reported_lng  END,
			count         = observed_sensors.count + 1,
			last_seen     = excluded.last_seen
	`, sourceUUID, hardwareUUID, nullFloat(lat), nullFloat(lng), now, now)
	if err != nil {
		log.Warn().Err(err).Str("source_uuid", sourceUUID).Msg("[trafficflow] observeSensor failed (continuing)")
	}
}

// ListObservedSensors returns every distinct source_uuid seen, most recently
// seen first.
func ListObservedSensors() []ObservedSensor {
	rows, err := db.Query(`
		SELECT source_uuid, hardware_uuid, reported_lat, reported_lng, count, first_seen, last_seen
		FROM observed_sensors ORDER BY last_seen DESC
	`)
	if err != nil {
		log.Warn().Err(err).Msg("[trafficflow] ListObservedSensors query failed")
		return nil
	}
	defer rows.Close()

	out := make([]ObservedSensor, 0, 64)
	for rows.Next() {
		var o ObservedSensor
		var lat, lng sql.NullFloat64
		var firstSeen, lastSeen string
		if err := rows.Scan(&o.SourceUUID, &o.HardwareUUID, &lat, &lng, &o.Count, &firstSeen, &lastSeen); err != nil {
			continue
		}
		o.ReportedLat = floatPtr(lat)
		o.ReportedLng = floatPtr(lng)
		o.FirstSeen, _ = time.Parse(time.RFC3339Nano, firstSeen)
		o.LastSeen, _ = time.Parse(time.RFC3339Nano, lastSeen)
		out = append(out, o)
	}
	return out
}

// ListSensors returns every configured mapping, by label.
func ListSensors() []Sensor {
	rows, err := db.Query(`SELECT id, source_uuid, hardware_uuid, label, lat, lon, enabled, created_at FROM sensors ORDER BY label`)
	if err != nil {
		log.Warn().Err(err).Msg("[trafficflow] ListSensors query failed")
		return nil
	}
	defer rows.Close()

	out := make([]Sensor, 0, 64)
	for rows.Next() {
		sn, err := scanSensor(rows)
		if err != nil {
			continue
		}
		out = append(out, sn)
	}
	return out
}

// scanSensor scans one row from a query selecting exactly
// (id, source_uuid, hardware_uuid, label, lat, lon, enabled, created_at),
// shared by ListSensors/GetSensorBySourceUUID/the single-row lookups below.
func scanSensor(row interface{ Scan(...any) error }) (Sensor, error) {
	var sn Sensor
	var enabled int
	var createdAt string
	if err := row.Scan(&sn.ID, &sn.SourceUUID, &sn.HardwareUUID, &sn.Label, &sn.Lat, &sn.Lon, &enabled, &createdAt); err != nil {
		return Sensor{}, err
	}
	sn.Enabled = enabled != 0
	sn.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return sn, nil
}

// GetSensorBySourceUUID returns the mapping for sourceUUID, if any.
func GetSensorBySourceUUID(sourceUUID string) (*Sensor, bool) {
	row := db.QueryRow(`SELECT id, source_uuid, hardware_uuid, label, lat, lon, enabled, created_at FROM sensors WHERE source_uuid = ?`, sourceUUID)
	sn, err := scanSensor(row)
	if err != nil {
		return nil, false
	}
	return &sn, true
}

// CreateSensor registers a new source_uuid -> map point mapping. SourceUUID
// must be non-empty and not already mapped by another Sensor — Hawkwise's
// doc describes source_uuid as the camera's own stable key, so a 1:1
// mapping is enforced here via the table's UNIQUE constraint.
func CreateSensor(sn Sensor) (*Sensor, error) {
	sn.SourceUUID = strings.TrimSpace(sn.SourceUUID)
	sn.Label = strings.TrimSpace(sn.Label)
	if sn.SourceUUID == "" {
		return nil, errTrafficFlow("source_uuid required")
	}
	if sn.Label == "" {
		return nil, errTrafficFlow("label required")
	}

	sn.ID = fmt.Sprintf("hws-%d", time.Now().UnixNano())
	sn.CreatedAt = time.Now()
	_, err := db.Exec(`
		INSERT INTO sensors (id, source_uuid, hardware_uuid, label, lat, lon, enabled, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, sn.ID, sn.SourceUUID, sn.HardwareUUID, sn.Label, sn.Lat, sn.Lon, boolInt(sn.Enabled), sn.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		if isUniqueConstraintErr(err) {
			return nil, errTrafficFlow("source_uuid already mapped to another sensor")
		}
		return nil, err
	}
	cp := sn
	return &cp, nil
}

// UpdateSensor replaces an existing mapping's fields (label, lat/lon,
// enabled, hardware_uuid). SourceUUID may be changed too, subject to the
// same uniqueness check as CreateSensor.
func UpdateSensor(id string, upd Sensor) (*Sensor, error) {
	upd.SourceUUID = strings.TrimSpace(upd.SourceUUID)
	upd.Label = strings.TrimSpace(upd.Label)
	if upd.SourceUUID == "" {
		return nil, errTrafficFlow("source_uuid required")
	}
	if upd.Label == "" {
		return nil, errTrafficFlow("label required")
	}

	res, err := db.Exec(`
		UPDATE sensors SET source_uuid = ?, hardware_uuid = ?, label = ?, lat = ?, lon = ?, enabled = ?
		WHERE id = ?
	`, upd.SourceUUID, upd.HardwareUUID, upd.Label, upd.Lat, upd.Lon, boolInt(upd.Enabled), id)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return nil, errTrafficFlow("source_uuid already mapped to another sensor")
		}
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, errTrafficFlowNotFound
	}

	sn, ok := GetSensorBySourceUUID(upd.SourceUUID)
	if !ok {
		return nil, errTrafficFlowNotFound
	}
	return sn, nil
}

// DeleteSensor removes a mapping — matching pushes keep being stored under
// their source_uuid, they just stop appearing on the map.
func DeleteSensor(id string) error {
	res, err := db.Exec(`DELETE FROM sensors WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errTrafficFlowNotFound
	}
	return nil
}

type trafficFlowError string

func (e trafficFlowError) Error() string { return string(e) }
func errTrafficFlow(msg string) error    { return trafficFlowError(msg) }

var errTrafficFlowNotFound = trafficFlowError("not found")

// isUniqueConstraintErr reports whether err is a SQLite UNIQUE constraint
// violation — modernc.org/sqlite reports this via a plain error whose text
// contains "UNIQUE constraint failed" (it doesn't expose a typed sentinel
// the way some other drivers do).
func isUniqueConstraintErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullFloat(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

func floatPtr(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := n.Float64
	return &v
}
