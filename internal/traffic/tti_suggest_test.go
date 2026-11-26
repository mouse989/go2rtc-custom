package traffic

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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

	report, err := computeHotspots(base.Add(-time.Hour), base.Add(time.Hour), 0, 0, 0, 0, 0, nil)
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
	// AvgTTI averages tti_fused (the FUSED speed+jamFactor figure
	// recordFlowScan actually stores, not the raw speed ratio) across ALL
	// 5 scans, not just the 4 congested ones (that's CongestedCount/
	// CongestionRate's own job): 4 scans at tti_fused≈3.861 (speed 2.0,
	// jamFactor 8.5) + 1 at tti_fused≈1.911 (speed 8.0, same jamFactor) =
	// 17.355/5 = 3.471, rounded to 3.47.
	if a.AvgTTI != 3.47 {
		t.Errorf("Đường A: AvgTTI=%v, want 3.47", a.AvgTTI)
	}
	// Đường B never once reached minTTI (congestedCount stays 0, even
	// though it clears minScans) — "chọn mức ùn tối thiểu > F nhưng vẫn
	// thấy điểm E/C/D": a road that never hit the chosen threshold at
	// all must not appear as a hotspot for it, so Đường B is excluded
	// entirely here. CongestionRate itself (for a road that DID hit the
	// threshold at least once) stays purely a sort key, never an
	// exclusion one — that part of the original design is unchanged.
	if b != nil {
		t.Fatalf("expected Đường B (never congested) to be excluded entirely, got %+v", out)
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

	report, err := computeHotspots(base.Add(-time.Hour), base.Add(time.Hour), 0, 0, 0, 0, 0, nil)
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

	report, err := computeHotspots(base.Add(-time.Hour), base.Add(time.Hour), 0, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("computeHotspots: %v", err)
	}
	for _, h := range report.Hotspots {
		if h.Description == "Đường C" {
			t.Fatalf("expected Đường C (only 2 scans) to be filtered out by minScans, got %+v", h)
		}
	}
}

// TestComputeHotspotsOnlyShowsGroupsThatReachedMinTTI covers "chọn mức
// ùn tối thiểu > F tuy nhiên bảng/bản đồ vẫn còn vẽ các điểm E/C/D": a
// road whose own worst-ever TTI only ever reaches "D" must be excluded
// entirely when the caller filters at the "F" threshold — not merely
// ranked last — and a road that DOES reach "F" must show WorstLOSLabel
// "F" (never a lower band it also happened to pass through along the
// way).
func TestComputeHotspotsOnlyShowsGroupsThatReachedMinTTI(t *testing.T) {
	resetFlowCaptureDB(t)
	ensureTravelTimeInit(t)

	base := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
	// Freeflow/speed = 8.0/4.0 = TTI 2.0, squarely "D" (1.96-2.56) — never F.
	for i := 0; i < 4; i++ {
		seedFlowScan(t, "Quận 3", "Đường Chỉ Tới D", base.Add(time.Duration(i)*5*time.Minute),
			4.0, 8.0, 5.0, 10.75, 106.65, 10.76, 106.66)
	}
	// Freeflow/speed = 8.0/2.0 = TTI 4.0, "F" (>= 3.45).
	for i := 0; i < 4; i++ {
		seedFlowScan(t, "Quận 3", "Đường Tới F", base.Add(time.Duration(i)*5*time.Minute),
			2.0, 8.0, 8.5, 10.85, 106.65, 10.86, 106.66)
	}

	const losFThreshold = 3.45
	report, err := computeHotspots(base.Add(-time.Hour), base.Add(time.Hour), losFThreshold, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("computeHotspots: %v", err)
	}

	var d, f *HotspotSuggestion
	for i := range report.Hotspots {
		switch report.Hotspots[i].Description {
		case "Đường Chỉ Tới D":
			d = &report.Hotspots[i]
		case "Đường Tới F":
			f = &report.Hotspots[i]
		}
	}
	if d != nil {
		t.Errorf("expected Đường Chỉ Tới D (never reaches F) to be excluded at the F threshold, got %+v", d)
	}
	if f == nil {
		t.Fatalf("expected Đường Tới F in results, got %+v", report.Hotspots)
	}
	if f.WorstLOSLabel != "F" {
		t.Errorf("Đường Tới F: WorstLOSLabel = %q, want %q", f.WorstLOSLabel, "F")
	}
	if f.WorstTTI < losFThreshold {
		t.Errorf("Đường Tới F: WorstTTI = %v, want >= %v", f.WorstTTI, losFThreshold)
	}
	if f.WorstTTILowSpeedWarning {
		t.Errorf("Đường Tới F: WorstTTILowSpeedWarning = true, want false (speed 2.0 m/s is well above the floor)")
	}
}

