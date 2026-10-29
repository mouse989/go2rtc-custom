package trafficinsight

import (
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/internal/traveltime"
)

func TestRouteFilterEmpty(t *testing.T) {
	if !(RouteFilter{}).Empty() {
		t.Error("expected zero-value RouteFilter to be Empty")
	}
	if (RouteFilter{Region: "Quận 1"}).Empty() {
		t.Error("expected a Region-only filter to not be Empty")
	}
	if (RouteFilter{RoadType: "Quốc lộ"}).Empty() {
		t.Error("expected a RoadType-only filter to not be Empty")
	}
}

func TestRouteFilterMatches(t *testing.T) {
	lookup := routeTagLookup{
		"r1": {ID: "r1", Region: "Quận 1", RoadType: "Quốc lộ"},
		"r2": {ID: "r2", Region: "Quận 3", RoadType: "Đường đô thị"},
		"r3": {ID: "r3"}, // chưa phân loại
	}

	cases := []struct {
		name    string
		filter  RouteFilter
		routeID string
		want    bool
	}{
		{"empty filter matches tagged route", RouteFilter{}, "r1", true},
		{"empty filter matches untagged route", RouteFilter{}, "r3", true},
		{"empty filter matches unknown route", RouteFilter{}, "nope", true},
		{"region match", RouteFilter{Region: "Quận 1"}, "r1", true},
		{"region mismatch", RouteFilter{Region: "Quận 1"}, "r2", false},
		{"road type match", RouteFilter{RoadType: "Đường đô thị"}, "r2", true},
		{"road type mismatch", RouteFilter{RoadType: "Đường đô thị"}, "r1", false},
		{"both must match", RouteFilter{Region: "Quận 1", RoadType: "Quốc lộ"}, "r1", true},
		{"one of two mismatches", RouteFilter{Region: "Quận 1", RoadType: "Đường đô thị"}, "r1", false},
		{"unclassified route never matches a non-empty filter", RouteFilter{Region: "Quận 1"}, "r3", false},
		{"unknown route never matches a non-empty filter", RouteFilter{Region: "Quận 1"}, "nope", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.filter.matches(c.routeID, lookup); got != c.want {
				t.Errorf("matches(%q) = %v, want %v", c.routeID, got, c.want)
			}
		})
	}
}

// createTestRoute creates a route in the real traveltime store (via its
// exported CreateRoute) so buildRouteTagLookup (traveltime.ListRoutes())
// actually sees it, and returns its generated ID — the fixture's
// LogEntry.RouteID must use this ID, not a hand-picked one, since
// createRoute always assigns its own.
func createTestRoute(t *testing.T, name, region, roadType string) string {
	t.Helper()
	setupSources(t)
	r, err := traveltime.CreateRoute(&traveltime.Route{
		Name: name, Origin: "10,106", Destination: "11,107", Region: region, RoadType: roadType,
	})
	if err != nil {
		t.Fatalf("traveltime.CreateRoute: %v", err)
	}
	return r.ID
}

func TestGetDailyMetricsFiltersTTIByRegion(t *testing.T) {
	setupSources(t)
	if err := traveltime.SetRegions([]string{"Quận 1", "Quận 3"}); err != nil {
		t.Fatalf("SetRegions: %v", err)
	}
	r1 := createTestRoute(t, "R1", "Quận 1", "")
	r2 := createTestRoute(t, "R2", "Quận 3", "")

	date := "2027-07-01"
	writeTravelTimeDay(t, date, []traveltime.LogEntry{
		{Timestamp: date + "T09:00:00+07:00", RouteID: r1, TTI: 2.0, DelaySec: 120},
		{Timestamp: date + "T09:10:00+07:00", RouteID: r2, TTI: 4.0, DelaySec: 240},
	})

	unfiltered, err := GetDailyMetrics(date, RouteFilter{})
	if err != nil {
		t.Fatalf("GetDailyMetrics: %v", err)
	}
	wantUnfiltered := (2.0 + 4.0) / 2.0
	if diff := unfiltered.TTIAvg - wantUnfiltered; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("unfiltered TTIAvg = %v, want %v", unfiltered.TTIAvg, wantUnfiltered)
	}

	q1, err := GetDailyMetrics(date, RouteFilter{Region: "Quận 1"})
	if err != nil {
		t.Fatalf("GetDailyMetrics (Quận 1): %v", err)
	}
	if !q1.HasTTIData || q1.TTISampleCount != 1 || q1.TTIAvg != 2.0 {
		t.Errorf("Quận 1-filtered metrics = %+v, want 1 sample of 2.0", q1)
	}

	q3, err := GetDailyMetrics(date, RouteFilter{Region: "Quận 3"})
	if err != nil {
		t.Fatalf("GetDailyMetrics (Quận 3): %v", err)
	}
	if !q3.HasTTIData || q3.TTISampleCount != 1 || q3.TTIAvg != 4.0 {
		t.Errorf("Quận 3-filtered metrics = %+v, want 1 sample of 4.0", q3)
	}

	none, err := GetDailyMetrics(date, RouteFilter{Region: "Không tồn tại"})
	if err != nil {
		t.Fatalf("GetDailyMetrics (no match): %v", err)
	}
	if none.HasTTIData {
		t.Errorf("expected no TTI data for a region with no matching route, got %+v", none)
	}
}

