package trafficflow

// records.go — SQLite storage for FlowRecord, one Hawkwise push cycle per
// camera. See db.go's doc comment for why this moved off the earlier
// day-bucketed-JSON-files design (a single global flow_index.json rewritten
// in full on every push, with no cap now that retention is meant to be
// indefinite for this dataset).

import (
	"database/sql"
	"encoding/json"
	"time"
)

// VehicleCount is one entry of details.density_detail.data[] (or a lane's
// own data[]) — label is Hawkwise's own free-text string, never validated
// against a fixed enum, per docs/hawkwise-flow-integration-plan.md §2.4.
type VehicleCount struct {
	Label    string  `json:"label"`
	Volume   int     `json:"volume"`
	AvgSpeed float64 `json:"avg_speed,omitempty"`
}

// LaneFlow is one entry of details.density_detail.partial_data[]. ID is the
// drawing-order number Hawkwise assigned when the camera was configured —
// NOT a standard "numbered from the median" lane convention, and carries no
// turn-direction/movement field (Hawkwise doesn't send one).
type LaneFlow struct {
	ID        int            `json:"id"`
	Occupancy float64        `json:"occupancy"`
	AvgSpeed  *float64       `json:"avg_speed,omitempty"`
	Volume    int            `json:"volume"`
	Data      []VehicleCount `json:"data,omitempty"`
}

