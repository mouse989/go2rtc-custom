package traffic

// flow_capture_history.go — www/flowcapture.html's "xem theo thời điểm" and
// "thống kê giờ ùn tắc theo tuyến đường" features: browsing a past moment
// (scanNearTimePerRegion, flow_capture_db.go) and, for one named road,
// which hour of day it most often reaches a chosen LOS severity.
//
// Both the road list and the hourly stats REQUIRE an explicit from/to
// range, capped at maxFlowHistoryRangeDays — flow_capture.db keeps every
// scan forever by default (FlowCaptureRetentionDays, 0 = no cleanup), so
// an unbounded query here would scan the whole, ever-growing
// flow_segments table (one row per captured road PER SCAN, the biggest
// table this package writes) instead of the small flow_scans table
// scanNearTimePerRegion is limited to. validateHistoryRange is the one
// place that cap is enforced, called from every handler in this file.

import (
	"fmt"
	"time"
)

// maxFlowHistoryRangeDays bounds how wide a from/to window
// listFlowRoads/computeRoadHourlyStats will accept — about 2 months,
// generous enough for "so sánh tháng trước" while still keeping a single
// query's row count bounded regardless of how long flow_capture.db has
// been running.
const maxFlowHistoryRangeDays = 62

func validateHistoryRange(from, to time.Time) error {
	if from.IsZero() || to.IsZero() {
		return fmt.Errorf("from and to are required")
	}
	if to.Before(from) {
		return fmt.Errorf("to must not be before from")
	}
	if to.Sub(from) > maxFlowHistoryRangeDays*24*time.Hour {
		return fmt.Errorf("khoảng thời gian tối đa %d ngày", maxFlowHistoryRangeDays)
	}
	return nil
}

// FlowRoadOption is one (region, named road) pair seen at least once in a
// queried range — www/flowcapture.html's road picker for the hourly-stats
// tool.
type FlowRoadOption struct {
	RegionName  string `json:"regionName"`
	Description string `json:"description"`
}