func TestGetDailyMetricsFilterNeverAffectsJamOrIncidentData(t *testing.T) {
	setupSources(t)
	r1 := createTestRoute(t, "R1", "Quận 1", "")

	date := "2027-07-02"
	writeTrafficDay(t, date, []fixtureScan{
		scanAtVN(2027, 7, 2, 8, 0, []fixturePersistentPoint{{Lat: 1, Lng: 1, JamFactor: 5}}),
	})
	writeTravelTimeDay(t, date, []traveltime.LogEntry{
		{Timestamp: date + "T09:00:00+07:00", RouteID: r1, TTI: 2.0, DelaySec: 120},
	})
	createIncident(t, time.Date(2027, 7, 2, 10, 0, 0, 0, vnLocation), "Va chạm")

	// A filter matching NO route must still show jam/incident data
	// unchanged — those sources have nothing to do with routes at all.
	filtered, err := GetDailyMetrics(date, RouteFilter{Region: "Không tồn tại"})
	if err != nil {
		t.Fatalf("GetDailyMetrics: %v", err)
	}
	if !filtered.HasJamData || filtered.JamPointCount != 1 {
		t.Errorf("expected jam data unaffected by route filter, got %+v", filtered)
	}
	if filtered.IncidentCount != 1 {
		t.Errorf("expected incident data unaffected by route filter, got %+v", filtered)
	}
	if filtered.HasTTIData {
		t.Errorf("expected TTI data to be filtered out, got %+v", filtered)
	}
}

func TestGetHourlySeriesFiltersByRoadType(t *testing.T) {
	setupSources(t)
	r1 := createTestRoute(t, "R1", "", "Quốc lộ")
	r2 := createTestRoute(t, "R2", "", "Đường đô thị")

	date := "2027-07-03"
	writeTravelTimeDay(t, date, []traveltime.LogEntry{
		{Timestamp: date + "T08:00:00+07:00", RouteID: r1, TTI: 3.0, DelaySec: 180},
		{Timestamp: date + "T08:15:00+07:00", RouteID: r2, TTI: 1.5, DelaySec: 60},
	})

	series, err := GetHourlySeries(date, RouteFilter{RoadType: "Quốc lộ"})
	if err != nil {
		t.Fatalf("GetHourlySeries: %v", err)
	}
	hour8 := series[8]
	if !hour8.HasTTIData || hour8.TTISampleCount != 1 || hour8.TTIAvg != 3.0 {
		t.Errorf("hour 8 (Quốc lộ filter) = %+v, want 1 sample of 3.0", hour8)
	}
}

func TestGetRouteTTIForDateFiltersOutNonMatchingRoutes(t *testing.T) {
	setupSources(t)
	r1 := createTestRoute(t, "R1", "Quận 1", "")
	r2 := createTestRoute(t, "R2", "Quận 3", "")

	date := "2027-07-04"
	writeTravelTimeDay(t, date, []traveltime.LogEntry{
		{Timestamp: date + "T09:00:00+07:00", RouteID: r1, TTI: 2.0, DelaySec: 120},
		{Timestamp: date + "T09:00:00+07:00", RouteID: r2, TTI: 4.0, DelaySec: 240},
	})

	rows, err := GetRouteTTIForDate(date, -1, RouteFilter{Region: "Quận 1"})
	if err != nil {
		t.Fatalf("GetRouteTTIForDate: %v", err)
	}
	if len(rows) != 1 || rows[0].RouteID != r1 {
		t.Fatalf("expected only r1's row with Quận 1 filter, got %+v", rows)
	}
}