// TestComputeHotspotsFlagsWorstTTIFromFlooredSpeed covers "TTi cao nhất
// hiện nay đang khá lớn: 46.00, 18.26 ... kiểm tra xem đây là số cộng
// dồn qua các lần lấy hay sao?" — WorstTTI is never cumulative (it's a
// plain per-scan max, see the aggregation loop above), but
// CalculateTTIFromSpeed (internal/trafficmetrics) floors a near-zero/zero
// speed reading to MinSpeedMps before dividing by it, so a road whose
// worst scan had an almost-stalled speed can show a very large TTI that
// reflects the floor kicking in more than a precisely measured ratio.
// WorstTTILowSpeedWarning must flag exactly that scan.
func TestComputeHotspotsFlagsWorstTTIFromFlooredSpeed(t *testing.T) {
	resetFlowCaptureDB(t)
	ensureTravelTimeInit(t)

	base := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	// speed 0.1 m/s is below trafficmetrics.MinSpeedMps (0.2) — floored,
	// so the resulting TTI is driven by freeFlow/MinSpeedMps rather than
	// the (unreliable at that point) exact measured speed.
	for i := 0; i < 3; i++ {
		seedFlowScan(t, "Quận 5", "Đường Gần Như Đứng Yên", base.Add(time.Duration(i)*5*time.Minute),
			0.1, 9.0, 9.5, 10.76, 106.68, 10.77, 106.69)
	}

	report, err := computeHotspots(base.Add(-time.Hour), base.Add(time.Hour), 0, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("computeHotspots: %v", err)
	}
	var h *HotspotSuggestion
	for i := range report.Hotspots {
		if report.Hotspots[i].Description == "Đường Gần Như Đứng Yên" {
			h = &report.Hotspots[i]
		}
	}
	if h == nil {
		t.Fatalf("expected Đường Gần Như Đứng Yên in results, got %+v", report.Hotspots)
	}
	if !h.WorstTTILowSpeedWarning {
		t.Errorf("expected WorstTTILowSpeedWarning = true for a scan with speed 0.1 m/s (below the floor), got false (WorstTTI=%v)", h.WorstTTI)
	}
}

