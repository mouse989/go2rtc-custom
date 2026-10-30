package trafficinsight

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/incidents"
	"github.com/AlexxIT/go2rtc/internal/traffic"
	"github.com/AlexxIT/go2rtc/internal/traveltime"
)

// setupOnce runs the three source packages' real Init() exactly once per
// test binary (each registers HTTP handlers on the default mux, which
// panics on a second call) against one shared temp directory. Every test
// below plants its fixtures on distinct dates so they never collide
// despite sharing that one directory.
var (
	setupOnce sync.Once
	testDir   string
)

func setupSources(t *testing.T) string {
	t.Helper()
	setupOnce.Do(func() {
		// A fresh, randomized directory every run — unlike t.TempDir()
		// (cleaned up per-test, but Init() can only run once for the whole
		// binary), a fixed path here would leak incidents/traffic/travel
		// fixtures from one `go test` invocation into the next's, double
		// counting anything this file re-creates on the same dates.
		dir, err := os.MkdirTemp("", "trafficinsight-test-")
		if err != nil {
			t.Fatalf("mkdir temp test dir: %v", err)
		}
		testDir = dir
		app.ConfigPath = filepath.Join(testDir, "go2rtc.yaml")
		traffic.Init()
		traveltime.Init()
		incidents.Init()
	})
	return testDir
}

// ── traffic fixture (daily history file) ────────────────────────────────

type fixturePersistentPoint struct {
	Lat       float64 `json:"lat"`
	Lng       float64 `json:"lng"`
	JamFactor float64 `json:"jamFactor"`
	Label     string  `json:"label,omitempty"`
	Area      string  `json:"area,omitempty"`
}
type fixtureScan struct {
	ScannedAt  string                   `json:"scannedAt"`
	Raw        []any                    `json:"raw"`
	Filtered   []any                    `json:"filtered"`
	Persistent []fixturePersistentPoint `json:"persistent"`
}
type fixtureDailyRecord struct {
	Date  string        `json:"date"`
	Scans []fixtureScan `json:"scans"`
}

// writeTrafficDay writes a traffic_data/<date>.json file directly in the
// on-disk shape internal/traffic/storage.go documents (and
// persistent_export.go already depends on externally) — the only way to
// seed traffic fixtures from outside that package, since saveScanData is
// unexported.
func writeTrafficDay(t *testing.T, date string, scans []fixtureScan) {
	t.Helper()
	dir := filepath.Join(setupSources(t), "traffic_data")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir traffic_data: %v", err)
	}
	rec := fixtureDailyRecord{Date: date, Scans: scans}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal traffic fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, date+".json"), data, 0644); err != nil {
		t.Fatalf("write traffic fixture: %v", err)
	}
}

// scanAtVN builds a scan whose ScannedAt is the given Vietnam-local
// wall-clock time, converted to UTC/RFC3339 the way the real collector
// stores it.
func scanAtVN(y, mo, d, h, mi int, points []fixturePersistentPoint) fixtureScan {
	t := time.Date(y, time.Month(mo), d, h, mi, 0, 0, vnLocation)
	return fixtureScan{ScannedAt: t.UTC().Format(time.RFC3339), Persistent: points}
}

// ── traveltime fixture (JSONL logs) ─────────────────────────────────────

func writeTravelTimeDay(t *testing.T, date string, entries []traveltime.LogEntry) {
	t.Helper()
	dir := filepath.Join(setupSources(t), "travel_logs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir travel_logs: %v", err)
	}
	f, err := os.Create(filepath.Join(dir, date+".jsonl"))
	if err != nil {
		t.Fatalf("create travel log file: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			t.Fatalf("encode travel log entry: %v", err)
		}
	}
}

// ── incidents fixture ────────────────────────────────────────────────────

func createIncident(t *testing.T, when time.Time, category string) {
	t.Helper()
	setupSources(t)
	in := &incidents.Incident{
		Time:     when,
		Category: category,
		Content:  "test incident",
		Unit:     "TEST UNIT",
	}
	if err := incidents.Create(in, "tester"); err != nil {
		t.Fatalf("incidents.Create: %v", err)
	}
}

// ── Tests ─────────────────────────────────────────────────────────────────

