package traffic

// flow_capture_db.go — SQLite storage for the raw traffic/flow API detail
// (flow_capture_parse.go), kept in its own file/table set next to the
// existing traffic_data/<date>.json history (storage.go). Separate from
// that JSON history on purpose: this stores much richer per-segment detail
// (including the full raw response, verbatim, as a safety net for any
// field not flattened into columns below). Capture always runs (no on/off
// switch — www/flowcapture.html's map depends on it); growth is instead
// bounded by Config.FlowCaptureRetentionDays (0 = keep forever, the
// default — a single region's response was ~95-100KB in real samples, so
// an admin who wants a cap can set one, same convention
// StorageConfig.RetentionDays already uses for traffic_data).
//
// Driver: modernc.org/sqlite — pure Go (no cgo), already a dependency via
// internal/trafficflow/db.go; same schema/init shape as that file.

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var flowCaptureDB *sql.DB

const flowCaptureSchema = `
CREATE TABLE IF NOT EXISTS flow_scans (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	region_name    TEXT NOT NULL,
	scanned_at     TEXT NOT NULL,
	source_updated TEXT NOT NULL DEFAULT '',
	raw_bytes      INTEGER NOT NULL,
	result_count   INTEGER NOT NULL,
	raw_json       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_flow_scans_region_time ON flow_scans(region_name, scanned_at);
CREATE INDEX IF NOT EXISTS idx_flow_scans_time ON flow_scans(scanned_at);

CREATE TABLE IF NOT EXISTS flow_segments (
	id                 INTEGER PRIMARY KEY AUTOINCREMENT,
	scan_id            INTEGER NOT NULL REFERENCES flow_scans(id),
	result_index       INTEGER NOT NULL,
	sub_index          INTEGER NOT NULL DEFAULT -1,
	description        TEXT NOT NULL DEFAULT '',
	length_m           REAL NOT NULL,
	speed_mps          REAL NOT NULL,
	speed_uncapped_mps REAL NOT NULL,
	free_flow_mps      REAL NOT NULL,
	jam_factor         REAL NOT NULL,
	has_jam_tendency   INTEGER NOT NULL DEFAULT 0,
	jam_tendency       REAL,
	confidence         REAL NOT NULL,
	traversability     TEXT NOT NULL DEFAULT '',
	functional_class   INTEGER,
	first_lat          REAL,
	first_lng          REAL,
	last_lat           REAL,
	last_lng           REAL
);
CREATE INDEX IF NOT EXISTS idx_flow_segments_scan ON flow_segments(scan_id);
CREATE INDEX IF NOT EXISTS idx_flow_segments_jam ON flow_segments(jam_factor);
`

// initFlowCaptureDB opens (creating if absent) traffic_data/flow_capture.db
// and applies the schema. Always called from Init() regardless of
// Config.FlowCaptureEnabled, so flipping that toggle at runtime (no
// restart) takes effect on the very next scan — only recordFlowScan's
// caller is gated.
func initFlowCaptureDB(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "flow_capture.db")
	var err error
	flowCaptureDB, err = sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := flowCaptureDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := flowCaptureDB.Exec(flowCaptureSchema); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}

