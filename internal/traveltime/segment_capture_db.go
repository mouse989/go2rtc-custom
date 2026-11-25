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
// One SQLite file PER MONTH ("travel_route_segments_2006-01.db"), not one
// file for all time — added after a real deployment (250 routes, 5-minute
// interval) reached 300MB in a single file with no retention ever actually
// wired up (cleanupSegmentCaptureOlderThan existed but nothing ever called
// it — pure dead code, since removed). A month boundary gives two things a
// single ever-growing file can't: a corrupted/lost file only costs one
// month of per-leg detail instead of the whole history, and a closed past
// month is trivial to back up or archive on its own. Nothing is ever
// deleted automatically — every month file is kept indefinitely; see
// segment_capture_migrate.go for the one-time, admin-triggered move of a
// pre-existing single-file database into this layout.
//
// Driver: modernc.org/sqlite — pure Go (no cgo), same pattern
// internal/traffic/flow_capture_db.go already uses.

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/trafficmetrics"
	_ "modernc.org/sqlite"
)

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

// legacySegmentDBName is the single-file layout this package used before
// the per-month split — initSegmentCaptureDB never opens or writes to it;
// it is only ever read by segment_capture_migrate.go's one-time migration.
const legacySegmentDBName = "travel_route_segments.db"

var segmentMonthFileRe = regexp.MustCompile(`^travel_route_segments_(\d{4}-\d{2})\.db$`)

// segmentMonthKeyRe validates a bare "YYYY-MM" month key coming from an API
// query param, before it's used to build a file path — prevents path
// traversal via a crafted ?month= value.
var segmentMonthKeyRe = regexp.MustCompile(`^\d{4}-\d{2}$`)

var (
	segmentCaptureMu  sync.Mutex
	segmentCaptureDir string
	segmentDBs        = map[string]*sql.DB{} // monthKey ("2006-01") -> open, schema-applied DB
)

func segmentMonthDBPath(dir, monthKey string) string {
	return filepath.Join(dir, fmt.Sprintf("travel_route_segments_%s.db", monthKey))
}

func legacySegmentDBPath(dir string) string {
	return filepath.Join(dir, legacySegmentDBName)
}

// segmentMonthKeyFor formats t (already expected in Vietnam time — callers
// pass time.Now().In(loc()) or an already-local time.Time) as "2006-01".
func segmentMonthKeyFor(t time.Time) string {
	return t.In(loc()).Format("2006-01")
}

// initSegmentCaptureDB sets the storage directory and opens (creating if
// absent) the CURRENT month's database. Past months are opened lazily, on
// first use, by segmentDBForMonth — most server runtimes never need more
// than the current month open.
func initSegmentCaptureDB(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	segmentCaptureMu.Lock()
	segmentCaptureDir = dir
	segmentCaptureMu.Unlock()
	_, err := segmentDBForMonth(segmentMonthKeyFor(time.Now()))
	return err
}