// listFlowRoads returns every distinct (region, description) pair with a
// non-empty description seen within [from, to] — unnamed results
// (description == "") are left out, same as tti_suggest.go's own
// named-road grouping: there's no stable identity to pick a road by
// without a name.
func listFlowRoads(from, to time.Time) ([]FlowRoadOption, error) {
	if err := validateHistoryRange(from, to); err != nil {
		return nil, err
	}
	if flowCaptureDB == nil {
		return []FlowRoadOption{}, nil
	}
	rows, err := flowCaptureDB.Query(`
		SELECT DISTINCT fsc.region_name, fs.description
		FROM flow_segments fs
		JOIN flow_scans fsc ON fsc.id = fs.scan_id
		WHERE fsc.scanned_at >= ? AND fsc.scanned_at <= ? AND fs.sub_index = -1 AND fs.description != ''
		ORDER BY fsc.region_name, fs.description
	`, from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("query flow_segments: %w", err)
	}
	defer rows.Close()
	out := []FlowRoadOption{}
	for rows.Next() {
		var o FlowRoadOption
		if err := rows.Scan(&o.RegionName, &o.Description); err != nil {
			return nil, fmt.Errorf("scan road row: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// HourlyLOSStats is one hour-of-day's (0-23, Vietnam local time) own
// aggregate over the queried range, for one road.
type HourlyLOSStats struct {
	Hour       int     `json:"hour"`
	Count      int     `json:"count"`
	AvgTTI     float64 `json:"avgTti"`
	MaxTTI     float64 `json:"maxTti"`
	WorstCount int     `json:"worstCount"` // readings whose los_label was one of the requested levels
	WorstRate  float64 `json:"worstRate"`  // worstCount / count
}

// RoadHourlyReport is computeRoadHourlyStats' full result: a 24-entry
// (always all 24 hours, zero-filled where there's no data) breakdown plus
// the single worst hour for quick display.
type RoadHourlyReport struct {
	RegionName  string           `json:"regionName"`
	Description string           `json:"description"`
	From        string           `json:"from"`
	To          string           `json:"to"`
	Levels      []string         `json:"levels"`
	Hours       []HourlyLOSStats `json:"hours"`    // always len 24, index == hour
	PeakHour    int              `json:"peakHour"` // -1 when no data at all in range
}

// computeRoadHourlyStats answers "vào giờ nào trong ngày tuyến này hay đạt
// mức ùn [levels] nhất?" for one (region, description) road over
// [from, to]. levels is e.g. {"D","E","F"} or {"E","F"} — a reading
// "counts" toward WorstCount/WorstRate when its own los_label is in that
// set; PeakHour is the hour with the highest WorstRate (ties broken by
// AvgTTI), among hours that have at least one reading.
func computeRoadHourlyStats(region, description string, from, to time.Time, levels []string) (*RoadHourlyReport, error) {
	if err := validateHistoryRange(from, to); err != nil {
		return nil, err
	}
	if description == "" {
		return nil, fmt.Errorf("description is required")
	}
	wantLevel := map[string]bool{}
	for _, l := range levels {
		wantLevel[l] = true
	}

	report := &RoadHourlyReport{
		RegionName: region, Description: description,
		From: from.UTC().Format(time.RFC3339), To: to.UTC().Format(time.RFC3339),
		Levels: levels, Hours: make([]HourlyLOSStats, 24), PeakHour: -1,
	}
	for h := 0; h < 24; h++ {
		report.Hours[h].Hour = h
	}
	if flowCaptureDB == nil {
		return report, nil
	}

	rows, err := flowCaptureDB.Query(`
		SELECT fsc.scanned_at, fs.tti_fused, fs.los_label
		FROM flow_segments fs
		JOIN flow_scans fsc ON fsc.id = fs.scan_id
		WHERE fsc.scanned_at >= ? AND fsc.scanned_at <= ? AND fs.sub_index = -1
		  AND fsc.region_name = ? AND fs.description = ?
	`, from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339), region, description)
	if err != nil {
		return nil, fmt.Errorf("query flow_segments: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var scannedAtStr, losLabel string
		var ttiFused float64
		if err := rows.Scan(&scannedAtStr, &ttiFused, &losLabel); err != nil {
			return nil, fmt.Errorf("scan hourly row: %w", err)
		}
		scannedAt, err := time.Parse(time.RFC3339, scannedAtStr)
		if err != nil {
			continue // malformed row — skip rather than fail the whole report
		}
		h := scannedAt.In(vnLocation).Hour()
		hs := &report.Hours[h]
		hs.Count++
		hs.AvgTTI += ttiFused // running sum for now, divided below once Count is final
		if ttiFused > hs.MaxTTI {
			hs.MaxTTI = ttiFused
		}
		if wantLevel[losLabel] {
			hs.WorstCount++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hourly rows: %w", err)
	}

	for h := range report.Hours {
		hs := &report.Hours[h]
		if hs.Count > 0 {
			hs.AvgTTI = round2(hs.AvgTTI / float64(hs.Count))
			hs.WorstRate = round2(float64(hs.WorstCount) / float64(hs.Count))
		}
		hs.MaxTTI = round2(hs.MaxTTI)
	}
	// Pick the hour with the highest WorstRate, among hours that actually
	// have data; ties go to the higher AvgTTI. A separate pass, after
	// every hour's AvgTTI/WorstRate above is already finalized, so the
	// comparison never reads a not-yet-divided running sum.
	for h := range report.Hours {
		hs := &report.Hours[h]
		if hs.Count == 0 {
			continue
		}
		if report.PeakHour < 0 {
			report.PeakHour = h
			continue
		}
		best := &report.Hours[report.PeakHour]
		if hs.WorstRate > best.WorstRate || (hs.WorstRate == best.WorstRate && hs.AvgTTI > best.AvgTTI) {
			report.PeakHour = h
		}
	}
	return report, nil
}