func TestGetDailyMetricsCombinesAllThreeSources(t *testing.T) {
	setupSources(t)
	date := "2027-03-15"

	writeTrafficDay(t, date, []fixtureScan{
		scanAtVN(2027, 3, 15, 8, 0, []fixturePersistentPoint{
			{Lat: 10.1, Lng: 106.1, JamFactor: 6},
			{Lat: 10.2, Lng: 106.2, JamFactor: 8},
		}),
		scanAtVN(2027, 3, 15, 12, 0, []fixturePersistentPoint{
			{Lat: 10.1, Lng: 106.1, JamFactor: 4},
		}),
	})

	writeTravelTimeDay(t, date, []traveltime.LogEntry{
		{Timestamp: date + "T09:00:00+07:00", RouteID: "r1", TTI: 1.5, DelaySec: 180},
		{Timestamp: date + "T15:00:00+07:00", RouteID: "r1", TTI: 2.5, DelaySec: 300},
	})

	createIncident(t, time.Date(2027, 3, 15, 10, 0, 0, 0, vnLocation), "Va chạm")
	createIncident(t, time.Date(2027, 3, 15, 11, 0, 0, 0, vnLocation), "Va chạm")
	createIncident(t, time.Date(2027, 3, 15, 20, 0, 0, 0, vnLocation), "Đông xe")

	m, err := GetDailyMetrics(date, RouteFilter{})
	if err != nil {
		t.Fatalf("GetDailyMetrics: %v", err)
	}

	if !m.HasJamData || m.JamPointCount != 3 {
		t.Fatalf("expected 3 jam points with data, got hasData=%v count=%d", m.HasJamData, m.JamPointCount)
	}
	wantJamAvg := (6.0 + 8.0 + 4.0) / 3.0
	if diff := m.JamAvgFactor - wantJamAvg; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("JamAvgFactor = %v, want %v", m.JamAvgFactor, wantJamAvg)
	}
	if m.JamScanCount != 2 {
		t.Errorf("JamScanCount = %d, want 2", m.JamScanCount)
	}

	if !m.HasTTIData || m.TTISampleCount != 2 {
		t.Fatalf("expected 2 TTI samples, got hasData=%v count=%d", m.HasTTIData, m.TTISampleCount)
	}
	if diff := m.TTIAvg - 2.0; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("TTIAvg = %v, want 2.0", m.TTIAvg)
	}
	wantDelayMin := (180.0 + 300.0) / 2.0 / 60.0 // 4 minutes
	if diff := m.TTIAvgDelayMin - wantDelayMin; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("TTIAvgDelayMin = %v, want %v", m.TTIAvgDelayMin, wantDelayMin)
	}

	wantWeekday := weekdayAbbr(time.Date(2027, 3, 15, 0, 0, 0, 0, vnLocation))
	if m.Weekday != wantWeekday {
		t.Errorf("Weekday = %q, want %q", m.Weekday, wantWeekday)
	}

	if m.IncidentCount != 3 {
		t.Fatalf("IncidentCount = %d, want 3", m.IncidentCount)
	}
	if m.IncidentsByCategory["Va chạm"] != 2 || m.IncidentsByCategory["Đông xe"] != 1 {
		t.Errorf("IncidentsByCategory = %+v, want {Va chạm:2, Đông xe:1}", m.IncidentsByCategory)
	}
}

