package traffic

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/traveltime"
)

// ensureTravelTimeInit initializes internal/traveltime's own route store
// exactly once for this whole test binary — same sync.Once +
// app.ConfigPath-pointed-at-a-temp-dir convention
// internal/trafficinsight's own setupSources(t) already established for
// calling another package's Init() from a package that isn't it (see
// internal/trafficinsight/insight_test.go). computeHotspots calls
// traveltime.ListRoutes() unconditionally, which panics on traveltime's
// unexported nil rstore if Init() was never called.
var (
	ttInitOnce sync.Once
)

func ensureTravelTimeInit(t *testing.T) {
	t.Helper()
	ttInitOnce.Do(func() {
		dir, err := os.MkdirTemp("", "tti-suggest-test-")
		if err != nil {
			t.Fatalf("mkdir temp dir: %v", err)
		}
		app.ConfigPath = filepath.Join(dir, "go2rtc.yaml")
		traveltime.Init()
	})
}

// seedFlowScan records one synthetic scan of a single named road, with
// the given jamFactor/speed/freeFlow — recordFlowScan (flow_capture_db.go)
// computes and stores tti_fused/los_label itself via internal/trafficmetrics,
// exactly as a real scan would.
func seedFlowScan(t *testing.T, region, description string, scannedAt time.Time, speedMps, freeFlowMps, jamFactor float64, firstLat, firstLng, lastLat, lastLng float64) {
	t.Helper()
	fr := flowResult{
		Description: description,
		LengthM:     900,
		SpeedMps:    speedMps,
		FreeFlowMps: freeFlowMps,
		JamFactor:   jamFactor,
		Links: []flowLink{{
			Points: [][2]float64{{firstLat, firstLng}, {lastLat, lastLng}},
		}},
	}
	if err := recordFlowScan(region, scannedAt, "", 0, []byte("{}"), []flowResult{fr}); err != nil {
		t.Fatalf("recordFlowScan: %v", err)
	}
}

func TestComputeHotspotsGroupsByRegionAndDescriptionAndRanksByRate(t *testing.T) {
	resetFlowCaptureDB(t)
	ensureTravelTimeInit(t)

	base := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)

	// "Đường A": congested (low speed vs free flow) in 4 of 5 scans.
	for i := 0; i < 5; i++ {
		speed := 2.0 // slow
		if i == 2 {
			speed = 8.0 // one free-flowing scan
		}
		seedFlowScan(t, "Quận 1", "Đường A", base.Add(time.Duration(i)*5*time.Minute),
			speed, 8.0, 8.5, 10.80, 106.70, 10.81, 106.71)
	}

	// "Đường B": never congested (always near free-flow speed).
	for i := 0; i < 5; i++ {
		seedFlowScan(t, "Quận 1", "Đường B", base.Add(time.Duration(i)*5*time.Minute),
			7.5, 8.0, 2.0, 10.90, 106.80, 10.91, 106.81)
	}

	report, err := computeHotspots(base.Add(-time.Hour), base.Add(time.Hour), 0, 0, 0, 0)
	if err != nil {
		t.Fatalf("computeHotspots: %v", err)
	}
	out := report.Hotspots

	var a, b *HotspotSuggestion
	for i := range out {
		switch out[i].Description {
		case "Đường A":
			a = &out[i]
		case "Đường B":
			b = &out[i]
		}
	}
	if a == nil {
		t.Fatalf("expected Đường A in results, got %+v", out)
	}
	if a.ScanCount != 5 || a.CongestedCount != 4 {
		t.Errorf("Đường A: ScanCount=%d CongestedCount=%d, want 5/4", a.ScanCount, a.CongestedCount)
	}
	if a.CongestionRate != 0.8 {
		t.Errorf("Đường A: CongestionRate=%v, want 0.8", a.CongestionRate)
	}
	// Đường B's congestion rate (0) is below the default minRate (0.2), so
	// it must be filtered out entirely.
	if b != nil {
		t.Errorf("expected Đường B to be filtered out (never congested), got %+v", b)
	}
}

func TestComputeHotspotsDedupsAgainstExistingRoute(t *testing.T) {
	resetFlowCaptureDB(t)
	ensureTravelTimeInit(t)

	base := time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		seedFlowScan(t, "Quận 7", "Nguyễn Văn Linh", base.Add(time.Duration(i)*5*time.Minute),
			2.0, 8.0, 8.5, 10.72, 106.70, 10.73, 106.71)
	}

	// A route already tracking almost exactly that same span.
	if _, err := traveltime.CreateRoute(&traveltime.Route{
		Name:        "NVL đã theo dõi",
		Origin:      "10.72001,106.70001",
		Destination: "10.73001,106.71001",
	}); err != nil {
		t.Fatalf("CreateRoute: %v", err)
	}

	report, err := computeHotspots(base.Add(-time.Hour), base.Add(time.Hour), 0, 0, 0, 0)
	if err != nil {
		t.Fatalf("computeHotspots: %v", err)
	}
	out := report.Hotspots
	if len(out) != 1 {
		t.Fatalf("expected exactly 1 hotspot, got %d: %+v", len(out), out)
	}
	if !out[0].Covered {
		t.Errorf("expected hotspot to be marked covered by the existing route, got Covered=false")
	}
	if out[0].CoveredByRoute != "NVL đã theo dõi" {
		t.Errorf("CoveredByRoute = %q, want %q", out[0].CoveredByRoute, "NVL đã theo dõi")
	}
}