// TestComputeHotspotsMinCongestedCountFiltersByOwnOccurrenceCount covers
// the "bổ sung thêm cho bộ lọc điểm nóng: số lần xuất hiện" request — a
// NEW raw-count filter on exactly HotspotSuggestion.CongestedCount (the
// "Số lần xuất hiện" column), distinct from both minScans (total scan
// count, unrelated to whether any of them were congested) and
// CongestionRate (a ranking-only signal, never an exclusion one — see
// computeHotspots' own doc comment). A road congested in exactly 2 of 5
// scans must be excluded once the caller asks for at least 3
// occurrences, included once the bar is at or below 2.
func TestComputeHotspotsMinCongestedCountFiltersByOwnOccurrenceCount(t *testing.T) {
	resetFlowCaptureDB(t)
	ensureTravelTimeInit(t)

	base := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		speed, freeFlow := 8.0, 8.0 // TTI 1.0 — not congested at the default 1.96 threshold
		if i < 2 {
			speed = 2.0 // TTI 4.0 — congested
		}
		seedFlowScan(t, "Quận 4", "Đường Ùn 2 Lần", base.Add(time.Duration(i)*5*time.Minute),
			speed, freeFlow, 5.0, 10.70, 106.60, 10.71, 106.61)
	}

	findIt := func(minCongestedCount int) *HotspotSuggestion {
		report, err := computeHotspots(base.Add(-time.Hour), base.Add(time.Hour), 0, 0, minCongestedCount, 0, 0, nil)
		if err != nil {
			t.Fatalf("computeHotspots(minCongestedCount=%d): %v", minCongestedCount, err)
		}
		for i := range report.Hotspots {
			if report.Hotspots[i].Description == "Đường Ùn 2 Lần" {
				return &report.Hotspots[i]
			}
		}
		return nil
	}

	if h := findIt(3); h != nil {
		t.Errorf("minCongestedCount=3: expected the road (congested only 2 times) excluded, got %+v", h)
	}
	if h := findIt(2); h == nil || h.CongestedCount != 2 {
		t.Errorf("minCongestedCount=2: expected the road included with CongestedCount=2, got %+v", h)
	}
	// 0 falls back to the minimum of 1 ("đã từng đạt ít nhất 1 lần") —
	// same floor as before this filter existed.
	if h := findIt(0); h == nil || h.CongestedCount != 2 {
		t.Errorf("minCongestedCount=0 (defaults to 1): expected the road included with CongestedCount=2, got %+v", h)
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

	if _, err := computeHotspots(base, base.Add(-time.Hour), 0, 0, 0, 0, 0, nil); err == nil {
		t.Error("expected error for to-before-from, got nil")
	}
	if _, err := computeHotspots(base, base.AddDate(0, 0, maxFlowHistoryRangeDays+1), 0, 0, 0, 0, 0, nil); err == nil {
		t.Error("expected error for a range wider than maxFlowHistoryRangeDays, got nil")
	}

	report, err := computeHotspots(base.Add(-time.Hour), base.Add(time.Hour), 0, 0, 0, 0, 0, nil)
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
	// Identical point -> identical key, whatever the radius.
	k1 := hotspotGroupKey("Quận 1", "", 10.800123, 106.700456, 100)
	k1b := hotspotGroupKey("Quận 1", "", 10.800123, 106.700456, 100)
	if k1 != k1b {
		t.Errorf("identical point must produce an identical key: %q vs %q", k1, k1b)
	}

	// Two points ~1000m apart in longitude only: floor-division
	// guarantees two values at least one cell-width apart land in
	// different buckets, so at gridRadiusM=100 (cell ~100m, far smaller
	// than the 1000m gap) these MUST differ — not a probabilistic check.
	lat, lng := 10.800000, 106.700000
	lngFar := lng + 1000.0/metersPerDegreeLng(lat) // ~1000m due east
	kNear := hotspotGroupKey("Quận 1", "", lat, lng, 100)
	kFar := hotspotGroupKey("Quận 1", "", lat, lngFar, 100)
	if kNear == kFar {
		t.Errorf("points ~1000m apart must land in different cells at gridRadiusM=100, both got %q", kNear)
	}

	// Same two points, but with a MUCH larger grid radius (100km, vs. a
	// 1000m/1% gap) — now too close together, relative to the cell, to
	// plausibly straddle a boundary, so they should collapse into one
	// bucket.
	kNearBig := hotspotGroupKey("Quận 1", "", lat, lng, 100000)
	kFarBig := hotspotGroupKey("Quận 1", "", lat, lngFar, 100000)
	if kNearBig != kFarBig {
		t.Errorf("points ~1000m apart should land in the SAME cell at gridRadiusM=100000, got %q vs %q", kNearBig, kFarBig)
	}

	// A different region never shares a grid-cell key, same point or not.
	if kOther := hotspotGroupKey("Quận 3", "", lat, lng, 100); kOther == kNear {
		t.Errorf("different region must never share a grid-cell key, both got %q", kNear)
	}
}