// TestGetDailyMetricsExcludesNonPositiveTTIFromAverage guards the fix for
// the "TTI trung bình hôm nay" KPI (trafficinsight.html) reading noticeably
// lower than dashboard.html's own "TTI TB" (traveltime.LatestSnapshot) for
// no real-traffic reason: a TTI<=0 entry means the scan found no usable
// route data that interval, not a real "free flow" reading — the same
// convention route_tti.go and traveltime's export.go/forecast.go already
// apply — so it must not be averaged in as if it were real data.
func TestGetDailyMetricsExcludesNonPositiveTTIFromAverage(t *testing.T) {
	date := "2027-04-01"
	setupSources(t)

	writeTravelTimeDay(t, date, []traveltime.LogEntry{
		{Timestamp: date + "T09:00:00+07:00", RouteID: "r1", TTI: 2.0, DelaySec: 240},
		{Timestamp: date + "T09:10:00+07:00", RouteID: "r1", TTI: 0, DelaySec: 0},     // no data that scan — must be excluded
		{Timestamp: date + "T09:20:00+07:00", RouteID: "r2", TTI: -1, DelaySec: 0},    // invalid — must be excluded
		{Timestamp: date + "T09:30:00+07:00", RouteID: "", TTI: 4.0, DelaySec: 300},   // missing route id — must be excluded
		{Timestamp: date + "T09:40:00+07:00", RouteID: "r1", TTI: 4.0, DelaySec: 360},
	})

	m, err := GetDailyMetrics(date, RouteFilter{})
	if err != nil {
		t.Fatalf("GetDailyMetrics: %v", err)
	}

	if !m.HasTTIData || m.TTISampleCount != 2 {
		t.Fatalf("expected only the 2 valid TTI samples counted, got hasData=%v count=%d", m.HasTTIData, m.TTISampleCount)
	}
	wantTTIAvg := (2.0 + 4.0) / 2.0
	if diff := m.TTIAvg - wantTTIAvg; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("TTIAvg = %v, want %v (0/negative/no-route-id entries must not drag it down)", m.TTIAvg, wantTTIAvg)
	}
	wantDelayMin := (240.0 + 360.0) / 2.0 / 60.0
	if diff := m.TTIAvgDelayMin - wantDelayMin; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("TTIAvgDelayMin = %v, want %v", m.TTIAvgDelayMin, wantDelayMin)
	}
}

func TestWeekdayAbbrAgainstKnownCalendarDates(t *testing.T) {
	cases := []struct {
		date string
		want string
	}{
		{"2024-01-01", "T2"}, // real-world Monday
		{"2024-01-02", "T3"},
		{"2024-01-06", "T7"},
		{"2024-01-07", "CN"}, // real-world Sunday
	}
	for _, c := range cases {
		d, err := time.ParseInLocation("2006-01-02", c.date, vnLocation)
		if err != nil {
			t.Fatalf("parse %q: %v", c.date, err)
		}
		if got := weekdayAbbr(d); got != c.want {
			t.Errorf("weekdayAbbr(%s) = %q, want %q", c.date, got, c.want)
		}
	}
}

func TestPersistentPointsForDateKeepsLabelAndArea(t *testing.T) {
	setupSources(t)
	date := "2027-05-10"
	writeTrafficDay(t, date, []fixtureScan{
		{
			ScannedAt: time.Date(2027, 5, 10, 8, 0, 0, 0, vnLocation).UTC().Format(time.RFC3339),
			Persistent: []fixturePersistentPoint{
				{Lat: 10.5, Lng: 106.5, JamFactor: 9, Label: "Ngã tư Hàng Xanh", Area: "Bình Thạnh"},
			},
		},
	})

	points, err := traffic.PersistentPointsForDate(date)
	if err != nil {
		t.Fatalf("PersistentPointsForDate: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("expected 1 point, got %d: %+v", len(points), points)
	}
	p := points[0]
	if p.Lat != 10.5 || p.Lng != 106.5 || p.JamFactor != 9 {
		t.Errorf("unexpected point coords/jamFactor: %+v", p)
	}
	if p.Label != "Ngã tư Hàng Xanh" || p.Area != "Bình Thạnh" {
		t.Errorf("expected Label/Area to be preserved (unlike the CSV export's 4-column shape), got %+v", p)
	}
}

func TestPersistentPointsForDateEmptyWhenNoFile(t *testing.T) {
	setupSources(t)
	points, err := traffic.PersistentPointsForDate("2027-05-11")
	if err != nil {
		t.Fatalf("PersistentPointsForDate: %v", err)
	}
	if len(points) != 0 {
		t.Fatalf("expected no points, got %+v", points)
	}
}

func TestGetDailyMetricsEmptyDayHasNoData(t *testing.T) {
	setupSources(t)
	m, err := GetDailyMetrics("2027-03-16", RouteFilter{})
	if err != nil {
		t.Fatalf("GetDailyMetrics: %v", err)
	}
	if m.HasJamData || m.HasTTIData || m.IncidentCount != 0 {
		t.Fatalf("expected an empty day to have no data, got %+v", m)
	}
}

func TestGetRangeAverageIgnoresDaysWithoutData(t *testing.T) {
	setupSources(t)
	// Day 1: jam data present, avg 4.
	writeTrafficDay(t, "2027-04-01", []fixtureScan{
		scanAtVN(2027, 4, 1, 8, 0, []fixturePersistentPoint{{Lat: 1, Lng: 1, JamFactor: 4}}),
	})
	// Day 2: no jam data at all (not written) — must not drag the average
	// toward zero.
	// Day 3: jam data present, avg 8.
	writeTrafficDay(t, "2027-04-03", []fixtureScan{
		scanAtVN(2027, 4, 3, 8, 0, []fixturePersistentPoint{{Lat: 1, Lng: 1, JamFactor: 8}}),
	})

	from := time.Date(2027, 4, 1, 0, 0, 0, 0, vnLocation)
	to := time.Date(2027, 4, 3, 0, 0, 0, 0, vnLocation)
	agg, err := GetRangeAverage(from, to, RouteFilter{})
	if err != nil {
		t.Fatalf("GetRangeAverage: %v", err)
	}
	if agg.DayCount != 3 {
		t.Errorf("DayCount = %d, want 3", agg.DayCount)
	}
	if agg.JamDaysWithData != 2 {
		t.Errorf("JamDaysWithData = %d, want 2", agg.JamDaysWithData)
	}
	wantAvg := (4.0 + 8.0) / 2.0
	if diff := agg.JamAvgFactor - wantAvg; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("JamAvgFactor = %v, want %v (not diluted by the data-less day)", agg.JamAvgFactor, wantAvg)
	}
}