// recordFlowScan persists one region's flow response: one flow_scans row
// (with the raw JSON verbatim) plus one flow_segments row per result
// (sub_index=-1) and, when present, one more per subSegment (sub_index =
// its position within that result's SubSegments).
func recordFlowScan(regionName string, scannedAt time.Time, sourceUpdated string, rawBytes int, rawJSON []byte, results []flowResult) error {
	if flowCaptureDB == nil {
		return nil
	}
	tx, err := flowCaptureDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO flow_scans (region_name, scanned_at, source_updated, raw_bytes, result_count, raw_json) VALUES (?, ?, ?, ?, ?, ?)`,
		regionName, scannedAt.UTC().Format(time.RFC3339), sourceUpdated, rawBytes, len(results), string(rawJSON),
	)
	if err != nil {
		return fmt.Errorf("insert flow_scans: %w", err)
	}
	scanID, err := res.LastInsertId()
	if err != nil {
		return err
	}

	insertSeg, err := tx.Prepare(`INSERT INTO flow_segments
		(scan_id, result_index, sub_index, description, length_m, speed_mps, speed_uncapped_mps, free_flow_mps,
		 jam_factor, has_jam_tendency, jam_tendency, confidence, traversability, functional_class,
		 first_lat, first_lng, last_lat, last_lng)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insertSeg.Close()

	for ri, r := range results {
		var functionalClass *int
		var firstLat, firstLng, lastLat, lastLng *float64
		if len(r.Links) > 0 {
			fc := r.Links[0].FunctionalClass
			functionalClass = &fc
			if pts := r.Links[0].Points; len(pts) > 0 {
				lat, lng := pts[0][0], pts[0][1]
				firstLat, firstLng = &lat, &lng
			}
			lastLink := r.Links[len(r.Links)-1]
			if pts := lastLink.Points; len(pts) > 0 {
				p := pts[len(pts)-1]
				lat, lng := p[0], p[1]
				lastLat, lastLng = &lat, &lng
			}
		}
		var jt *float64
		if r.HasJamTendency {
			v := r.JamTendency
			jt = &v
		}
		if _, err := insertSeg.Exec(scanID, ri, -1, r.Description, r.LengthM, r.SpeedMps, r.SpeedUncappedMps,
			r.FreeFlowMps, r.JamFactor, r.HasJamTendency, jt, r.Confidence, r.Traversability, functionalClass,
			firstLat, firstLng, lastLat, lastLng); err != nil {
			return fmt.Errorf("insert flow_segments (result %d): %w", ri, err)
		}
		for si, ss := range r.SubSegments {
			var sjt *float64
			if ss.HasJamTendency {
				v := ss.JamTendency
				sjt = &v
			}
			if _, err := insertSeg.Exec(scanID, ri, si, r.Description, ss.LengthM, ss.SpeedMps, ss.SpeedUncappedMps,
				ss.FreeFlowMps, ss.JamFactor, ss.HasJamTendency, sjt, ss.Confidence, ss.Traversability, functionalClass,
				nil, nil, nil, nil); err != nil {
				return fmt.Errorf("insert flow_segments (result %d sub %d): %w", ri, si, err)
			}
		}
	}

	return tx.Commit()
}

// cleanupFlowCaptureOlderThan deletes every flow_scans row (and its
// flow_segments rows — deleted first, in the same transaction, since this
// DB doesn't enable SQLite's FK-cascade pragma) whose scanned_at is older
// than days ago. A no-op when days <= 0 — callers are expected to check
// that themselves (see runScan's own "0 = forever" skip) since this
// function has no way to tell "disabled" apart from "clean up everything",
// which 0 would otherwise mean.
func cleanupFlowCaptureOlderThan(days int) error {
	if flowCaptureDB == nil || days <= 0 {
		return nil
	}
	cutoff := time.Now().AddDate(0, 0, -days).UTC().Format(time.RFC3339)

	tx, err := flowCaptureDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`DELETE FROM flow_segments WHERE scan_id IN (SELECT id FROM flow_scans WHERE scanned_at < ?)`, cutoff,
	); err != nil {
		return fmt.Errorf("delete old flow_segments: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM flow_scans WHERE scanned_at < ?`, cutoff); err != nil {
		return fmt.Errorf("delete old flow_scans: %w", err)
	}
	return tx.Commit()
}

// FlowScanSummary is one flow_scans row, for the "list recent scans" API.
type FlowScanSummary struct {
	ID            int64  `json:"id"`
	RegionName    string `json:"regionName"`
	ScannedAt     string `json:"scannedAt"`
	SourceUpdated string `json:"sourceUpdated"`
	RawBytes      int    `json:"rawBytes"`
	ResultCount   int    `json:"resultCount"`
}