// TestComputeHotspotsMergesNearbyUnnamedGroupsAcrossGridBoundary covers
// "vẫn có 1 số điểm không gộp theo bán kính lọc ... rà soát lại cơ chế
// gộm tuyến thành khu vực, cụm tuyến đường": two unnamed readings whose
// own start points sit on OPPOSITE sides of one raw grid cell's hard
// boundary (only ~2m apart — well within even the smallest "Bán kính
// gộp" option) used to land in two different hotspotGroupKey cells and
// show up as two disconnected map entries, however large gridRadiusM was
// set — mergeNearbyUnnamedGroups must fold them back into exactly ONE
// hotspot, and computeHotspotTimeline must recognize rows from BOTH
// original cells under that one merged entry's own Key.
func TestComputeHotspotsMergesNearbyUnnamedGroupsAcrossGridBoundary(t *testing.T) {
	resetFlowCaptureDB(t)
	ensureTravelTimeInit(t)

	const gridRadiusM = 100.0
	cellSizeLat := gridRadiusM / metersPerDegreeLat()
	k := math.Floor(10.78 / cellSizeLat)
	boundaryLat := k * cellSizeLat
	metersToLat := func(m float64) float64 { return m / metersPerDegreeLat() }
	latBelow := boundaryLat - metersToLat(1) // falls in cell k-1
	latAbove := boundaryLat + metersToLat(1) // falls in cell k
	const lng = 106.70

	// Sanity-check the test's own premise before relying on it below.
	if hotspotGroupKey("Quận 9", "", latBelow, lng, gridRadiusM) == hotspotGroupKey("Quận 9", "", latAbove, lng, gridRadiusM) {
		t.Fatalf("test setup error: latBelow/latAbove must land in different raw grid cells")
	}
	if d := haversine(latBelow, lng, latAbove, lng); d > gridRadiusM {
		t.Fatalf("test setup error: latBelow/latAbove are %v m apart, want well within gridRadiusM=%v", d, gridRadiusM)
	}

	base := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		seedFlowScan(t, "Quận 9", "", base.Add(time.Duration(i)*5*time.Minute),
			2.0, 8.0, 8.5, latBelow, lng, latBelow+0.001, lng+0.001)
	}
	for i := 0; i < 3; i++ {
		seedFlowScan(t, "Quận 9", "", base.Add(time.Duration(i+3)*5*time.Minute),
			2.0, 8.0, 8.5, latAbove, lng, latAbove+0.001, lng+0.001)
	}

	report, err := computeHotspots(base.Add(-time.Hour), base.Add(time.Hour), 0, 0, 0, gridRadiusM, 0, nil)
	if err != nil {
		t.Fatalf("computeHotspots: %v", err)
	}
	var matches []HotspotSuggestion
	for _, h := range report.Hotspots {
		if h.RegionName == "Quận 9" {
			matches = append(matches, h)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("expected the two straddling-boundary unnamed groups to merge into exactly 1 hotspot, got %d: %+v", len(matches), matches)
	}
	if matches[0].ScanCount != 6 {
		t.Errorf("merged ScanCount = %d, want 6 (3+3 from both sides of the boundary)", matches[0].ScanCount)
	}
	if matches[0].CongestedCount != 6 {
		t.Errorf("merged CongestedCount = %d, want 6", matches[0].CongestedCount)
	}

	// The merged entry's own Key must let computeHotspotTimeline recover
	// rows from BOTH original cells, not just whichever one became the
	// canonical key.
	tl, err := computeHotspotTimeline("Quận 9", matches[0].Key, base.Add(-time.Hour), base.Add(time.Hour), 0, gridRadiusM)
	if err != nil {
		t.Fatalf("computeHotspotTimeline: %v", err)
	}
	total := 0
	for _, s := range tl.Slots {
		total += s.Count
	}
	if total != 6 {
		t.Errorf("timeline total = %d, want 6 (rows from both merged cells)", total)
	}
}

