package traveltime

import (
	"path/filepath"
	"testing"
)

// resetForecastState replicates Init()'s own sequence (minus HTTP handler
// registration, which panics on a second call) against a fresh temp dir, so
// Forecast()'s dependencies — routes, logs, holiday store — are all
// initialized the same way a real server would, instead of relying on
// package-level state left over from another test file.
func resetForecastState(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cfg = defaultConfig()
	initRoutes(filepath.Join(dir, "travel_routes.json"))
	initRegions(filepath.Join(dir, "travel_route_regions.json"))
	initLogs(filepath.Join(dir, "travel_logs"))
	initHolidayStore(filepath.Join(dir, "holiday_overrides.json"))
}

func TestForecastReturnsOneEntryPerRoute(t *testing.T) {
	resetForecastState(t)
	r, err := CreateRoute(&Route{Name: "R1", Origin: "10,106", Destination: "11,107"})
	if err != nil {
		t.Fatalf("CreateRoute: %v", err)
	}

	out := Forecast(10, 60)
	if len(out) != 1 || out[0].RouteID != r.ID {
		t.Fatalf("Forecast(10, 60) = %+v, want exactly one entry for route %q", out, r.ID)
	}
	// No logs were ever written for this route, so there isn't enough data
	// to fit any model — Method must say so, not silently fabricate points.
	if out[0].Method != "none" {
		t.Errorf("Method = %q, want \"none\" with no log data at all", out[0].Method)
	}
}

func TestForecastEmptyWithNoRoutes(t *testing.T) {
	resetForecastState(t)
	out := Forecast(10, 60)
	if len(out) != 0 {
		t.Fatalf("Forecast with no routes = %+v, want empty", out)
	}
}

func TestGetAppearanceReturnsConfiguredTiers(t *testing.T) {
	resetForecastState(t)
	a := GetAppearance()
	if len(a.Tiers) == 0 {
		t.Fatal("expected at least one tier from the default config")
	}
	if a.Tiers[len(a.Tiers)-1].MaxTTI != 0 {
		t.Errorf("last tier MaxTTI = %v, want 0 (catch-all)", a.Tiers[len(a.Tiers)-1].MaxTTI)
	}
}