func TestComputeHotspotsMinScansFiltersSparseGroups(t *testing.T) {
	resetFlowCaptureDB(t)
	ensureTravelTimeInit(t)

	base := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	// Only 2 scans, both congested — should still be dropped by the
	// default minScans=3 (too little evidence to call it "recurring").
	for i := 0; i < 2; i++ {
		seedFlowScan(t, "Quận 2", "Đường C", base.Add(time.Duration(i)*5*time.Minute),
			1.5, 8.0, 9.0, 10.78, 106.75, 10.79, 106.76)
	}

	report, err := computeHotspots(base.Add(-time.Hour), base.Add(time.Hour), 0, 0, 0, 0)
	if err != nil {
		t.Fatalf("computeHotspots: %v", err)
	}
	for _, h := range report.Hotspots {
		if h.Description == "Đường C" {
			t.Fatalf("expected Đường C (only 2 scans) to be filtered out by minScans, got %+v", h)
		}
	}
}

func TestComputeHotspotsRequiresValidRangeAndReportsDataQuality(t *testing.T) {
	resetFlowCaptureDB(t)
	ensureTravelTimeInit(t)

	base := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	seedFlowScan(t, "Quận 1", "Đường D", base, 2.0, 8.0, 8.5, 10.80, 106.70, 10.81, 106.71)
	// A second scan with no Links at all — recordFlowScan stores NULL
	// first/last lat,lng for a result like this, same as a real provider
	// response that omitted shape.links for that result.
	noLinksResult := flowResult{Description: "Đường E (không có hình học)", SpeedMps: 2.0, FreeFlowMps: 8.0, JamFactor: 8.5}
	if err := recordFlowScan("Quận 1", base.Add(time.Minute), "", 0, []byte("{}"), []flowResult{noLinksResult}); err != nil {
		t.Fatalf("recordFlowScan (no links): %v", err)
	}

	if _, err := computeHotspots(base, base.Add(-time.Hour), 0, 0, 0, 0); err == nil {
		t.Error("expected error for to-before-from, got nil")
	}
	if _, err := computeHotspots(base, base.AddDate(0, 0, maxFlowHistoryRangeDays+1), 0, 0, 0, 0); err == nil {
		t.Error("expected error for a range wider than maxFlowHistoryRangeDays, got nil")
	}

	report, err := computeHotspots(base.Add(-time.Hour), base.Add(time.Hour), 0, 0, 0, 0)
	if err != nil {
		t.Fatalf("computeHotspots: %v", err)
	}
	// 2 flow_segments rows total in range: Đường D (seedFlowScan always
	// writes Links) and the no-Links Đường E write above.
	if report.DataQuality.ScansExamined != 2 {
		t.Errorf("DataQuality.ScansExamined = %d, want 2", report.DataQuality.ScansExamined)
	}
	if report.DataQuality.WithGeometry != 1 {
		t.Errorf("DataQuality.WithGeometry = %d, want 1 (the no-Links write lacks first/last lat,lng)", report.DataQuality.WithGeometry)
	}
	if report.DataQuality.GeometryRate != 0.5 {
		t.Errorf("DataQuality.GeometryRate = %v, want 0.5", report.DataQuality.GeometryRate)
	}
}

func TestHotspotGroupKeyFallsBackToGridCellWhenUnnamed(t *testing.T) {
	k1 := hotspotGroupKey("Quận 1", "", 10.800123, 106.700456)
	k2 := hotspotGroupKey("Quận 1", "", 10.800234, 106.700321) // same ~100m cell after rounding
	if k1 != k2 {
		t.Errorf("expected nearby unnamed points to share a grid-cell key: %q vs %q", k1, k2)
	}
	k3 := hotspotGroupKey("Quận 1", "", 10.850000, 106.750000) // far away
	if k1 == k3 {
		t.Errorf("expected distant unnamed points to land in different grid cells, both got %q", k1)
	}
}

func TestParseLatLng(t *testing.T) {
	lat, lng, ok := parseLatLng(" 10.5 , 106.7 ")
	if !ok || lat != 10.5 || lng != 106.7 {
		t.Errorf("parseLatLng(' 10.5 , 106.7 ') = (%v, %v, %v), want (10.5, 106.7, true)", lat, lng, ok)
	}
	if _, _, ok := parseLatLng("not-a-point"); ok {
		t.Errorf("parseLatLng(\"not-a-point\") ok = true, want false")
	}
}