// TestComputeHotspotsMergeHelpsClearMinScans covers the merge step's own
// side benefit: two unnamed sub-groups, each individually too sparse to
// clear minScans on its own, must still qualify once merged into one
// combined candidate — the merge step runs BEFORE minScans/
// minCongestedCount filtering (see computeHotspots' own call site).
func TestComputeHotspotsMergeHelpsClearMinScans(t *testing.T) {
	resetFlowCaptureDB(t)
	ensureTravelTimeInit(t)

	const gridRadiusM = 100.0
	cellSizeLat := gridRadiusM / metersPerDegreeLat()
	k := math.Floor(10.78 / cellSizeLat)
	boundaryLat := k * cellSizeLat
	metersToLat := func(m float64) float64 { return m / metersPerDegreeLat() }
	latBelow := boundaryLat - metersToLat(1)
	latAbove := boundaryLat + metersToLat(1)
	const lng = 106.70

	base := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	// 2 scans on each side — individually below the default minScans=3,
	// but 4 combined clears it once merged.
	for i := 0; i < 2; i++ {
		seedFlowScan(t, "Quận 10", "", base.Add(time.Duration(i)*5*time.Minute),
			2.0, 8.0, 8.5, latBelow, lng, latBelow+0.001, lng+0.001)
	}
	for i := 0; i < 2; i++ {
		seedFlowScan(t, "Quận 10", "", base.Add(time.Duration(i+2)*5*time.Minute),
			2.0, 8.0, 8.5, latAbove, lng, latAbove+0.001, lng+0.001)
	}

	report, err := computeHotspots(base.Add(-time.Hour), base.Add(time.Hour), 0, 0, 0, gridRadiusM, 0, nil)
	if err != nil {
		t.Fatalf("computeHotspots: %v", err)
	}
	var found *HotspotSuggestion
	for i := range report.Hotspots {
		if report.Hotspots[i].RegionName == "Quận 10" {
			found = &report.Hotspots[i]
		}
	}
	if found == nil {
		t.Fatalf("expected the merged group (4 scans combined) to clear the default minScans=3, got none in Quận 10: %+v", report.Hotspots)
	}
	if found.ScanCount != 4 {
		t.Errorf("ScanCount = %d, want 4", found.ScanCount)
	}
}

