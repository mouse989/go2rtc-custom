package trafficinsight

// hourly_series.go — "Xu hướng theo giờ" module: the exact same
// jam/TTI/delay/incident metrics as the "Xu hướng theo ngày" daily trend
// chart (see GetDailySeries/GetDailySeriesRange in insight.go), but for a
// single Vietnam-local calendar day sliced into its 24 hour-of-day
// buckets instead of across multiple days — "which hours of today were
// actually bad" rather than "which days were bad".

import (
	"time"

	"github.com/AlexxIT/go2rtc/internal/incidents"
	"github.com/AlexxIT/go2rtc/internal/traffic"
	"github.com/AlexxIT/go2rtc/internal/traveltime"
)

// HourlyMetrics is one hour-of-day (0-23, Vietnam-local) bucket within a
// single calendar day — same fields/semantics as DailyMetrics, just
// scoped to an hour instead of a whole day.
type HourlyMetrics struct {
	Hour int `json:"hour"` // 0-23

	JamAvgFactor  float64 `json:"jamAvgFactor"`
	JamPointCount int     `json:"jamPointCount"`
	HasJamData    bool    `json:"hasJamData"`

	TTIAvg         float64 `json:"ttiAvg"`
	TTIAvgDelayMin float64 `json:"ttiAvgDelayMin"`
	TTISampleCount int     `json:"ttiSampleCount"`
	HasTTIData     bool    `json:"hasTTIData"`

	IncidentCount int `json:"incidentCount"`
}

// GetHourlySeries returns 24 HourlyMetrics (hour 0..23, always present and
// in order even for hours with no data) for one Vietnam-local calendar
// day "date" (YYYY-MM-DD).
func GetHourlySeries(date string) ([]HourlyMetrics, error) {
	start, end, err := dayBounds(date)
	if err != nil {
		return nil, err
	}

	out := make([]HourlyMetrics, 24)
	for h := range out {
		out[h].Hour = h
	}

	// Each row's Timestamp is pre-formatted "01/02/2006 15:04:05" Vietnam
	// wall-clock (see PersistentExportRow) — its hour is read directly,
	// no timezone conversion needed.
	if rows, err := traffic.PersistentPointsInRange(start, end); err == nil {
		sums := make([]float64, 24)
		counts := make([]int, 24)
		for _, row := range rows {
			t, err := time.Parse("01/02/2006 15:04:05", row.Timestamp)
			if err != nil {
				continue
			}
			h := t.Hour()
			sums[h] += row.JamFactor
			counts[h]++
		}
		for h := range out {
			if counts[h] > 0 {
				out[h].JamPointCount = counts[h]
				out[h].JamAvgFactor = sums[h] / float64(counts[h])
				out[h].HasJamData = true
			}
		}
	}

	// Same TTI<=0/missing-RouteID exclusion as computeDailyMetrics — see
	// its comment for why (a failed/empty scan isn't a real "free flow"
	// reading and must not be averaged in as if it were).
	if entries, err := traveltime.GetLogsForDate(date, 0); err == nil {
		ttiSums := make([]float64, 24)
		delaySums := make([]int, 24)
		counts := make([]int, 24)
		for _, e := range entries {
			if e.RouteID == "" || e.TTI <= 0 {
				continue
			}
			t, err := time.Parse(time.RFC3339, e.Timestamp)
			if err != nil || t.Before(start) || t.After(end) {
				continue
			}
			h := t.In(vnLocation).Hour()
			ttiSums[h] += e.TTI
			delaySums[h] += e.DelaySec
			counts[h]++
		}
		for h := range out {
			if counts[h] > 0 {
				out[h].TTISampleCount = counts[h]
				out[h].TTIAvg = ttiSums[h] / float64(counts[h])
				out[h].TTIAvgDelayMin = float64(delaySums[h]) / float64(counts[h]) / 60.0
				out[h].HasTTIData = true
			}
		}
	}

	if list, err := incidents.List(incidents.ListFilter{Year: start.Year(), From: start, To: end}); err == nil {
		for _, in := range list {
			h := in.Time.In(vnLocation).Hour()
			out[h].IncidentCount++
		}
	}

	return out, nil
}