// TestGetDailyMetricsUntilTruncatesByWallClockTime is the core guard for
// the "so sánh: hôm nay — TB 7 ngày — TB 30 ngày" fairness fix: comparing
// a still-in-progress "today" against historical FULL days is biased, so
// each historical day must be truncated to the same wall-clock cutoff.
func TestGetDailyMetricsUntilTruncatesByWallClockTime(t *testing.T) {
	setupSources(t)
	date := "2027-03-16"

	writeTrafficDay(t, date, []fixtureScan{
		scanAtVN(2027, 3, 16, 8, 0, []fixturePersistentPoint{
			{Lat: 10.1, Lng: 106.1, JamFactor: 6},
			{Lat: 10.2, Lng: 106.2, JamFactor: 8},
		}),
		scanAtVN(2027, 3, 16, 14, 0, []fixturePersistentPoint{ // after cutoff — excluded
			{Lat: 10.1, Lng: 106.1, JamFactor: 2},
		}),
	})
	writeTravelTimeDay(t, date, []traveltime.LogEntry{
		{Timestamp: date + "T09:00:00+07:00", RouteID: "r1", TTI: 1.5, DelaySec: 180},
		{Timestamp: date + "T15:00:00+07:00", RouteID: "r1", TTI: 9.0, DelaySec: 600}, // after cutoff — excluded
	})
	createIncident(t, time.Date(2027, 3, 16, 10, 0, 0, 0, vnLocation), "Va chạm")
	createIncident(t, time.Date(2027, 3, 16, 11, 0, 0, 0, vnLocation), "Va chạm")
	createIncident(t, time.Date(2027, 3, 16, 20, 0, 0, 0, vnLocation), "Đông xe") // after cutoff — excluded

	// Cutoff's own calendar date (1999-01-01, nowhere near `date`) must be
	// ignored — only its 12:00:00 wall-clock time matters, applied to `date`.
	cutoff := time.Date(1999, 1, 1, 12, 0, 0, 0, vnLocation)

	m, err := GetDailyMetricsUntil(date, cutoff, RouteFilter{})
	if err != nil {
		t.Fatalf("GetDailyMetricsUntil: %v", err)
	}

	if !m.HasJamData || m.JamPointCount != 2 {
		t.Fatalf("expected only the pre-cutoff scan's 2 points, got hasData=%v count=%d", m.HasJamData, m.JamPointCount)
	}
	if diff := m.JamAvgFactor - 7.0; diff > 1e-9 || diff < -1e-9 { // (6+8)/2, the 14:00 scan's JF 2 excluded
		t.Errorf("JamAvgFactor = %v, want 7.0 (14:00 scan must be excluded)", m.JamAvgFactor)
	}
	if !m.HasTTIData || m.TTISampleCount != 1 || m.TTIAvg != 1.5 {
		t.Errorf("expected only the 09:00 TTI entry (avg 1.5), got hasData=%v count=%d avg=%v", m.HasTTIData, m.TTISampleCount, m.TTIAvg)
	}
	if m.IncidentCount != 2 {
		t.Errorf("IncidentCount = %d, want 2 (20:00 incident must be excluded)", m.IncidentCount)
	}
}