// TestComputeHotspotsMergesNearbyNamedRoadsAcrossDifferentNames covers
// "những điểm có tên cũng phải được gộp thành nhóm (trong đó có nhiều
// đường)": two DIFFERENTLY-named roads (e.g. a junction where a provincial
// road meets a national highway) whose own start points sit only ~5m
// apart — well within gridRadiusM — must merge into ONE combined hotspot,
// not stay separate just because their exact description strings differ.
// The merged entry's own Description still names only its single
// representative road, but MergedRoadNames must list BOTH.
func TestComputeHotspotsMergesNearbyNamedRoadsAcrossDifferentNames(t *testing.T) {
	resetFlowCaptureDB(t)
	ensureTravelTimeInit(t)

	const gridRadiusM = 100.0
	lat, lng := 10.78, 106.70
	lat2 := lat + 5.0/metersPerDegreeLat() // ~5m north — well within gridRadiusM

	base := time.Date(2026, 11, 28, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		seedFlowScan(t, "Quận 11", "Đường Tỉnh 743", base.Add(time.Duration(i)*5*time.Minute),
			2.0, 8.0, 8.5, lat, lng, lat+0.001, lng+0.001)
	}
	for i := 0; i < 3; i++ {
		seedFlowScan(t, "Quận 11", "Quốc Lộ 1", base.Add(time.Duration(i+3)*5*time.Minute),
			2.0, 8.0, 8.5, lat2, lng, lat2+0.001, lng+0.001)
	}

	report, err := computeHotspots(base.Add(-time.Hour), base.Add(time.Hour), 0, 0, 0, gridRadiusM, 0, nil)
	if err != nil {
		t.Fatalf("computeHotspots: %v", err)
	}
	var matches []HotspotSuggestion
	for _, h := range report.Hotspots {
		if h.RegionName == "Quận 11" {
			matches = append(matches, h)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("expected the 2 nearby differently-named roads to merge into exactly 1 hotspot, got %d: %+v", len(matches), matches)
	}
	h := matches[0]
	if h.ScanCount != 6 {
		t.Errorf("merged ScanCount = %d, want 6", h.ScanCount)
	}
	wantNames := []string{"Đường Tỉnh 743", "Quốc Lộ 1"}
	sort.Strings(wantNames)
	if !reflect.DeepEqual(h.MergedRoadNames, wantNames) {
		t.Errorf("MergedRoadNames = %v, want %v", h.MergedRoadNames, wantNames)
	}
	if h.Description != "Đường Tỉnh 743" && h.Description != "Quốc Lộ 1" {
		t.Errorf("Description should be one of the 2 merged roads' own names, got %q", h.Description)
	}
}

func TestComputeHotspotsLimitDefaultsTo50ButHonorsAnExplicitHigherValue(t *testing.T) {
	resetFlowCaptureDB(t)
	ensureTravelTimeInit(t)

	base := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
	const roadCount = 55 // > defaultHotspotLimit (50)
	for r := 0; r < roadCount; r++ {
		name := fmt.Sprintf("Đường Test %02d", r)
		for i := 0; i < 3; i++ {
			seedFlowScan(t, "Quận 1", name, base.Add(time.Duration(i)*5*time.Minute),
				2.0, 8.0, 8.5, 10.0+float64(r)*0.01, 106.0, 10.01+float64(r)*0.01, 106.01)
		}
	}

	// limit omitted (0) -> defaultHotspotLimit (50) results, but
	// TotalMatched must still report the true count (55), not 50.
	report, err := computeHotspots(base.Add(-time.Hour), base.Add(time.Hour), 0, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("computeHotspots: %v", err)
	}
	if len(report.Hotspots) != 50 {
		t.Errorf("len(Hotspots) = %d, want 50 (defaultHotspotLimit)", len(report.Hotspots))
	}
	if report.TotalMatched != roadCount {
		t.Errorf("TotalMatched = %d, want %d", report.TotalMatched, roadCount)
	}

	// An explicit limit above the old (buggy) "reset to 50 past 200"
	// threshold must be honored, not silently reduced back to 50.
	report2, err := computeHotspots(base.Add(-time.Hour), base.Add(time.Hour), 0, 0, 0, 0, roadCount, nil)
	if err != nil {
		t.Fatalf("computeHotspots (explicit limit): %v", err)
	}
	if len(report2.Hotspots) != roadCount {
		t.Errorf("len(Hotspots) with limit=%d = %d, want %d", roadCount, len(report2.Hotspots), roadCount)
	}
	if report2.TotalMatched != roadCount {
		t.Errorf("TotalMatched = %d, want %d", report2.TotalMatched, roadCount)
	}

	// A limit above maxHotspotLimit is clamped DOWN to maxHotspotLimit,
	// not reset to defaultHotspotLimit.
	report3, err := computeHotspots(base.Add(-time.Hour), base.Add(time.Hour), 0, 0, 0, 0, maxHotspotLimit+500, nil)
	if err != nil {
		t.Fatalf("computeHotspots (over-max limit): %v", err)
	}
	if len(report3.Hotspots) != roadCount {
		t.Errorf("len(Hotspots) with an over-max limit = %d, want %d (every match, since roadCount < maxHotspotLimit)", len(report3.Hotspots), roadCount)
	}
}

// TestComputeHotspotsEnrichesGeometryFromRawJSON verifies
// enrichHotspotGeometry/findMatchingFlowResult/flattenFlowResultPoints
// against realSampleJSON (flow_capture_parse_test.go) — a real captured
// API response, not a hand-built fixture — for BOTH matching paths:
// a named road (matched by Description) and an unnamed one (matched by
// nearest first-point, since it has no description to key off of).
func TestComputeHotspotsEnrichesGeometryFromRawJSON(t *testing.T) {
	resetFlowCaptureDB(t)
	ensureTravelTimeInit(t)

	results, sourceUpdated, err := parseFlowResults([]byte(realSampleJSON))
	if err != nil {
		t.Fatalf("parseFlowResults: %v", err)
	}
	base := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ { // >= default minScans (3)
		scannedAt := base.Add(time.Duration(i) * 5 * time.Minute)
		if err := recordFlowScan("Quận 7", scannedAt, sourceUpdated, len(realSampleJSON), []byte(realSampleJSON), results); err != nil {
			t.Fatalf("recordFlowScan (%d): %v", i, err)
		}
	}

	report, err := computeHotspots(base.Add(-time.Hour), base.Add(time.Hour), 0, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("computeHotspots: %v", err)
	}

	var named, unnamed *HotspotSuggestion
	for i := range report.Hotspots {
		h := &report.Hotspots[i]
		switch h.Description {
		case "Duong Nguyen Thi Thap":
			named = h
		case "":
			unnamed = h
		}
	}
	if named == nil {
		t.Fatalf("expected a hotspot for 'Duong Nguyen Thi Thap', got %+v", report.Hotspots)
	}
	wantNamedGeom := [][2]float64{{10.73097, 106.70022}, {10.73114, 106.70019}}
	if !reflect.DeepEqual(named.Geometry, wantNamedGeom) {
		t.Errorf("named hotspot Geometry = %v, want %v", named.Geometry, wantNamedGeom)
	}

	if unnamed == nil {
		t.Fatalf("expected an unnamed (grid-cell-grouped) hotspot, got %+v", report.Hotspots)
	}
	wantUnnamedGeom := [][2]float64{
		{10.80114, 106.66217}, {10.80099, 106.66213}, {10.80087, 106.66214},
		{10.80087, 106.66214}, {10.80073, 106.66218},
	}
	if !reflect.DeepEqual(unnamed.Geometry, wantUnnamedGeom) {
		t.Errorf("unnamed hotspot Geometry = %v, want %v (both links' points flattened in order)", unnamed.Geometry, wantUnnamedGeom)
	}
}

func TestComputeHotspotTimelineBucketsByVNLocal30MinSlotAndSumsAcrossDays(t *testing.T) {
	resetFlowCaptureDB(t)

	from := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)

	// Day 1: 00:05 UTC == 07:05 VN local -> slot 14 (07:00-07:30).
	seedFlowScan(t, "Quận 1", "Đường A", time.Date(2026, 11, 20, 0, 5, 0, 0, time.UTC),
		2.0, 8.0, 8.5, 10.80, 106.70, 10.81, 106.71)
	// Day 2, same time-of-day -> same slot 14, should be summed with day 1.
	seedFlowScan(t, "Quận 1", "Đường A", time.Date(2026, 11, 21, 0, 5, 0, 0, time.UTC),
		2.0, 8.0, 8.5, 10.80, 106.70, 10.81, 106.71)
	// Day 1, 00:40 UTC == 07:40 VN local -> slot 15 (07:30-08:00), free-flowing (not congested).
	seedFlowScan(t, "Quận 1", "Đường A", time.Date(2026, 11, 20, 0, 40, 0, 0, time.UTC),
		7.5, 8.0, 2.0, 10.80, 106.70, 10.81, 106.71)
	// A different road — must not leak into Đường A's timeline.
	seedFlowScan(t, "Quận 1", "Đường B", time.Date(2026, 11, 20, 0, 5, 0, 0, time.UTC),
		2.0, 8.0, 8.5, 10.90, 106.80, 10.91, 106.81)

	key := hotspotGroupKey("Quận 1", "Đường A", 10.80, 106.70, 0)
	tl, err := computeHotspotTimeline("Quận 1", key, from, to, 0, 0)
	if err != nil {
		t.Fatalf("computeHotspotTimeline: %v", err)
	}
	if len(tl.Slots) != 48 {
		t.Fatalf("expected 48 slots, got %d", len(tl.Slots))
	}
	if tl.Slots[14].Count != 2 {
		t.Errorf("slot 14 (07:00-07:30) Count = %d, want 2 (summed across both days)", tl.Slots[14].Count)
	}
	if tl.Slots[14].Label != "07:00-07:30" {
		t.Errorf("slot 14 Label = %q, want %q", tl.Slots[14].Label, "07:00-07:30")
	}
	if tl.Slots[15].Count != 0 {
		t.Errorf("slot 15 (07:30-08:00) Count = %d, want 0 (that reading wasn't congested)", tl.Slots[15].Count)
	}
	// AvgTTI/ReadingCount track EVERY matching scan regardless of minTTI —
	// slot 14's 2 scans both have the same fused tti_fused≈3.861 (speed
	// 2.0, freeFlow 8.0, jamFactor 8.5 — tti_fused blends the raw speed
	// ratio with a jamFactor-implied TTI, see trafficmetrics.ComputeFlow,
	// so it isn't simply freeFlow/speed); slot 15's 1 scan is still
	// counted toward ReadingCount even though it never cleared minTTI
	// (Count stays 0 for it, per the check above).
	if tl.Slots[14].AvgTTI != 3.86 || tl.Slots[14].ReadingCount != 2 {
		t.Errorf("slot 14 AvgTTI/ReadingCount = %v/%d, want 3.86/2", tl.Slots[14].AvgTTI, tl.Slots[14].ReadingCount)
	}
	if tl.Slots[15].ReadingCount != 1 {
		t.Errorf("slot 15 ReadingCount = %d, want 1 (still counted toward the average even though not congested)", tl.Slots[15].ReadingCount)
	}
	total := 0
	for _, s := range tl.Slots {
		total += s.Count
	}
	if total != 2 {
		t.Errorf("total Count across all slots = %d, want 2 (Đường B's reading must not be included)", total)
	}

	// Per-day breakdown: slot 14 (07:00-07:30) should split its total of 2
	// into one occurrence each on 2026-11-20 and 2026-11-21.
	byDay := tl.Slots[14].ByDay
	if len(byDay) != 2 {
		t.Fatalf("slot 14 ByDay = %+v, want 2 entries", byDay)
	}
	if byDay[0].Date != "2026-11-20" || byDay[0].Count != 1 {
		t.Errorf("byDay[0] = %+v, want {Date: 2026-11-20, Count: 1}", byDay[0])
	}
	if byDay[1].Date != "2026-11-21" || byDay[1].Count != 1 {
		t.Errorf("byDay[1] = %+v, want {Date: 2026-11-21, Count: 1}", byDay[1])
	}
	wantWeekday0 := weekdayNamesVI[time.Date(2026, 11, 20, 0, 0, 0, 0, vnLocation).Weekday()]
	if byDay[0].Weekday != wantWeekday0 {
		t.Errorf("byDay[0].Weekday = %q, want %q", byDay[0].Weekday, wantWeekday0)
	}
	// Slot 15 was never congested, so it must carry no per-day breakdown at all.
	if tl.Slots[15].ByDay != nil {
		t.Errorf("slot 15 ByDay = %+v, want nil (never congested)", tl.Slots[15].ByDay)
	}

	if _, err := computeHotspotTimeline("Quận 1", "", from, to, 0, 0); err == nil {
		t.Error("expected error for empty key, got nil")
	}
	if _, err := computeHotspotTimeline("Quận 1", key, to, from, 0, 0); err == nil {
		t.Error("expected error for to-before-from, got nil")
	}
}

