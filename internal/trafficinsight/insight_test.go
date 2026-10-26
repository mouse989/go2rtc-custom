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

	m, err := GetDailyMetrics(date)
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
	m, err := GetDailyMetrics("2027-03-16")
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
	agg, err := GetRangeAverage(from, to)
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

func TestGetDailySeriesReturnsOneEntryPerDayOldestFirst(t *testing.T) {
	setupSources(t)
	series := GetDailySeries(5)
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

	result := GetWeekdayMatrix(1)
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
	result := GetWeekdayMatrix(4)
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
	result := GetWeekdayMatrix(1000)
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