func TestGetDailyMetricsUntilEmptyBeforeCutoffHasNoData(t *testing.T) {
	setupSources(t)
	date := "2027-03-17"
	// Everything happens AFTER the cutoff — nothing should count.
	writeTrafficDay(t, date, []fixtureScan{
		scanAtVN(2027, 3, 17, 14, 0, []fixturePersistentPoint{{Lat: 1, Lng: 1, JamFactor: 5}}),
	})
	cutoff := time.Date(2027, 3, 17, 6, 0, 0, 0, vnLocation)

	m, err := GetDailyMetricsUntil(date, cutoff, RouteFilter{})
	if err != nil {
		t.Fatalf("GetDailyMetricsUntil: %v", err)
	}
	if m.HasJamData {
		t.Errorf("expected no jam data before a 06:00 cutoff when the only scan is at 14:00, got %+v", m)
	}
}

func TestGetRangeAverageUntilUsesOnlyPreCutoffDataPerDay(t *testing.T) {
	setupSources(t)
	writeTrafficDay(t, "2027-05-01", []fixtureScan{
		scanAtVN(2027, 5, 1, 8, 0, []fixturePersistentPoint{{Lat: 1, Lng: 1, JamFactor: 4}}),
		scanAtVN(2027, 5, 1, 18, 0, []fixturePersistentPoint{{Lat: 1, Lng: 1, JamFactor: 10}}), // after cutoff
	})
	writeTrafficDay(t, "2027-05-02", []fixtureScan{
		scanAtVN(2027, 5, 2, 8, 0, []fixturePersistentPoint{{Lat: 1, Lng: 1, JamFactor: 6}}),
		scanAtVN(2027, 5, 2, 18, 0, []fixturePersistentPoint{{Lat: 1, Lng: 1, JamFactor: 10}}), // after cutoff
	})

	from := time.Date(2027, 5, 1, 0, 0, 0, 0, vnLocation)
	to := time.Date(2027, 5, 2, 0, 0, 0, 0, vnLocation)
	cutoff := time.Date(2027, 5, 2, 12, 0, 0, 0, vnLocation) // noon — excludes both days' 18:00 scans
	agg, err := GetRangeAverageUntil(from, to, cutoff, RouteFilter{})
	if err != nil {
		t.Fatalf("GetRangeAverageUntil: %v", err)
	}
	if agg.JamDaysWithData != 2 {
		t.Fatalf("JamDaysWithData = %d, want 2", agg.JamDaysWithData)
	}
	wantAvg := (4.0 + 6.0) / 2.0 // the 18:00 (JF 10) scans must not pull this up
	if diff := agg.JamAvgFactor - wantAvg; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("JamAvgFactor = %v, want %v (post-cutoff scans must be excluded)", agg.JamAvgFactor, wantAvg)
	}
}

func TestGetDailySeriesReturnsOneEntryPerDayOldestFirst(t *testing.T) {
	setupSources(t)
	series := GetDailySeries(5, RouteFilter{})
	if len(series) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(series))
	}
	today := time.Now().In(vnLocation).Format("2006-01-02")
	if series[len(series)-1].Date != today {
		t.Errorf("last entry's date = %q, want today %q", series[len(series)-1].Date, today)
	}
	for i := 1; i < len(series); i++ {
		if series[i].Date <= series[i-1].Date {
			t.Fatalf("series not strictly increasing by date at index %d: %q then %q", i, series[i-1].Date, series[i].Date)
		}
	}
}