// listFlowScans returns the most recent captured scans, newest first.
func listFlowScans(limit int) ([]FlowScanSummary, error) {
	if flowCaptureDB == nil {
		return nil, nil
	}
	rows, err := flowCaptureDB.Query(
		`SELECT id, region_name, scanned_at, source_updated, raw_bytes, result_count
		 FROM flow_scans ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FlowScanSummary{}
	for rows.Next() {
		var s FlowScanSummary
		if err := rows.Scan(&s.ID, &s.RegionName, &s.ScannedAt, &s.SourceUpdated, &s.RawBytes, &s.ResultCount); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// FlowSegmentRow is one flow_segments row. SpeedKmh/FreeFlowKmh are
// computed here (×3.6) for convenience — see flow_capture_parse.go's own
// doc comment for why the stored *_mps columns are metres/second, not km/h.
type FlowSegmentRow struct {
	ID               int64    `json:"id"`
	ScanID           int64    `json:"scanId"`
	ResultIndex      int      `json:"resultIndex"`
	SubIndex         int      `json:"subIndex"` // -1 = this row is the top-level result, not a subSegment
	Description      string   `json:"description"`
	LengthM          float64  `json:"lengthM"`
	SpeedMps         float64  `json:"speedMps"`
	SpeedKmh         float64  `json:"speedKmh"`
	SpeedUncappedMps float64  `json:"speedUncappedMps"`
	FreeFlowMps      float64  `json:"freeFlowMps"`
	FreeFlowKmh      float64  `json:"freeFlowKmh"`
	JamFactor        float64  `json:"jamFactor"`
	HasJamTendency   bool     `json:"hasJamTendency"`
	JamTendency      float64  `json:"jamTendency,omitempty"`
	Confidence       float64  `json:"confidence"`
	Traversability   string   `json:"traversability"`
	FunctionalClass  *int     `json:"functionalClass,omitempty"`
	FirstLat         *float64 `json:"firstLat,omitempty"`
	FirstLng         *float64 `json:"firstLng,omitempty"`
	LastLat          *float64 `json:"lastLat,omitempty"`
	LastLng          *float64 `json:"lastLng,omitempty"`
}

// listFlowSegments returns every segment (results + their subSegments) for
// one captured scan, in result/sub order.
func listFlowSegments(scanID int64) ([]FlowSegmentRow, error) {
	if flowCaptureDB == nil {
		return nil, nil
	}
	rows, err := flowCaptureDB.Query(
		`SELECT id, scan_id, result_index, sub_index, description, length_m, speed_mps, speed_uncapped_mps,
		        free_flow_mps, jam_factor, has_jam_tendency, jam_tendency, confidence, traversability,
		        functional_class, first_lat, first_lng, last_lat, last_lng
		 FROM flow_segments WHERE scan_id = ? ORDER BY result_index, sub_index`, scanID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FlowSegmentRow{}
	for rows.Next() {
		var s FlowSegmentRow
		var hasJT int
		var jt sql.NullFloat64
		if err := rows.Scan(&s.ID, &s.ScanID, &s.ResultIndex, &s.SubIndex, &s.Description, &s.LengthM,
			&s.SpeedMps, &s.SpeedUncappedMps, &s.FreeFlowMps, &s.JamFactor, &hasJT, &jt, &s.Confidence,
			&s.Traversability, &s.FunctionalClass, &s.FirstLat, &s.FirstLng, &s.LastLat, &s.LastLng); err != nil {
			return nil, err
		}
		s.HasJamTendency = hasJT != 0
		if jt.Valid {
			s.JamTendency = jt.Float64
		}
		s.SpeedKmh = s.SpeedMps * 3.6
		s.FreeFlowKmh = s.FreeFlowMps * 3.6
		out = append(out, s)
	}
	return out, rows.Err()
}

// latestFlowScanPerRegion returns the single most recent scan for each
// distinct region_name ever captured — one row per region, newest first.
// Used by the Flow Capture map's "always show the latest data, combined
// across every scanned region" view: regions are purely a bbox-scanning
// control (how the admin splits the area VietMap is queried for), not a
// meaningful way to split what the map displays, so the map always shows
// every region's own latest capture together rather than making the
// viewer pick one.
func latestFlowScanPerRegion() ([]FlowScanSummary, error) {
	if flowCaptureDB == nil {
		return nil, nil
	}
	rows, err := flowCaptureDB.Query(`
		SELECT s.id, s.region_name, s.scanned_at, s.source_updated, s.raw_bytes, s.result_count
		FROM flow_scans s
		INNER JOIN (SELECT region_name, MAX(id) AS max_id FROM flow_scans GROUP BY region_name) latest
			ON latest.region_name = s.region_name AND latest.max_id = s.id
		ORDER BY s.scanned_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FlowScanSummary{}
	for rows.Next() {
		var s FlowScanSummary
		if err := rows.Scan(&s.ID, &s.RegionName, &s.ScannedAt, &s.SourceUpdated, &s.RawBytes, &s.ResultCount); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// getFlowScan returns one scan's own metadata row.
func getFlowScan(id int64) (*FlowScanSummary, error) {
	if flowCaptureDB == nil {
		return nil, sql.ErrNoRows
	}
	var s FlowScanSummary
	err := flowCaptureDB.QueryRow(
		`SELECT id, region_name, scanned_at, source_updated, raw_bytes, result_count FROM flow_scans WHERE id = ?`, id,
	).Scan(&s.ID, &s.RegionName, &s.ScannedAt, &s.SourceUpdated, &s.RawBytes, &s.ResultCount)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// rawFlowScanJSON returns one scan's raw API response, verbatim.
func rawFlowScanJSON(scanID int64) (string, error) {
	if flowCaptureDB == nil {
		return "", sql.ErrNoRows
	}
	var raw string
	err := flowCaptureDB.QueryRow(`SELECT raw_json FROM flow_scans WHERE id = ?`, scanID).Scan(&raw)
	return raw, err
}
