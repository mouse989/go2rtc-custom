// Package trafficinsight aggregates three already-existing data sources —
// internal/traffic's persistent jam points, internal/traveltime's TTI logs,
// and internal/incidents' incident log — into day-level metrics for trend
// visualization: today vs. the last 7 days' average vs. the last 30 days'
// average, and a weekday-by-week comparison matrix (e.g. "is this Monday
// worse than the last few Mondays"), so an operator can see that without
// manually cross-referencing three separate pages.
//
// This package owns no storage of its own — everything is computed on
// demand from each source's already-exported query functions
// (traffic.PersistentPointsInRange, traveltime.GetLogsForDate,
// incidents.List). It's read-only and safe to call from any authenticated
// request; there is nothing here for an admin to configure.
package trafficinsight

import (
	"time"

	"github.com/AlexxIT/go2rtc/internal/incidents"
	"github.com/AlexxIT/go2rtc/internal/traffic"
	"github.com/AlexxIT/go2rtc/internal/traveltime"
)

// vnLocation anchors every "calendar day" in this package to Vietnam
// wall-clock time, regardless of the server process/OS timezone — same
// pattern internal/traffic/storage.go and internal/incidents/timeslots.go
// each already use independently.
var vnLocation = loadVNLocation()

func loadVNLocation() *time.Location {
	if loc, err := time.LoadLocation("Asia/Ho_Chi_Minh"); err == nil {
		return loc
	}
	return time.FixedZone("ICT", 7*3600)
}

// DailyMetrics holds every source's aggregate for one Vietnam-local
// calendar day ("YYYY-MM-DD"). Each source is independent and best-effort:
// a source with nothing recorded that day (Traffic Monitor wasn't running,
// no incidents logged) just leaves its Has*Data flag false rather than
// failing the whole call.
type DailyMetrics struct {
	Date string `json:"date"`

	JamAvgFactor  float64 `json:"jamAvgFactor"`
	JamPointCount int     `json:"jamPointCount"` // persistent-point readings summed across the day's scans
	JamScanCount  int     `json:"jamScanCount"`  // distinct scan timestamps that day
	HasJamData    bool    `json:"hasJamData"`

	TTIAvg         float64 `json:"ttiAvg"`
	TTISampleCount int     `json:"ttiSampleCount"`
	HasTTIData     bool    `json:"hasTTIData"`

	IncidentCount       int            `json:"incidentCount"`
	IncidentsByCategory map[string]int `json:"incidentsByCategory,omitempty"`
}

// dayBounds returns the [start, end] instants of the Vietnam-local calendar
// day "date" (YYYY-MM-DD), end inclusive — same convention
// internal/incidents/api.go's parseListFilter uses for its own date filter.
func dayBounds(date string) (start, end time.Time, err error) {
	start, err = time.ParseInLocation("2006-01-02", date, vnLocation)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	end = start.Add(24*time.Hour - time.Second)
	return start, end, nil
}

// GetDailyMetrics computes the aggregate for one Vietnam-local calendar day.
func GetDailyMetrics(date string) (DailyMetrics, error) {
	start, end, err := dayBounds(date)
	if err != nil {
		return DailyMetrics{}, err
	}
	m := DailyMetrics{Date: date}

	if rows, err := traffic.PersistentPointsInRange(start, end); err == nil && len(rows) > 0 {
		var sum float64
		scans := map[string]bool{}
		for _, row := range rows {
			sum += row.JamFactor
			scans[row.Timestamp] = true
		}
		m.JamPointCount = len(rows)
		m.JamAvgFactor = sum / float64(len(rows))
		m.JamScanCount = len(scans)
		m.HasJamData = true
	}

	if entries, err := traveltime.GetLogsForDate(date, 0); err == nil && len(entries) > 0 {
		var sum float64
		for _, e := range entries {
			sum += e.TTI
		}
		m.TTISampleCount = len(entries)
		m.TTIAvg = sum / float64(len(entries))
		m.HasTTIData = true
	}

	if list, err := incidents.List(incidents.ListFilter{Year: start.Year(), From: start, To: end}); err == nil {
		m.IncidentCount = len(list)
		if len(list) > 0 {
			m.IncidentsByCategory = map[string]int{}
			for _, in := range list {
				cat := in.Category
				if cat == "" {
					cat = "Khác"
				}
				m.IncidentsByCategory[cat]++
			}
		}
	}

	return m, nil
}

