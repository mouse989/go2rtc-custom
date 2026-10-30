package trafficinsight

import (
	"testing"

	"github.com/AlexxIT/go2rtc/internal/traveltime"
)

func testTiers() []traveltime.TTITier {
	return []traveltime.TTITier{
		{MaxTTI: 1.2, Color: "#22c55e", Label: "Thông thoáng"},
		{MaxTTI: 1.35, Color: "#84cc16", Label: "Bình thường"},
		{MaxTTI: 1.5, Color: "#f59e0b", Label: "Chậm"},
		{MaxTTI: 0, Color: "#ef4444", Label: "Tắc nghẽn"},
	}
}

func TestTierIndex(t *testing.T) {
	tiers := testTiers()
	cases := []struct {
		v    float64
		want int
	}{
		{0, -1}, {-1, -1},
		{1.0, 0}, {1.19, 0},
		{1.2, 1}, {1.34, 1},
		{1.35, 2}, {1.49, 2},
		{1.5, 3}, {5.0, 3},
	}
	for _, c := range cases {
		if got := tierIndex(c.v, tiers); got != c.want {
			t.Errorf("tierIndex(%v) = %d, want %d", c.v, got, c.want)
		}
	}
}

func TestTierIndexNoTiers(t *testing.T) {
	if got := tierIndex(5.0, nil); got != -1 {
		t.Errorf("tierIndex with no tiers = %d, want -1", got)
	}
}

func TestClassifyForecastsDegradeNearAndJamFar(t *testing.T) {
	tiers := testTiers()
	curTier := map[string]int{"r1": 0, "r2": 2} // r1 Thông thoáng, r2 Chậm
	forecasts := []traveltime.RouteForecast{
		{RouteID: "r1", Points: []traveltime.ForecastPoint{
			{MinutesAhead: 10, TTI: 1.4}, // tier 2 (Chậm) > current 0 → degrade
			{MinutesAhead: 60, TTI: 1.6}, // tier 3 (Tắc nghẽn) → jam
		}},
		{RouteID: "r2", Points: []traveltime.ForecastPoint{
			{MinutesAhead: 10, TTI: 1.3}, // tier 1 (Bình thường) < current 2 → not a degrade
			{MinutesAhead: 60, TTI: 1.1}, // tier 0 → not jam
		}},
		{RouteID: "unknown", Points: []traveltime.ForecastPoint{{MinutesAhead: 10, TTI: 5.0}}}, // no current tier → ignored
	}
	degradeNear, worstNearIdx, jamFar := classifyForecasts(forecasts, curTier, tiers)
	if degradeNear != 1 {
		t.Errorf("degradeNear = %d, want 1", degradeNear)
	}
	if worstNearIdx != 2 {
		t.Errorf("worstNearIdx = %d, want 2 (Chậm)", worstNearIdx)
	}
	if jamFar != 1 {
		t.Errorf("jamFar = %d, want 1", jamFar)
	}
}

func TestClassifyForecastsAlreadyWorstTierNeverDegrades(t *testing.T) {
	tiers := testTiers()
	curTier := map[string]int{"r1": 3} // already Tắc nghẽn
	forecasts := []traveltime.RouteForecast{
		{RouteID: "r1", Points: []traveltime.ForecastPoint{
			{MinutesAhead: 10, TTI: 1.6}, // still tier 3, not WORSE than current → no degrade
			{MinutesAhead: 60, TTI: 1.6}, // tier 3 == worst → jam
		}},
	}
	degradeNear, _, jamFar := classifyForecasts(forecasts, curTier, tiers)
	if degradeNear != 0 {
		t.Errorf("degradeNear = %d, want 0", degradeNear)
	}
	if jamFar != 1 {
		t.Errorf("jamFar = %d, want 1", jamFar)
	}
}

func TestClassifyForecastsMissingPointsAreIgnored(t *testing.T) {
	tiers := testTiers()
	curTier := map[string]int{"r1": 0}
	forecasts := []traveltime.RouteForecast{
		{RouteID: "r1", Points: nil}, // no forecast data at all (Method "none")
	}
	degradeNear, worstNearIdx, jamFar := classifyForecasts(forecasts, curTier, tiers)
	if degradeNear != 0 || jamFar != 0 {
		t.Errorf("expected no degrade/jam with no points, got degradeNear=%d jamFar=%d", degradeNear, jamFar)
	}
	if worstNearIdx != -1 {
		t.Errorf("worstNearIdx = %d, want -1", worstNearIdx)
	}
}

// TestGetNetworkStatusTierCountsMatchLatestSnapshot guards the tier-count
// pass-through against traveltime.LatestSnapshot()/GetAppearance() without
// writing any "today"-dated fixture itself — traveltime.LatestSnapshot()
// always reads the real wall-clock "today" (internal/traveltime/logs.go's
// getLogs), so seeding that date directly here would collide with whatever
// other "today" state the shared test fixture directory already holds
// (same reason TestGetTodaySnapshotExposesCurrentTTIFromLatestSnapshot in
// insight_test.go doesn't seed "today" either). Instead this recomputes the
// same tier-count classification independently and checks GetNetworkStatus
// agrees, whatever the current state is.
func TestGetNetworkStatusTierCountsMatchLatestSnapshot(t *testing.T) {
	setupSources(t)

	ns := GetNetworkStatus()
	appearance := traveltime.GetAppearance()
	snap := traveltime.LatestSnapshot()

	wantCounts := make([]int, len(appearance.Tiers))
	wantNoData := 0
	for _, rs := range snap.Routes {
		idx := tierIndex(rs.LatestTTI, appearance.Tiers)
		if idx < 0 {
			wantNoData++
			continue
		}
		wantCounts[idx]++
	}

	if len(ns.Tiers) != len(appearance.Tiers) {
		t.Fatalf("got %d tiers, want %d", len(ns.Tiers), len(appearance.Tiers))
	}
	for i, tier := range ns.Tiers {
		if tier.Count != wantCounts[i] {
			t.Errorf("tier %q count = %d, want %d", tier.Label, tier.Count, wantCounts[i])
		}
		if tier.Label != appearance.Tiers[i].Label || tier.Color != appearance.Tiers[i].Color {
			t.Errorf("tier %d label/color = %q/%q, want %q/%q", i, tier.Label, tier.Color, appearance.Tiers[i].Label, appearance.Tiers[i].Color)
		}
	}
	if ns.NoDataCount != wantNoData {
		t.Errorf("NoDataCount = %d, want %d", ns.NoDataCount, wantNoData)
	}
	if ns.NoDataColor != appearance.NoDataColor {
		t.Errorf("NoDataColor = %q, want %q", ns.NoDataColor, appearance.NoDataColor)
	}
}