// FlowRecord is one Hawkwise Advanced Traffic Density push cycle, normalized
// from the raw wire struct (hawkwisePush in api.go) to the fields this
// system actually stores/displays.
type FlowRecord struct {
	EventID       int64  `json:"event_id"` // dedup key, per Hawkwise doc §6.1 — this table's PRIMARY KEY
	EventIDString string `json:"event_id_string,omitempty"`
	SourceUUID    string `json:"source_uuid"`

	TriggerCode   string `json:"trigger_code,omitempty"`
	SeverityLevel int    `json:"severity_level"`
	EventState    *int   `json:"event_state,omitempty"` // null | 3 (jam ongoing) | 5 (jam ended)

	// StartTime/EndTime are Hawkwise's own reported window, parsed from its
	// local "yyyy-MM-dd HH:mm:ss" strings via parseHawkwiseTime.
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`

	TrafficStatus *string `json:"traffic_status,omitempty"` // FreeFlow | HeavyTraffic | CongestedFlow | null
	// AvgOccupancy/AvgSpeed units are unconfirmed even in Hawkwise's own
	// document (flagged "CẦN XÁC NHẬN") — stored as-is, see plan §5.2.
	AvgOccupancy float64 `json:"avg_occupancy"`
	AvgSpeed     float64 `json:"avg_speed"`
	Total        int     `json:"total"`

	VehicleCounts []VehicleCount `json:"vehicle_counts,omitempty"`
	Lanes         []LaneFlow     `json:"lanes,omitempty"`

	FrameURL *string `json:"frame_url,omitempty"`

	// ReportedLat/ReportedLng are Hawkwise's own per-push coordinates, kept
	// for reference only (e.g. prefilling a new Sensor mapping) — never used
	// directly for map placement, per the user's explicit instruction. See
	// Sensor.Lat/Lon.
	ReportedLat *float64 `json:"reported_lat,omitempty"`
	ReportedLng *float64 `json:"reported_lng,omitempty"`

	// ReceivedAt is our server clock at first ingestion — immune to clock
	// skew on Hawkwise's side, same convention as internal/aievent.AIEvent.ReceivedAt.
	ReceivedAt time.Time `json:"received_at"`
}

// Exists reports whether eventID has already been ingested — used by the
// push handler's dry_run mode and dedup fast path.
func Exists(eventID int64) bool {
	var one int
	err := db.QueryRow(`SELECT 1 FROM flow_records WHERE event_id = ?`, eventID).Scan(&one)
	return err == nil
}

// Ingest stores a new FlowRecord, keyed by rec.EventID, stamping
// ReceivedAt with our own server clock (immune to Hawkwise's clock skew —
// see FlowRecord.ReceivedAt's doc comment). A duplicate EventID (Hawkwise
// retried the same push cycle) is a no-op — per Hawkwise doc §6.1,
// "nhận trùng → không cộng dồn, vẫn trả 2xx" — returns created=false
// without touching storage.
func Ingest(rec *FlowRecord) (created bool, err error) {
	cp := *rec
	cp.ReceivedAt = time.Now()
	return insertFlowRecord(&cp)
}

// insertFlowRecord is the low-level insert, trusting rec.ReceivedAt as
// given rather than stamping it — Ingest (the live push path) always sets
// it to "now" first; migrateJSONData (restoring history from the old
// day-bucketed-JSON format) calls this directly to preserve each record's
// real original ReceivedAt instead of rewriting history to "now". The
// INSERT OR IGNORE + the table's PRIMARY KEY on event_id make this atomic
// and race-safe even under concurrent pushes, unlike the old
// mutex-guarded full-file-rewrite.
func insertFlowRecord(rec *FlowRecord) (created bool, err error) {
	vehicleCountsJSON, err := marshalOrNil(rec.VehicleCounts)
	if err != nil {
		return false, err
	}
	lanesJSON, err := marshalOrNil(rec.Lanes)
	if err != nil {
		return false, err
	}

	res, err := db.Exec(`
		INSERT OR IGNORE INTO flow_records (
			event_id, event_id_string, source_uuid, trigger_code, severity_level, event_state,
			start_time, end_time, traffic_status, avg_occupancy, avg_speed, total,
			vehicle_counts, lanes, frame_url, reported_lat, reported_lng, received_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		rec.EventID, rec.EventIDString, rec.SourceUUID, rec.TriggerCode, rec.SeverityLevel, nullInt(rec.EventState),
		rec.StartTime.Format(time.RFC3339), rec.EndTime.Format(time.RFC3339), nullString(rec.TrafficStatus),
		rec.AvgOccupancy, rec.AvgSpeed, rec.Total,
		vehicleCountsJSON, lanesJSON, nullString(rec.FrameURL), nullFloat(rec.ReportedLat), nullFloat(rec.ReportedLng),
		rec.ReceivedAt.UnixNano(),
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// flowRow scans one row from a query selecting exactly the 18 flow_records
// columns in the same order Ingest inserts them in — shared by
// GetLatestForSensor/ListLatest.
func flowRow(row interface{ Scan(...any) error }) (*FlowRecord, error) {
	var rec FlowRecord
	var eventState sql.NullInt64
	var trafficStatus, frameURL sql.NullString
	var vehicleCountsJSON, lanesJSON sql.NullString
	var reportedLat, reportedLng sql.NullFloat64
	var startTime, endTime string
	var receivedAtNano int64

	if err := row.Scan(
		&rec.EventID, &rec.EventIDString, &rec.SourceUUID, &rec.TriggerCode, &rec.SeverityLevel, &eventState,
		&startTime, &endTime, &trafficStatus, &rec.AvgOccupancy, &rec.AvgSpeed, &rec.Total,
		&vehicleCountsJSON, &lanesJSON, &frameURL, &reportedLat, &reportedLng, &receivedAtNano,
	); err != nil {
		return nil, err
	}

	if eventState.Valid {
		v := int(eventState.Int64)
		rec.EventState = &v
	}
	rec.StartTime, _ = time.Parse(time.RFC3339, startTime)
	rec.EndTime, _ = time.Parse(time.RFC3339, endTime)
	if trafficStatus.Valid {
		rec.TrafficStatus = &trafficStatus.String
	}
	if frameURL.Valid {
		rec.FrameURL = &frameURL.String
	}
	rec.ReportedLat = floatPtr(reportedLat)
	rec.ReportedLng = floatPtr(reportedLng)
	rec.ReceivedAt = time.Unix(0, receivedAtNano)
	if vehicleCountsJSON.Valid {
		_ = json.Unmarshal([]byte(vehicleCountsJSON.String), &rec.VehicleCounts)
	}
	if lanesJSON.Valid {
		_ = json.Unmarshal([]byte(lanesJSON.String), &rec.Lanes)
	}
	return &rec, nil
}

const flowRecordColumns = `
	event_id, event_id_string, source_uuid, trigger_code, severity_level, event_state,
	start_time, end_time, traffic_status, avg_occupancy, avg_speed, total,
	vehicle_counts, lanes, frame_url, reported_lat, reported_lng, received_at
`

// GetLatestForSensor returns the most recent FlowRecord for sourceUUID, if
// any has ever been ingested. Backed by idx_flow_records_source_received —
// an indexed lookup, not a scan of every record ever stored for this sensor.
func GetLatestForSensor(sourceUUID string) (*FlowRecord, bool) {
	row := db.QueryRow(`
		SELECT `+flowRecordColumns+`
		FROM flow_records WHERE source_uuid = ?
		ORDER BY received_at DESC, event_id DESC LIMIT 1
	`, sourceUUID)
	rec, err := flowRow(row)
	if err != nil {
		return nil, false
	}
	return rec, true
}

// ListLatest returns the most recent FlowRecord for every source_uuid ever
// seen, most recently received first. Uses a window function to pick each
// group's top row in one indexed pass, rather than N per-sensor queries.
func ListLatest() []*FlowRecord {
	rows, err := db.Query(`
		SELECT ` + flowRecordColumns + `
		FROM (
			SELECT *, ROW_NUMBER() OVER (PARTITION BY source_uuid ORDER BY received_at DESC, event_id DESC) AS rn
			FROM flow_records
		) AS sub
		WHERE rn = 1
		ORDER BY received_at DESC
	`)
	if err != nil {
		log.Warn().Err(err).Msg("[trafficflow] ListLatest query failed")
		return nil
	}
	defer rows.Close()

	out := make([]*FlowRecord, 0, 64)
	for rows.Next() {
		rec, err := flowRow(rows)
		if err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// deleteRecordsOlderThan removes flow_records rows older than retentionDays
// — a single indexed DELETE (via idx_flow_records_received), not a
// directory scan + per-file remove. Returns the number of rows removed.
func deleteRecordsOlderThan(retentionDays int) int64 {
	cutoff := time.Now().AddDate(0, 0, -retentionDays).UnixNano()
	res, err := db.Exec(`DELETE FROM flow_records WHERE received_at < ?`, cutoff)
	if err != nil {
		log.Warn().Err(err).Msg("[trafficflow] retention delete failed")
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}

// HourlyBucket is one hour's network-wide total across every sensor —
// summed Total and per-label Volume (vehicle_counts), used by Dashboard
// 02's "Lưu lượng phương tiện" module. Hour is 0-23 in Vietnam wall-clock
// time (vnLocation), matching the day-bucketing convention used elsewhere
// in this package and in internal/traffic/internal/incidents.
type HourlyBucket struct {
	Hour          int            `json:"hour"`
	Total         int            `json:"total"`
	VehicleCounts map[string]int `json:"vehicleCounts"`
}

// HourlyTotals aggregates every flow_records row received on the given
// Vietnam-local calendar day (YYYY-MM-DD, vnLocation) into 24 hourly
// buckets, network-wide across all sensors — Dashboard 02 calls this once
// for "today" and once for "yesterday" rather than this package computing
// the comparison itself, same division of labor as internal/traffic's
// per-day scan files. Always returns 24 buckets (0-23), zero-filled for
// hours with no records, so callers can index directly by hour.
func HourlyTotals(day string) ([]HourlyBucket, error) {
	dayStart, err := time.ParseInLocation("2006-01-02", day, vnLocation)
	if err != nil {
		return nil, err
	}
	dayEnd := dayStart.AddDate(0, 0, 1)

	rows, err := db.Query(`
		SELECT received_at, total, vehicle_counts
		FROM flow_records
		WHERE received_at >= ? AND received_at < ?
	`, dayStart.UnixNano(), dayEnd.UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	buckets := make([]HourlyBucket, 24)
	for h := range buckets {
		buckets[h] = HourlyBucket{Hour: h, VehicleCounts: map[string]int{}}
	}

	for rows.Next() {
		var receivedAtNano int64
		var total int
		var vcJSON sql.NullString
		if err := rows.Scan(&receivedAtNano, &total, &vcJSON); err != nil {
			continue
		}
		h := time.Unix(0, receivedAtNano).In(vnLocation).Hour()
		b := &buckets[h]
		b.Total += total
		if vcJSON.Valid {
			var vcs []VehicleCount
			if err := json.Unmarshal([]byte(vcJSON.String), &vcs); err == nil {
				for _, vc := range vcs {
					b.VehicleCounts[vc.Label] += vc.Volume
				}
			}
		}
	}
	return buckets, rows.Err()
}

func marshalOrNil[T any](v []T) (any, error) {
	if len(v) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func nullInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullString(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}