// segmentDBForMonth returns the opened, schema-applied DB for monthKey
// ("2006-01"), opening and caching it on first use — safe to call for any
// valid month, past or current. A month with no file yet is created empty;
// only the current month's write path (recordRouteSegments) is expected to
// ever populate a fresh one in practice.
func segmentDBForMonth(monthKey string) (*sql.DB, error) {
	// Defense in depth: every caller of this function builds monthKey from
	// time.Format("2006-01") internally except the admin history API,
	// which already validates the query param before reaching here — this
	// guards the actual path-building choke point itself, so a future
	// caller can never turn an unvalidated monthKey into a path-traversal
	// read/create outside segmentCaptureDir.
	if !segmentMonthKeyRe.MatchString(monthKey) {
		return nil, fmt.Errorf("invalid month key %q", monthKey)
	}
	segmentCaptureMu.Lock()
	defer segmentCaptureMu.Unlock()
	if db, ok := segmentDBs[monthKey]; ok {
		return db, nil
	}
	path := segmentMonthDBPath(segmentCaptureDir, monthKey)
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.Exec(segmentCaptureSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrateRouteSegmentsMetricsColumns(db); err != nil {
		db.Close()
		return nil, err
	}
	segmentDBs[monthKey] = db
	return db, nil
}

// currentSegmentDB returns the writable DB for whatever month it is right
// now — recordRouteSegments' only entry point into this store. Re-resolved
// on every call (not cached beyond segmentDBForMonth's own cache) so a
// write right after a month boundary correctly lands in the new month's
// file without needing an explicit "rotate" step.
func currentSegmentDB() (*sql.DB, error) {
	return segmentDBForMonth(segmentMonthKeyFor(time.Now()))
}

// routeSegmentsMetricsColumns are the LOS columns added after this table's
// initial release (internal/trafficmetrics, classified straight from TTI
// per HCM's Urban Streets methodology — no JF available per leg, so no
// fused TTI/anomaly here either, unlike internal/traffic's flow_segments
// equivalent). Added via ALTER TABLE, same convention
// internal/traffic/flow_capture_db.go's own migration uses — applied to
// EVERY month's DB on open (segmentDBForMonth), not just the current one,
// since a freshly-created month file always starts from segmentCaptureSchema
// as it stood when this binary was built, which already includes these
// columns in CREATE TABLE; this list only matters for re-opening an older
// file written by a binary from before a given column existed. (z_est/
// z_percent columns from an earlier BPR-inverse Z estimate were removed
// entirely — this list only ever adds columns, never drops the now-dead
// ones from an already-migrated DB, which is harmless: nothing reads them.)
var routeSegmentsMetricsColumns = []string{
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
// — one route_segment_scans row plus one route_segments row per leg, into
// whichever month's DB "now" falls in.
func recordRouteSegments(routeID, routeName string, scannedAt time.Time, legs []RouteLegStat) error {
	if len(legs) == 0 {
		return nil
	}
	db, err := currentSegmentDB()
	if err != nil {
		return fmt.Errorf("open current month's segment capture DB: %w", err)
	}

	tx, err := db.Begin()
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
		 los_label, los_color)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insertLeg.Close()

	// internal/trafficmetrics: LOS classified straight from this leg's own
	// TTI (no fused TTI/anomaly — a route leg has no JF reading of its own
	// to fuse with or cross-check against; see ComputeRouteLeg's own doc
	// comment for why this differs from internal/traffic's flow-capture
	// path, which has speed+freeFlow+JF all from the same reading).
	metricsCfg := trafficmetrics.GetConfig()

	for _, leg := range legs {
		los := metricsCfg.ComputeRouteLeg(leg.TTI)
		if _, err := insertLeg.Exec(scanID, leg.LegIndex, leg.FromLat, leg.FromLng, leg.ToLat, leg.ToLng,
			leg.LengthM, leg.DurationSec, leg.BaseSec, leg.DelaySec, leg.TTI,
			los.Label, los.Color); err != nil {
			return fmt.Errorf("insert route_segments (leg %d): %w", leg.LegIndex, err)
		}
	}
	return tx.Commit()
}

// routeSegmentScanMeta is one route_segment_scans row. MonthKey records
// which month's DB it came from — scan ids are only unique WITHIN one
// month's file, so any lookup by ID (getRouteSegmentLegs) needs both.
type routeSegmentScanMeta struct {
	ID        int64
	MonthKey  string
	RouteID   string
	RouteName string
	ScannedAt string
}

// latestRouteSegmentScans returns the single most recent scan for every
// distinct route_id captured in the current month or the previous one —
// two months, not just the current one, so the brief window right after a
// month rolls over (before that route's next 5-minute cycle has run in the
// new month's empty file) doesn't read as "no data" for a route that in
// fact was scanned minutes ago, just in last month's file.
func latestRouteSegmentScans() ([]routeSegmentScanMeta, error) {
	now := time.Now().In(loc())
	curKey := segmentMonthKeyFor(now)
	firstOfThisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	prevKey := segmentMonthKeyFor(firstOfThisMonth.AddDate(0, 0, -1))

	monthKeys := []string{curKey}
	if prevKey != curKey {
		monthKeys = append(monthKeys, prevKey)
	}

	best := map[string]routeSegmentScanMeta{} // routeID -> most recent across both months
	for _, mk := range monthKeys {
		db, err := segmentDBForMonth(mk)
		if err != nil {
			continue // that month's file genuinely unavailable — skip, not fatal to the other month
		}
		rows, err := db.Query(`
			SELECT s.id, s.route_id, s.route_name, s.scanned_at
			FROM route_segment_scans s
			INNER JOIN (SELECT route_id, MAX(id) AS max_id FROM route_segment_scans GROUP BY route_id) latest
				ON latest.route_id = s.route_id AND latest.max_id = s.id`)
		if err != nil {
			continue
		}
		for rows.Next() {
			var m routeSegmentScanMeta
			if err := rows.Scan(&m.ID, &m.RouteID, &m.RouteName, &m.ScannedAt); err != nil {
				continue
			}
			m.MonthKey = mk
			// scanned_at is an RFC3339 UTC string — lexicographically
			// sortable, same convention this package already relies on
			// elsewhere for timestamp comparisons.
			if ex, ok := best[m.RouteID]; !ok || m.ScannedAt > ex.ScannedAt {
				best[m.RouteID] = m
			}
		}
		rows.Close()
	}

	out := make([]routeSegmentScanMeta, 0, len(best))
	for _, m := range best {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ScannedAt > out[j].ScannedAt })
	return out, nil
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
	LOSLabel    string
	LOSColor    string
}