func TestGetDailySeriesRangeReturnsInclusiveOldestFirst(t *testing.T) {
	setupSources(t)
	series, err := GetDailySeriesRange("2027-04-01", "2027-04-05", RouteFilter{})
	if err != nil {
		t.Fatalf("GetDailySeriesRange: %v", err)
	}
	if len(series) != 5 {
		t.Fatalf("expected 5 entries (inclusive both ends), got %d: %+v", len(series), series)
	}
	if series[0].Date != "2027-04-01" || series[len(series)-1].Date != "2027-04-05" {
		t.Errorf("unexpected bounds: first=%q last=%q", series[0].Date, series[len(series)-1].Date)
	}
	for i := 1; i < len(series); i++ {
		if series[i].Date <= series[i-1].Date {
			t.Fatalf("series not strictly increasing by date at index %d: %q then %q", i, series[i-1].Date, series[i].Date)
		}
	}
}

func TestGetDailySeriesRangeSwapsReversedBounds(t *testing.T) {
	setupSources(t)
	series, err := GetDailySeriesRange("2027-04-05", "2027-04-01", RouteFilter{}) // from/to reversed
	if err != nil {
		t.Fatalf("GetDailySeriesRange: %v", err)
	}
	if len(series) != 5 || series[0].Date != "2027-04-01" || series[len(series)-1].Date != "2027-04-05" {
		t.Fatalf("expected the same 5-day range regardless of argument order, got %+v", series)
	}
}

func TestGetDailySeriesRangeClampsOverlongRangeKeepingRecentEnd(t *testing.T) {
	setupSources(t)
	series, err := GetDailySeriesRange("2027-01-01", "2027-06-01", RouteFilter{}) // ~152 days, over the 90-day cap
	if err != nil {
		t.Fatalf("GetDailySeriesRange: %v", err)
	}
	if len(series) != maxDailySeriesDays {
		t.Fatalf("expected the range clamped to %d days, got %d", maxDailySeriesDays, len(series))
	}
	if series[len(series)-1].Date != "2027-06-01" {
		t.Errorf("expected the clamp to keep the requested 'to' end, last date = %q", series[len(series)-1].Date)
	}
}

func TestGetDailySeriesRangeInvalidDateReturnsError(t *testing.T) {
	setupSources(t)
	if _, err := GetDailySeriesRange("not-a-date", "2027-04-01", RouteFilter{}); err == nil {
		t.Error("expected an error for an invalid 'from' date, got nil")
	}
	if _, err := GetDailySeriesRange("2027-04-01", "not-a-date", RouteFilter{}); err == nil {
		t.Error("expected an error for an invalid 'to' date, got nil")
	}
}

// TestGetWeekdayMatrixPerMetricHasDataFlags guards against a cell showing
// "0.0" for a metric that was never actually measured that day (e.g. jam
// data exists but no TTI samples were collected) — HasJamData/HasTTIData
// must be independent, not both tied to the OR'd HasData used only to grey
// out a cell with nothing at all.
func TestGetWeekdayMatrixPerMetricHasDataFlags(t *testing.T) {
	setupSources(t)
	today := time.Now().In(vnLocation)
	date := today.Format("2006-01-02")

	// Jam data only for today — no travel-time fixture is written.
	writeTrafficDay(t, date, []fixtureScan{
		scanAtVN(today.Year(), int(today.Month()), today.Day(), 9, 0, []fixturePersistentPoint{
			{Lat: 1, Lng: 1, JamFactor: 5},
		}),
	})

	result := GetWeekdayMatrix(1, RouteFilter{})
	offset := (int(today.Weekday()) + 6) % 7
	cell := result.Cells[offset][0]
	if cell.Date != date {
		t.Fatalf("expected today's cell at [%d][0] to be %q, got %q", offset, date, cell.Date)
	}
	if !cell.HasJamData {
		t.Error("expected HasJamData=true (jam fixture was written for today)")
	}
	if cell.HasTTIData {
		t.Error("expected HasTTIData=false (no travel-time fixture was written for today)")
	}
	if !cell.HasData {
		t.Error("expected the OR'd HasData=true since jam data exists")
	}
}

