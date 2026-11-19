package traveltime

import (
	"os"
	"testing"
	"time"
)

func resetSegmentCaptureDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := initSegmentCaptureDB(dir); err != nil {
		t.Fatalf("initSegmentCaptureDB: %v", err)
	}
	t.Cleanup(func() {
		segmentCaptureMu.Lock()
		for k, db := range segmentDBs {
			db.Close()
			delete(segmentDBs, k)
		}
		segmentCaptureDir = ""
		segmentCaptureMu.Unlock()
	})
	return dir
}

func testLegs() []RouteLegStat {
	return []RouteLegStat{
		{LegIndex: 0, FromLat: 10.77, FromLng: 106.70, ToLat: 10.78, ToLng: 106.69, LengthM: 2000, DurationSec: 300, BaseSec: 200, DelaySec: 100, TTI: 1.5},
		{LegIndex: 1, FromLat: 10.78, FromLng: 106.69, ToLat: 10.79, ToLng: 106.68, LengthM: 3000, DurationSec: 200, BaseSec: 200, DelaySec: 0, TTI: 1.0},
	}
}

func TestRecordAndFetchRouteSegments(t *testing.T) {
	resetSegmentCaptureDB(t)
	scannedAt := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	if err := recordRouteSegments("r1", "Test Route", scannedAt, testLegs()); err != nil {
		t.Fatalf("recordRouteSegments: %v", err)
	}

	metas, err := latestRouteSegmentScans()
	if err != nil {
		t.Fatalf("latestRouteSegmentScans: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("got %d scans, want 1", len(metas))
	}
	if metas[0].RouteID != "r1" || metas[0].RouteName != "Test Route" {
		t.Errorf("meta = %+v, want route r1/Test Route", metas[0])
	}

	legs, err := getRouteSegmentLegs(metas[0].MonthKey, metas[0].ID)
	if err != nil {
		t.Fatalf("getRouteSegmentLegs: %v", err)
	}
	if len(legs) != 2 {
		t.Fatalf("got %d legs, want 2", len(legs))
	}
	if legs[0].TTI != 1.5 || legs[1].TTI != 1.0 {
		t.Errorf("leg TTIs = [%v, %v], want [1.5, 1.0]", legs[0].TTI, legs[1].TTI)
	}
	if legs[0].ToLat != legs[1].FromLat || legs[0].ToLng != legs[1].FromLng {
		t.Error("consecutive legs should share the same connecting point")
	}
}

// TestRecordRouteSegmentsPopulatesLOS pins that internal/trafficmetrics
// (TTI-only LOS path, ComputeRouteLeg — classified per HCM Urban Streets
// speed-ratio bands) is actually wired into recordRouteSegments' insert.
// testLegs()'s leg[1] has TTI=1.0 (free flow) so must classify as the
// lowest LOS band (A); leg[0] has TTI=1.5 so must classify as C (moderate
// congestion, not the worst tier — the exact case that motivated removing
// the old BPR-inverse Z estimate).
func TestRecordRouteSegmentsPopulatesLOS(t *testing.T) {
	resetSegmentCaptureDB(t)
	if err := recordRouteSegments("r1", "Test Route", time.Now(), testLegs()); err != nil {
		t.Fatalf("recordRouteSegments: %v", err)
	}
	metas, err := latestRouteSegmentScans()
	if err != nil {
		t.Fatalf("latestRouteSegmentScans: %v", err)
	}
	legs, err := getRouteSegmentLegs(metas[0].MonthKey, metas[0].ID)
	if err != nil {
		t.Fatalf("getRouteSegmentLegs: %v", err)
	}
	if len(legs) != 2 {
		t.Fatalf("got %d legs, want 2", len(legs))
	}
	if legs[1].LOSLabel != "A" {
		t.Errorf("free-flow leg (TTI=1.0) LOSLabel = %q, want A", legs[1].LOSLabel)
	}
	if legs[0].LOSLabel != "C" || legs[0].LOSColor == "" {
		t.Errorf("congested leg (TTI=1.5) = %+v, want LOSLabel=C (not the worst tier), color populated", legs[0])
	}

	all, err := LatestRouteSegments()
	if err != nil {
		t.Fatalf("LatestRouteSegments: %v", err)
	}
	if len(all) != 1 || len(all[0].Segments) != 2 {
		t.Fatalf("LatestRouteSegments = %+v, want 1 route with 2 segments", all)
	}
	if all[0].Segments[0].LOSLabel == "" {
		t.Errorf("exported RouteSegmentDetail missing LOSLabel: %+v", all[0].Segments[0])
	}
}

func TestLatestRouteSegmentsOnlyKeepsNewestPerRoute(t *testing.T) {
	resetSegmentCaptureDB(t)
	older := time.Date(2026, 10, 30, 11, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	if err := recordRouteSegments("r1", "Route A", older, testLegs()); err != nil {
		t.Fatalf("recordRouteSegments (older): %v", err)
	}
	if err := recordRouteSegments("r1", "Route A", newer, testLegs()); err != nil {
		t.Fatalf("recordRouteSegments (newer): %v", err)
	}
	if err := recordRouteSegments("r2", "Route B", older, testLegs()); err != nil {
		t.Fatalf("recordRouteSegments (r2): %v", err)
	}

	all, err := LatestRouteSegments()
	if err != nil {
		t.Fatalf("LatestRouteSegments: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d routes, want 2 (one per distinct route, each its own latest)", len(all))
	}
	byID := map[string]RouteSegments{}
	for _, rs := range all {
		byID[rs.RouteID] = rs
	}
	a, ok := byID["r1"]
	if !ok {
		t.Fatal("r1 missing")
	}
	if a.ScannedAt != newer.UTC().Format(time.RFC3339) {
		t.Errorf("r1 ScannedAt = %q, want the NEWER scan, not the older one", a.ScannedAt)
	}
	if len(a.Segments) != 2 {
		t.Errorf("r1 has %d segments, want 2", len(a.Segments))
	}
	if _, ok := byID["r2"]; !ok {
		t.Error("r2 missing — a route scanned only once must still appear")
	}
}

// recordRouteSegments always writes into whichever month's file wall-clock
// "now" falls in (not scannedAt — see currentSegmentDB's doc comment), so
// every write in this test suite lands in one real file:
// travel_route_segments_<this month>.db. This test confirms that file
// actually exists on disk with the expected name, and that
// ListSegmentCaptureMonths reports it.
func TestRecordRouteSegmentsWritesCurrentMonthFile(t *testing.T) {
	dir := resetSegmentCaptureDB(t)
	if err := recordRouteSegments("r1", "Route A", time.Now(), testLegs()); err != nil {
		t.Fatalf("recordRouteSegments: %v", err)
	}

	wantKey := segmentMonthKeyFor(time.Now())
	path := segmentMonthDBPath(dir, wantKey)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected %s to exist: %v", path, err)
	}

	months, err := ListSegmentCaptureMonths()
	if err != nil {
		t.Fatalf("ListSegmentCaptureMonths: %v", err)
	}
	if len(months) != 1 || months[0].MonthKey != wantKey {
		t.Errorf("ListSegmentCaptureMonths = %+v, want exactly [%q]", months, wantKey)
	}
	if months[0].SizeKB < 0 {
		t.Errorf("SizeKB = %d, want >= 0", months[0].SizeKB)
	}
}

// latestRouteSegmentScans must still find a route's latest scan when that
// scan lives in LAST month's file — the brief window right after a month
// rollover, before a route's next cycle has written anything into the new
// (empty) current-month file.
func TestLatestRouteSegmentScansSpansMonthBoundary(t *testing.T) {
	resetSegmentCaptureDB(t)

	now := time.Now().In(loc())
	firstOfThisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	prevMonthKey := segmentMonthKeyFor(firstOfThisMonth.AddDate(0, 0, -1))

	// Write directly into last month's file, bypassing recordRouteSegments
	// (which always targets "now"'s file) — simulates a scan that really
	// happened before this month started.
	prevDB, err := segmentDBForMonth(prevMonthKey)
	if err != nil {
		t.Fatalf("segmentDBForMonth(%q): %v", prevMonthKey, err)
	}
	res, err := prevDB.Exec(`INSERT INTO route_segment_scans (route_id, route_name, scanned_at) VALUES (?, ?, ?)`,
		"r1", "Route A", firstOfThisMonth.Add(-time.Hour).UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("insert into previous month: %v", err)
	}
	scanID, _ := res.LastInsertId()
	if _, err := prevDB.Exec(`INSERT INTO route_segments
		(scan_id, leg_index, from_lat, from_lng, to_lat, to_lng, length_m, duration_sec, base_sec, delay_sec, tti, los_label, los_color)
		VALUES (?, 0, 10, 106, 10.1, 106.1, 1000, 100, 100, 0, 1.0, 'A', '#000')`, scanID); err != nil {
		t.Fatalf("insert leg into previous month: %v", err)
	}

	all, err := LatestRouteSegments()
	if err != nil {
		t.Fatalf("LatestRouteSegments: %v", err)
	}
	if len(all) != 1 || all[0].RouteID != "r1" {
		t.Fatalf("expected to find r1's scan from last month's file, got %+v", all)
	}
	if len(all[0].Segments) != 1 {
		t.Errorf("expected 1 leg, got %d", len(all[0].Segments))
	}
}

func TestQueryRouteSegmentHistoryRequiresRouteID(t *testing.T) {
	resetSegmentCaptureDB(t)
	if _, err := QueryRouteSegmentHistory(QueryRouteSegmentHistoryParams{MonthKey: segmentMonthKeyFor(time.Now())}); err == nil {
		t.Error("expected an error when RouteID is empty")
	}
}

func TestQueryRouteSegmentHistoryReturnsOldestFirstWithinMonth(t *testing.T) {
	resetSegmentCaptureDB(t)
	t1 := time.Now().Add(-2 * time.Hour)
	t2 := time.Now().Add(-1 * time.Hour)
	if err := recordRouteSegments("r1", "Route A", t1, testLegs()); err != nil {
		t.Fatalf("recordRouteSegments (t1): %v", err)
	}
	if err := recordRouteSegments("r1", "Route A", t2, testLegs()); err != nil {
		t.Fatalf("recordRouteSegments (t2): %v", err)
	}
	// A different route must never appear in r1's history.
	if err := recordRouteSegments("r2", "Route B", t1, testLegs()); err != nil {
		t.Fatalf("recordRouteSegments (r2): %v", err)
	}

	hist, err := QueryRouteSegmentHistory(QueryRouteSegmentHistoryParams{
		MonthKey: segmentMonthKeyFor(time.Now()), RouteID: "r1",
	})
	if err != nil {
		t.Fatalf("QueryRouteSegmentHistory: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("got %d history points, want 2 (only r1's own scans)", len(hist))
	}
	if hist[0].ScannedAt != t1.UTC().Format(time.RFC3339) || hist[1].ScannedAt != t2.UTC().Format(time.RFC3339) {
		t.Errorf("expected oldest-first ordering [t1, t2], got [%s, %s]", hist[0].ScannedAt, hist[1].ScannedAt)
	}
	if len(hist[0].Segments) != 2 {
		t.Errorf("expected 2 legs per scan, got %d", len(hist[0].Segments))
	}
}

func TestQueryRouteSegmentHistoryRespectsFromTo(t *testing.T) {
	resetSegmentCaptureDB(t)
	early := time.Now().Add(-3 * time.Hour)
	mid := time.Now().Add(-2 * time.Hour)
	late := time.Now().Add(-1 * time.Hour)
	for _, ts := range []time.Time{early, mid, late} {
		if err := recordRouteSegments("r1", "Route A", ts, testLegs()); err != nil {
			t.Fatalf("recordRouteSegments: %v", err)
		}
	}

	hist, err := QueryRouteSegmentHistory(QueryRouteSegmentHistoryParams{
		MonthKey: segmentMonthKeyFor(time.Now()), RouteID: "r1",
		From: mid.UTC().Format(time.RFC3339), To: late.UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("QueryRouteSegmentHistory: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("got %d history points, want 2 (mid and late, early excluded by From)", len(hist))
	}
}

func TestRecordRouteSegmentsSkipsWhenNoLegs(t *testing.T) {
	resetSegmentCaptureDB(t)
	if err := recordRouteSegments("r1", "Route A", time.Now(), nil); err != nil {
		t.Errorf("recordRouteSegments with no legs = %v, want nil (no-op)", err)
	}
	metas, err := latestRouteSegmentScans()
	if err != nil {
		t.Fatalf("latestRouteSegmentScans: %v", err)
	}
	if len(metas) != 0 {
		t.Errorf("got %d scans, want 0 — an empty legs slice shouldn't record anything", len(metas))
	}
}
