package traffic

import (
	"testing"
	"time"
)

// TestLatestFlowScanPerRegionExcludesGhostRegions proves the fix for the
// user's report: a region renamed/removed from Config.Regions left its
// last scan permanently "latest" under its old name (GROUP BY region_name
// has no idea the region is gone), so the Flow Capture map kept merging
// that stale, possibly much larger capture in next to whatever the
// CURRENTLY configured regions are producing — while the admin's own scan
// log (listFlowScans, unfiltered) correctly showed every real recent scan
// returning only a handful of results.
func TestLatestFlowScanPerRegionExcludesGhostRegions(t *testing.T) {
	resetFlowCaptureDB(t)

	results, sourceUpdated, err := parseFlowResults([]byte(realSampleJSON))
	if err != nil {
		t.Fatalf("parseFlowResults: %v", err)
	}

	base := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

	// A "ghost" region (renamed/removed from config) with a scan that has
	// MANY results, recorded BEFORE the two scans below.
	if err := recordFlowScan("Khu vực cũ (đã xoá)", base, sourceUpdated, 100, []byte(realSampleJSON), results); err != nil {
		t.Fatalf("recordFlowScan (ghost): %v", err)
	}
	// The two CURRENTLY configured regions, each with their own small,
	// genuinely latest scan.
	if err := recordFlowScan("TPHCM BD", base.Add(time.Minute), sourceUpdated, 10, []byte(realSampleJSON), results[:1]); err != nil {
		t.Fatalf("recordFlowScan (TPHCM BD): %v", err)
	}
	if err := recordFlowScan("BR VT", base.Add(2*time.Minute), sourceUpdated, 10, []byte(realSampleJSON), results[:1]); err != nil {
		t.Fatalf("recordFlowScan (BR VT): %v", err)
	}

	setTestRegions(t, []Region{
		{Name: "TPHCM BD", Enabled: true, Coords: [][2]float64{{10, 106}}},
		{Name: "BR VT", Enabled: true, Coords: [][2]float64{{10, 107}}},
		// "Khu vực cũ (đã xoá)" deliberately absent — simulates deletion.
	})

	scans, err := latestFlowScanPerRegion()
	if err != nil {
		t.Fatalf("latestFlowScanPerRegion: %v", err)
	}
	if len(scans) != 2 {
		t.Fatalf("got %d regions, want 2 (ghost region must be excluded): %+v", len(scans), scans)
	}
	for _, s := range scans {
		if s.RegionName == "Khu vực cũ (đã xoá)" {
			t.Fatalf("ghost region leaked into latestFlowScanPerRegion: %+v", scans)
		}
	}

	// Disabling a currently-configured region must also exclude it (same
	// "not actively scanning right now" reasoning as a deleted one).
	cfgMu.Lock()
	cfg.Regions[1].Enabled = false
	cfgMu.Unlock()
	scans, err = latestFlowScanPerRegion()
	if err != nil {
		t.Fatalf("latestFlowScanPerRegion (after disable): %v", err)
	}
	if len(scans) != 1 || scans[0].RegionName != "TPHCM BD" {
		t.Fatalf("disabling BR VT should leave only TPHCM BD, got: %+v", scans)
	}

	t.Log("OK: latestFlowScanPerRegion only returns currently-enabled, currently-configured regions")
}