// AggregateMetrics summarizes DailyMetrics across a range of days — used
// for the 7-day and 30-day baselines. Each metric's average is a simple
// mean of only the days that actually had data for that metric (not
// weighted by sample count and not diluted by data-less days), so a day the
// Traffic Monitor happened to be off doesn't drag the jam average toward
// zero.
type AggregateMetrics struct {
	FromDate string `json:"fromDate"`
	ToDate   string `json:"toDate"`
	DayCount int    `json:"dayCount"`

	JamAvgFactor      float64 `json:"jamAvgFactor"`
	JamDaysWithData   int     `json:"jamDaysWithData"`
	JamPointAvgPerDay float64 `json:"jamPointAvgPerDay"`

	TTIAvg          float64 `json:"ttiAvg"`
	TTIDaysWithData int     `json:"ttiDaysWithData"`

	IncidentAvgPerDay float64 `json:"incidentAvgPerDay"`
	IncidentTotal     int     `json:"incidentTotal"`
}

// GetRangeAverage averages each metric over every Vietnam-local calendar
// day from "from" to "to" inclusive.
func GetRangeAverage(from, to time.Time) (AggregateMetrics, error) {
	fromLocal := from.In(vnLocation)
	toLocal := to.In(vnLocation)
	out := AggregateMetrics{
		FromDate: fromLocal.Format("2006-01-02"),
		ToDate:   toLocal.Format("2006-01-02"),
	}

	var jamSum, ttiSum float64
	var jamPointSum int

	day := time.Date(fromLocal.Year(), fromLocal.Month(), fromLocal.Day(), 0, 0, 0, 0, vnLocation)
	last := time.Date(toLocal.Year(), toLocal.Month(), toLocal.Day(), 0, 0, 0, 0, vnLocation)
	for !day.After(last) {
		m, err := GetDailyMetrics(day.Format("2006-01-02"))
		if err == nil {
			out.DayCount++
			if m.HasJamData {
				jamSum += m.JamAvgFactor
				jamPointSum += m.JamPointCount
				out.JamDaysWithData++
			}
			if m.HasTTIData {
				ttiSum += m.TTIAvg
				out.TTIDaysWithData++
			}
			out.IncidentTotal += m.IncidentCount
		}
		day = day.AddDate(0, 0, 1)
	}

	if out.JamDaysWithData > 0 {
		out.JamAvgFactor = jamSum / float64(out.JamDaysWithData)
		out.JamPointAvgPerDay = float64(jamPointSum) / float64(out.JamDaysWithData)
	}
	if out.TTIDaysWithData > 0 {
		out.TTIAvg = ttiSum / float64(out.TTIDaysWithData)
	}
	if out.DayCount > 0 {
		out.IncidentAvgPerDay = float64(out.IncidentTotal) / float64(out.DayCount)
	}
	return out, nil
}

// TodaySnapshot is the page's "right now" section: today's day-level
// aggregate plus the LIVE current-moment map points (traffic.LatestSnapshot
// — the in-memory result of the most recent scan), not the day's full
// accumulated history, so the map reflects what's happening right now
// rather than a blur of every scan since midnight.
type TodaySnapshot struct {
	Date      string              `json:"date"`
	Metrics   DailyMetrics        `json:"metrics"`
	Points    []traffic.DashPoint `json:"points"`
	ScannedAt string              `json:"scannedAt,omitempty"`
}

// GetTodaySnapshot returns TodaySnapshot for the current Vietnam-local date.
func GetTodaySnapshot() (TodaySnapshot, error) {
	date := time.Now().In(vnLocation).Format("2006-01-02")
	m, err := GetDailyMetrics(date)
	if err != nil {
		return TodaySnapshot{}, err
	}
	live := traffic.LatestSnapshot()
	return TodaySnapshot{
		Date:      date,
		Metrics:   m,
		Points:    live.Points,
		ScannedAt: live.ScannedAt,
	}, nil
}

// maxDailySeriesDays bounds the ?days= query param on the trend-chart
// endpoint for the same reason maxWeekdayMatrixWeeks bounds ?weeks=: each
// extra day is another GetDailyMetrics call (3 source reads).
const maxDailySeriesDays = 90