func TestComputeHotspotTimelineWorksForGridCellGroupedUnnamedHotspot(t *testing.T) {
	resetFlowCaptureDB(t)

	from := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)

	baseLat, baseLng := 10.800000, 106.700000
	// ~1m east — well under 1% of the default (100m) cell width, so this
	// lands in the same grid cell as baseLat/baseLng for any realistic
	// alignment.
	nearLng := baseLng + 1.0/metersPerDegreeLng(baseLat)
	// ~1000m east — at least one full cell width away, so floor-division
	// GUARANTEES a different cell at the default 100m radius.
	farLng := baseLng + 1000.0/metersPerDegreeLng(baseLat)

	// Two unnamed readings landing in the same grid cell — same scenario
	// computeHotspots itself groups into one "(chưa rõ tên đường)"
	// hotspot.
	seedFlowScan(t, "Quận 1", "", time.Date(2026, 11, 20, 0, 5, 0, 0, time.UTC),
		2.0, 8.0, 8.5, baseLat, baseLng, 10.81, 106.71)
	seedFlowScan(t, "Quận 1", "", time.Date(2026, 11, 20, 0, 35, 0, 0, time.UTC),
		2.0, 8.0, 8.5, baseLat, nearLng, 10.81, 106.71)
	// A reading in a clearly different grid cell, same region, also
	// unnamed — must not leak into the first cell's timeline.
	seedFlowScan(t, "Quận 1", "", time.Date(2026, 11, 20, 0, 5, 0, 0, time.UTC),
		2.0, 8.0, 8.5, baseLat, farLng, 10.96, 106.96)

	key := hotspotGroupKey("Quận 1", "", baseLat, baseLng, 0)
	tl, err := computeHotspotTimeline("Quận 1", key, from, to, 0, 0)
	if err != nil {
		t.Fatalf("computeHotspotTimeline: %v", err)
	}
	total := 0
	for _, s := range tl.Slots {
		total += s.Count
	}
	if total != 2 {
		t.Errorf("total Count = %d, want 2 (both readings in the same grid cell, the distant one excluded)", total)
	}
	if tl.Slots[14].Count != 1 || tl.Slots[15].Count != 1 {
		t.Errorf("expected one reading each in slot 14 (07:00-07:30) and slot 15 (07:30-08:00), got %+v", tl.Slots[14:16])
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