func getRouteSegmentLegs(monthKey string, scanID int64) ([]legRow, error) {
	db, err := segmentDBForMonth(monthKey)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`
		SELECT leg_index, from_lat, from_lng, to_lat, to_lng, length_m, duration_sec, base_sec, delay_sec, tti,
		       los_label, los_color
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
			&l.LOSLabel, &l.LOSColor); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
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
//
// LOSLabel/LOSColor are recomputed here from the CURRENT trafficmetrics
// config, not read from the leg's own persisted columns — this is "latest"
// data shown live on the map, so it must track an admin's LOS threshold
// edits immediately, the same way resultsToGeometry (flowcapture_page_api.go,
// the sibling "ambient" layer) already recomputes live rather than trusting
// its own persisted los_label/los_color. The persisted columns stay as the
// historical record of what was classified at capture time — see
// queryRouteSegmentHistory for the date-scoped caller that wants that
// frozen value instead.
func LatestRouteSegments() ([]RouteSegments, error) {
	metas, err := latestRouteSegmentScans()
	if err != nil {
		return nil, err
	}
	metricsCfg := trafficmetrics.GetConfig()
	out := make([]RouteSegments, 0, len(metas))
	for _, m := range metas {
		legs, err := getRouteSegmentLegs(m.MonthKey, m.ID)
		if err != nil || len(legs) == 0 {
			continue
		}
		rs := RouteSegments{RouteID: m.RouteID, RouteName: m.RouteName, ScannedAt: m.ScannedAt}
		for _, l := range legs {
			los := metricsCfg.ComputeRouteLeg(l.TTI)
			rs.Segments = append(rs.Segments, RouteSegmentDetail{
				LegIndex: l.LegIndex, FromLat: l.FromLat, FromLng: l.FromLng, ToLat: l.ToLat, ToLng: l.ToLng,
				LengthM: l.LengthM, DurationSec: l.DurationSec, BaseSec: l.BaseSec, DelaySec: l.DelaySec, TTI: l.TTI,
				LOSLabel: los.Label, LOSColor: los.Color,
			})
		}
		out = append(out, rs)
	}
	return out, nil
}

// ── Historical query (admin "tra cứu dữ liệu tháng cũ") ──────────────────

// SegmentCaptureMonth is one available month's file, for the admin picker.
type SegmentCaptureMonth struct {
	MonthKey string `json:"monthKey"` // "2026-01"
	SizeKB   int64  `json:"sizeKb"`
}

// ListSegmentCaptureMonths scans segmentCaptureDir for per-month database
// files and returns them newest-first, with each file's current on-disk
// size — lets an admin see at a glance how large each month's capture is
// without opening every file.
func ListSegmentCaptureMonths() ([]SegmentCaptureMonth, error) {
	segmentCaptureMu.Lock()
	dir := segmentCaptureDir
	segmentCaptureMu.Unlock()
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := []SegmentCaptureMonth{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := segmentMonthFileRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, SegmentCaptureMonth{MonthKey: m[1], SizeKB: info.Size() / 1024})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MonthKey > out[j].MonthKey })
	return out, nil
}

// RouteSegmentHistoryPoint is one scan's full leg detail, for the
// historical query API/UI — same per-leg shape as RouteSegmentDetail plus
// the scan's own timestamp, since a history result spans many scans.
type RouteSegmentHistoryPoint struct {
	ScannedAt string               `json:"scannedAt"`
	Segments  []RouteSegmentDetail `json:"segments"`
}

// QueryRouteSegmentHistoryParams bounds a history lookup — routeID is
// mandatory (an unscoped query across 250 routes × a whole month would be
// enormous); from/to further narrow within that month, both optional.
type QueryRouteSegmentHistoryParams struct {
	MonthKey string // "2006-01", required
	RouteID  string // required
	From, To string // RFC3339, optional — inclusive bounds within the month
	Limit    int    // 0 → queryHistoryDefaultLimit
}

const (
	queryHistoryDefaultLimit = 500
	queryHistoryMaxLimit     = 5000
)

// QueryRouteSegmentHistory returns one route's captured scans (each with
// every leg's own frozen TTI/LOS as recorded at the time, not recomputed
// against today's trafficmetrics config — this is a historical record,
// unlike LatestRouteSegments) within one month, oldest first, capped at
// Limit (or queryHistoryDefaultLimit).
func QueryRouteSegmentHistory(p QueryRouteSegmentHistoryParams) ([]RouteSegmentHistoryPoint, error) {
	if p.RouteID == "" {
		return nil, fmt.Errorf("routeId is required")
	}
	limit := p.Limit
	if limit <= 0 {
		limit = queryHistoryDefaultLimit
	} else if limit > queryHistoryMaxLimit {
		limit = queryHistoryMaxLimit
	}

	db, err := segmentDBForMonth(p.MonthKey)
	if err != nil {
		return nil, err
	}

	query := `SELECT id, scanned_at FROM route_segment_scans WHERE route_id = ?`
	args := []any{p.RouteID}
	if p.From != "" {
		query += ` AND scanned_at >= ?`
		args = append(args, p.From)
	}
	if p.To != "" {
		query += ` AND scanned_at <= ?`
		args = append(args, p.To)
	}
	query += ` ORDER BY scanned_at ASC LIMIT ?`
	args = append(args, limit)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	type scanIDTime struct {
		id        int64
		scannedAt string
	}
	var scans []scanIDTime
	for rows.Next() {
		var s scanIDTime
		if err := rows.Scan(&s.id, &s.scannedAt); err != nil {
			rows.Close()
			return nil, err
		}
		scans = append(scans, s)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	out := make([]RouteSegmentHistoryPoint, 0, len(scans))
	for _, s := range scans {
		legs, err := getRouteSegmentLegs(p.MonthKey, s.id)
		if err != nil {
			return nil, err
		}
		pt := RouteSegmentHistoryPoint{ScannedAt: s.scannedAt}
		for _, l := range legs {
			pt.Segments = append(pt.Segments, RouteSegmentDetail{
				LegIndex: l.LegIndex, FromLat: l.FromLat, FromLng: l.FromLng, ToLat: l.ToLat, ToLng: l.ToLng,
				LengthM: l.LengthM, DurationSec: l.DurationSec, BaseSec: l.BaseSec, DelaySec: l.DelaySec, TTI: l.TTI,
				LOSLabel: l.LOSLabel, LOSColor: l.LOSColor,
			})
		}
		out = append(out, pt)
	}
	return out, nil
}
