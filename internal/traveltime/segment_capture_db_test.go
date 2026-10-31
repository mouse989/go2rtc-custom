package traveltime

import (
	"testing"
	"time"
)

func resetSegmentCaptureDB(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := initSegmentCaptureDB(dir); err != nil {
		t.Fatalf("initSegmentCaptureDB: %v", err)
	}
	t.Cleanup(func() {
		if segmentCaptureDB != nil {
			_ = segmentCaptureDB.Close()
			segmentCaptureDB = nil
		}
	})
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

	legs, err := getRouteSegmentLegs(metas[0].ID)
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

func TestCleanupSegmentCaptureOlderThan(t *testing.T) {
	resetSegmentCaptureDB(t)
	old := time.Now().AddDate(0, 0, -10)
	recent := time.Now().AddDate(0, 0, -1)
	if err := recordRouteSegments("r1", "Route A", old, testLegs()); err != nil {
		t.Fatalf("recordRouteSegments (old): %v", err)
	}
	if err := recordRouteSegments("r1", "Route A", recent, testLegs()); err != nil {
		t.Fatalf("recordRouteSegments (recent): %v", err)
	}
	if err := cleanupSegmentCaptureOlderThan(7); err != nil {
		t.Fatalf("cleanupSegmentCaptureOlderThan: %v", err)
	}
	var scanCount, segCount int
	if err := segmentCaptureDB.QueryRow(`SELECT COUNT(*) FROM route_segment_scans`).Scan(&scanCount); err != nil {
		t.Fatalf("count scans: %v", err)
	}
	if err := segmentCaptureDB.QueryRow(`SELECT COUNT(*) FROM route_segments`).Scan(&segCount); err != nil {
		t.Fatalf("count segments: %v", err)
	}
	if scanCount != 1 {
		t.Errorf("scanCount = %d, want 1 (only the recent scan should survive)", scanCount)
	}
	if segCount != 2 {
		t.Errorf("segCount = %d, want 2 (the recent scan's own legs, old scan's legs must be gone)", segCount)
	}
}

func TestCleanupSegmentCaptureNoopWhenDaysNotPositive(t *testing.T) {
	resetSegmentCaptureDB(t)
	veryOld := time.Now().AddDate(-1, 0, 0)
	if err := recordRouteSegments("r1", "Route A", veryOld, testLegs()); err != nil {
		t.Fatalf("recordRouteSegments: %v", err)
	}
	if err := cleanupSegmentCaptureOlderThan(0); err != nil {
		t.Fatalf("cleanupSegmentCaptureOlderThan(0): %v", err)
	}
	metas, err := latestRouteSegmentScans()
	if err != nil {
		t.Fatalf("latestRouteSegmentScans: %v", err)
	}
	if len(metas) != 1 {
		t.Errorf("got %d scans, want 1 — days<=0 must be a no-op", len(metas))
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
