package traveltime

// segment_capture_db.go — SQLite storage for per-leg TTI detail
// (collector.go's RouteLegStat), kept entirely separate from the existing
// travel_logs/<date>.jsonl aggregate log (logs.go): that log, and
// everything built on it (Dashboard, Tổng quan giao thông, forecasting),
// keeps reading exactly one TTI number per route per scan, unaffected by
// this. This store exists purely to feed www/flowcapture.html's map with
// "which leg of a tracked route is jammed vs free" — "Phương án B":
// collecting per-segment TTI by having the admin add waypoints to a route,
// since getRouteStats already fetches one VietMap API call per leg between
// consecutive waypoints (no extra API cost over what a route with
// waypoints already made; a route with none still gets exactly one leg,
// identical to its own aggregate).
//
// Driver: modernc.org/sqlite — pure Go (no cgo), same pattern
// internal/traffic/flow_capture_db.go already uses.

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AlexxIT/go2rtc/internal/trafficmetrics"
	_ "modernc.org/sqlite"
)

var segmentCaptureDB *sql.DB

const segmentCaptureSchema = `
CREATE TABLE IF NOT EXISTS route_segment_scans (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	route_id   TEXT NOT NULL,
	route_name TEXT NOT NULL,
	scanned_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_route_segment_scans_route ON route_segment_scans(route_id, scanned_at DESC);

CREATE TABLE IF NOT EXISTS route_segments (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	scan_id      INTEGER NOT NULL REFERENCES route_segment_scans(id),
	leg_index    INTEGER NOT NULL,
	from_lat     REAL NOT NULL,
	from_lng     REAL NOT NULL,
	to_lat       REAL NOT NULL,
	to_lng       REAL NOT NULL,
	length_m     INTEGER NOT NULL,
	duration_sec INTEGER NOT NULL,
	base_sec     INTEGER NOT NULL,
	delay_sec    INTEGER NOT NULL,
	tti          REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_route_segments_scan ON route_segments(scan_id);
`

// initSegmentCaptureDB opens (creating if absent) travel_route_segments.db
// next to travel_routes.json and applies the schema (idempotent).
func initSegmentCaptureDB(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "travel_route_segments.db")
	var err error
	segmentCaptureDB, err = sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := segmentCaptureDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := segmentCaptureDB.Exec(segmentCaptureSchema); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if err := migrateRouteSegmentsMetricsColumns(segmentCaptureDB); err != nil {
		return err
	}
	return nil
}

// routeSegmentsMetricsColumns are the Z/LOS columns added after this
// table's initial release (internal/trafficmetrics, SPEC-ITS-TRAFFIC-
// ANALYSIS-01's "Z-từ-TTI-thuần" path — no JF available per leg, so only
// Z/LOS, no fused TTI/anomaly here, unlike internal/traffic's flow_segments
// equivalent). Added via ALTER TABLE, same convention
// internal/traffic/flow_capture_db.go's own migration uses.
var routeSegmentsMetricsColumns = []string{
	"ALTER TABLE route_segments ADD COLUMN z_est REAL NOT NULL DEFAULT 0",
	"ALTER TABLE route_segments ADD COLUMN z_percent REAL NOT NULL DEFAULT 0",
	"ALTER TABLE route_segments ADD COLUMN los_label TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE route_segments ADD COLUMN los_color TEXT NOT NULL DEFAULT ''",
}

func migrateRouteSegmentsMetricsColumns(db *sql.DB) error {
	for _, stmt := range routeSegmentsMetricsColumns {
		if _, err := db.Exec(stmt); err != nil && !isDuplicateColumnErr(err) {
			return fmt.Errorf("migrate route_segments metrics columns: %w", err)
		}
	}
	return nil
}

func isDuplicateColumnErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate column name")
}

// recordRouteSegments persists one route's leg-by-leg stats for this scan
// — one route_segment_scans row plus one route_segments row per leg.
func recordRouteSegments(routeID, routeName string, scannedAt time.Time, legs []RouteLegStat) error {
	if segmentCaptureDB == nil || len(legs) == 0 {
		return nil
	}
	tx, err := segmentCaptureDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO route_segment_scans (route_id, route_name, scanned_at) VALUES (?, ?, ?)`,
		routeID, routeName, scannedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert route_segment_scans: %w", err)
	}
	scanID, err := res.LastInsertId()
	if err != nil {
		return err
	}

	insertLeg, err := tx.Prepare(`INSERT INTO route_segments
		(scan_id, leg_index, from_lat, from_lng, to_lat, to_lng, length_m, duration_sec, base_sec, delay_sec, tti,
		 z_est, z_percent, los_label, los_color)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insertLeg.Close()

	// internal/trafficmetrics (SPEC-ITS-TRAFFIC-ANALYSIS-01): only Z/LOS,
	// no fused TTI/anomaly — a route leg has no JF reading of its own to
	// fuse with or cross-check against (see ComputeRouteLeg's own doc
	// comment for why this differs from internal/traffic's flow-capture
	// path, which has speed+freeFlow+JF all from the same reading).
	metricsCfg := trafficmetrics.GetConfig()

	for _, leg := range legs {
		m := metricsCfg.ComputeRouteLeg(leg.TTI)
		if _, err := insertLeg.Exec(scanID, leg.LegIndex, leg.FromLat, leg.FromLng, leg.ToLat, leg.ToLng,
			leg.LengthM, leg.DurationSec, leg.BaseSec, leg.DelaySec, leg.TTI,
			m.Z, m.ZPercent, m.LOS.Label, m.LOS.Color); err != nil {
			return fmt.Errorf("insert route_segments (leg %d): %w", leg.LegIndex, err)
		}
	}
	return tx.Commit()
}