// GetDailySeries returns one DailyMetrics per Vietnam-local calendar day,
// oldest first, for the trailing `days` days (today inclusive) — the data
// behind the page's day-by-day trend chart.
func GetDailySeries(days int) []DailyMetrics {
	if days <= 0 {
		days = 30
	}
	if days > maxDailySeriesDays {
		days = maxDailySeriesDays
	}

	now := time.Now().In(vnLocation)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, vnLocation)

	out := make([]DailyMetrics, 0, days)
	for i := days - 1; i >= 0; i-- {
		date := today.AddDate(0, 0, -i).Format("2006-01-02")
		if m, err := GetDailyMetrics(date); err == nil {
			out = append(out, m)
		}
	}
	return out
}

// WeekdayMatrixCell is one day's metrics placed at a (weekday, week)
// position in the comparison grid.
type WeekdayMatrixCell struct {
	Date          string  `json:"date"`
	JamAvgFactor  float64 `json:"jamAvgFactor"`
	TTIAvg        float64 `json:"ttiAvg"`
	IncidentCount int     `json:"incidentCount"`
	HasData       bool    `json:"hasData"` // any source had data this day
	Future        bool    `json:"future"`  // day hasn't happened yet (current week's remaining days)
}

// WeekInfo labels one column (one week) of the matrix.
type WeekInfo struct {
	WeekStart string `json:"weekStart"` // Monday's date, YYYY-MM-DD
	Label     string `json:"label"`     // e.g. "20/10 - 26/10"
}

// WeekdayMatrixResult is a weeks (oldest→newest) × 7-weekdays (Mon→Sun)
// grid: Cells[weekdayIndex][weekIndex].
type WeekdayMatrixResult struct {
	Weeks    []WeekInfo            `json:"weeks"`
	Weekdays []string              `json:"weekdays"`
	Cells    [][]WeekdayMatrixCell `json:"cells"`
}

var vietnameseWeekdays = []string{"Thứ 2", "Thứ 3", "Thứ 4", "Thứ 5", "Thứ 6", "Thứ 7", "Chủ nhật"}

// maxWeekdayMatrixWeeks bounds the ?weeks= query param: each extra week is
// 7 more GetDailyMetrics calls (each reading a traffic day-file, a
// travel-time day-file, and the incidents year already held in memory), so
// an unbounded value could be used to force a lot of disk I/O per request.
const maxWeekdayMatrixWeeks = 12

// GetWeekdayMatrix returns the last `weeks` ISO weeks (Monday-start,
// Vietnam local), oldest first, ending with the current — possibly
// partial — week. A day that hasn't happened yet is marked Future instead
// of being queried.
func GetWeekdayMatrix(weeks int) WeekdayMatrixResult {
	if weeks <= 0 {
		weeks = 6
	}
	if weeks > maxWeekdayMatrixWeeks {
		weeks = maxWeekdayMatrixWeeks
	}

	now := time.Now().In(vnLocation)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, vnLocation)
	// ISO weekday with Monday=0 .. Sunday=6.
	offset := (int(today.Weekday()) + 6) % 7
	curMonday := today.AddDate(0, 0, -offset)

	result := WeekdayMatrixResult{Weekdays: vietnameseWeekdays}
	result.Cells = make([][]WeekdayMatrixCell, 7)
	for i := range result.Cells {
		result.Cells[i] = make([]WeekdayMatrixCell, weeks)
	}

	for w := 0; w < weeks; w++ {
		weekStart := curMonday.AddDate(0, 0, -7*(weeks-1-w))
		weekEnd := weekStart.AddDate(0, 0, 6)
		result.Weeks = append(result.Weeks, WeekInfo{
			WeekStart: weekStart.Format("2006-01-02"),
			Label:     weekStart.Format("02/01") + " - " + weekEnd.Format("02/01"),
		})
		for d := 0; d < 7; d++ {
			day := weekStart.AddDate(0, 0, d)
			cell := WeekdayMatrixCell{Date: day.Format("2006-01-02")}
			if day.After(today) {
				cell.Future = true
			} else if m, err := GetDailyMetrics(cell.Date); err == nil {
				cell.JamAvgFactor = m.JamAvgFactor
				cell.TTIAvg = m.TTIAvg
				cell.IncidentCount = m.IncidentCount
				cell.HasData = m.HasJamData || m.HasTTIData || m.IncidentCount > 0
			}
			result.Cells[d][w] = cell
		}
	}
	return result
}