func TestGetWeekdayMatrixShapeAndFutureDays(t *testing.T) {
	setupSources(t)
	result := GetWeekdayMatrix(4, RouteFilter{})
	if len(result.Weeks) != 4 {
		t.Fatalf("expected 4 weeks, got %d", len(result.Weeks))
	}
	if len(result.Weekdays) != 7 {
		t.Fatalf("expected 7 weekday labels, got %d", len(result.Weekdays))
	}
	if len(result.Cells) != 7 {
		t.Fatalf("expected 7 weekday rows, got %d", len(result.Cells))
	}
	for d, row := range result.Cells {
		if len(row) != 4 {
			t.Fatalf("row %d: expected 4 week columns, got %d", d, len(row))
		}
	}

	now := time.Now().In(vnLocation)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, vnLocation)
	foundFuture := false
	for _, row := range result.Cells {
		last := row[len(row)-1] // current week's column
		cellDate, _ := time.ParseInLocation("2006-01-02", last.Date, vnLocation)
		if cellDate.After(today) {
			if !last.Future {
				t.Errorf("cell %q is after today but Future=false", last.Date)
			}
			foundFuture = true
		} else if last.Future {
			t.Errorf("cell %q is today or earlier but Future=true", last.Date)
		}
	}
	// The current week has at least one future day unless today is Sunday.
	if now.Weekday() != time.Sunday && !foundFuture {
		t.Error("expected at least one future day in the current week's column")
	}
}

func TestGetWeekdayMatrixCapsExcessiveWeeks(t *testing.T) {
	setupSources(t)
	result := GetWeekdayMatrix(1000, RouteFilter{})
	if len(result.Weeks) != maxWeekdayMatrixWeeks {
		t.Errorf("expected weeks capped at %d, got %d", maxWeekdayMatrixWeeks, len(result.Weeks))
	}
}

func TestGetTodaySnapshotUsesLiveScanPoints(t *testing.T) {
	setupSources(t)
	snap, err := GetTodaySnapshot()
	if err != nil {
		t.Fatalf("GetTodaySnapshot: %v", err)
	}
	today := time.Now().In(vnLocation).Format("2006-01-02")
	if snap.Date != today {
		t.Errorf("snapshot date = %q, want %q", snap.Date, today)
	}
	// No live scan has run in this test process, so Points should be empty
	// rather than erroring.
	if snap.Points == nil && len(snap.Points) != 0 {
		t.Errorf("expected Points to be an empty slice, not nil-with-length-mismatch")
	}
}

// TestGetTodaySnapshotExposesCurrentTTIFromLatestSnapshot guards the "TTI
// TB hiện tại"/"Trễ TB hiện tại" KPIs' wiring: CurrentTTIAvg/
// CurrentTTIAvgDelayMin/HasCurrentTTIData must be exactly
// traveltime.LatestSnapshot()'s own AvgTTI/AvgDelaySec (in minutes) —
// the "latest scan per route" metric dashboard.html's "TTI TB" already
// shows — not Metrics.TTIAvg (this package's own, separately-tested
// full-day-average). Routes/"latest per route" selection is
// traveltime.LatestSnapshot()'s own tested responsibility; this only
// checks the pass-through, so no route or travel-log fixtures are needed
// here — it compares against a second, independent call to the same
// function LatestSnapshot() itself, whatever state the shared test
// fixture directory happens to be in.
func TestGetTodaySnapshotExposesCurrentTTIFromLatestSnapshot(t *testing.T) {
	setupSources(t)

	snap, err := GetTodaySnapshot()
	if err != nil {
		t.Fatalf("GetTodaySnapshot: %v", err)
	}
	want := traveltime.LatestSnapshot()

	if snap.CurrentTTIAvg != want.AvgTTI {
		t.Errorf("CurrentTTIAvg = %v, want %v (traveltime.LatestSnapshot().AvgTTI)", snap.CurrentTTIAvg, want.AvgTTI)
	}
	wantDelayMin := want.AvgDelaySec / 60.0
	if snap.CurrentTTIAvgDelayMin != wantDelayMin {
		t.Errorf("CurrentTTIAvgDelayMin = %v, want %v", snap.CurrentTTIAvgDelayMin, wantDelayMin)
	}
	wantHasData := want.AvgTTI > 0
	if snap.HasCurrentTTIData != wantHasData {
		t.Errorf("HasCurrentTTIData = %v, want %v", snap.HasCurrentTTIData, wantHasData)
	}
}