// routeSegmentScanMeta is one route_segment_scans row.
type routeSegmentScanMeta struct {
	ID        int64
	RouteID   string
	RouteName string
	ScannedAt string
}

// latestRouteSegmentScans returns the single most recent scan for every
// distinct route_id ever captured — one row per route, mirroring
// internal/traffic's latestFlowScanPerRegion.
func latestRouteSegmentScans() ([]routeSegmentScanMeta, error) {
	if segmentCaptureDB == nil {
		return nil, nil
	}
	rows, err := segmentCaptureDB.Query(`
		SELECT s.id, s.route_id, s.route_name, s.scanned_at
		FROM route_segment_scans s
		INNER JOIN (SELECT route_id, MAX(id) AS max_id FROM route_segment_scans GROUP BY route_id) latest
			ON latest.route_id = s.route_id AND latest.max_id = s.id
		ORDER BY s.scanned_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []routeSegmentScanMeta{}
	for rows.Next() {
		var m routeSegmentScanMeta
		if err := rows.Scan(&m.ID, &m.RouteID, &m.RouteName, &m.ScannedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// legRow is one route_segments row.
type legRow struct {
	LegIndex    int
	FromLat     float64
	FromLng     float64
	ToLat       float64
	ToLng       float64
	LengthM     int
	DurationSec int
	BaseSec     int
	DelaySec    int
	TTI         float64
	Z           float64
	ZPercent    float64
	LOSLabel    string
	LOSColor    string
}

func getRouteSegmentLegs(scanID int64) ([]legRow, error) {
	if segmentCaptureDB == nil {
		return nil, nil
	}
	rows, err := segmentCaptureDB.Query(`
		SELECT leg_index, from_lat, from_lng, to_lat, to_lng, length_m, duration_sec, base_sec, delay_sec, tti,
		       z_est, z_percent, los_label, los_color
		FROM route_segments WHERE scan_id = ? ORDER BY leg_index`, scanID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []legRow{}
	for rows.Next() {
		var l legRow
		if err := rows.Scan(&l.LegIndex, &l.FromLat, &l.FromLng, &l.ToLat, &l.ToLng,
			&l.LengthM, &l.DurationSec, &l.BaseSec, &l.DelaySec, &l.TTI,
			&l.Z, &l.ZPercent, &l.LOSLabel, &l.LOSColor); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// cleanupSegmentCaptureOlderThan deletes route_segment_scans rows (and
// their route_segments) older than days ago. No-op when days <= 0.
func cleanupSegmentCaptureOlderThan(days int) error {
	if segmentCaptureDB == nil || days <= 0 {
		return nil
	}
	cutoff := time.Now().AddDate(0, 0, -days).UTC().Format(time.RFC3339)
	tx, err := segmentCaptureDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`DELETE FROM route_segments WHERE scan_id IN (SELECT id FROM route_segment_scans WHERE scanned_at < ?)`, cutoff,
	); err != nil {
		return fmt.Errorf("delete old route_segments: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM route_segment_scans WHERE scanned_at < ?`, cutoff); err != nil {
		return fmt.Errorf("delete old route_segment_scans: %w", err)
	}
	return tx.Commit()
}

// RouteSegmentDetail is one leg's stats, exported for other packages (the
// Flow Capture map's API passthrough — see internal/traffic/
// flowcapture_page_api.go) to read without reaching into this package's
// unexported storage.
type RouteSegmentDetail struct {
	LegIndex    int     `json:"legIndex"`
	FromLat     float64 `json:"fromLat"`
	FromLng     float64 `json:"fromLng"`
	ToLat       float64 `json:"toLat"`
	ToLng       float64 `json:"toLng"`
	LengthM     int     `json:"lengthM"`
	DurationSec int     `json:"durationSec"`
	BaseSec     int     `json:"baseSec"`
	DelaySec    int     `json:"delaySec"`
	TTI         float64 `json:"tti"`
	Z           float64 `json:"z"`
	ZPercent    float64 `json:"zPercent"`
	LOSLabel    string  `json:"losLabel"`
	LOSColor    string  `json:"losColor"`
}

// RouteSegments is one route's own latest capture: every leg between its
// Origin→Waypoints→Destination chain, each with its own TTI.
type RouteSegments struct {
	RouteID   string               `json:"routeId"`
	RouteName string               `json:"routeName"`
	ScannedAt string               `json:"scannedAt"`
	Segments  []RouteSegmentDetail `json:"segments"`
}

// LatestRouteSegments returns every route's own most recent per-leg
// capture — the data source for www/flowcapture.html's "tuyến TTI đang
// theo dõi" layer. A route never yet scanned (or scanned before this
// store existed) simply doesn't appear; it isn't an error.
func LatestRouteSegments() ([]RouteSegments, error) {
	metas, err := latestRouteSegmentScans()
	if err != nil {
		return nil, err
	}
	out := make([]RouteSegments, 0, len(metas))
	for _, m := range metas {
		legs, err := getRouteSegmentLegs(m.ID)
		if err != nil || len(legs) == 0 {
			continue
		}
		rs := RouteSegments{RouteID: m.RouteID, RouteName: m.RouteName, ScannedAt: m.ScannedAt}
		for _, l := range legs {
			rs.Segments = append(rs.Segments, RouteSegmentDetail{
				LegIndex: l.LegIndex, FromLat: l.FromLat, FromLng: l.FromLng, ToLat: l.ToLat, ToLng: l.ToLng,
				LengthM: l.LengthM, DurationSec: l.DurationSec, BaseSec: l.BaseSec, DelaySec: l.DelaySec, TTI: l.TTI,
				Z: l.Z, ZPercent: l.ZPercent, LOSLabel: l.LOSLabel, LOSColor: l.LOSColor,
			})
		}
		out = append(out, rs)
	}
	return out, nil
}
