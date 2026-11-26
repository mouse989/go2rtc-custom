package traffic

// tti_suggest.go — "gợi ý tuyến cần theo dõi TTI": scans flow_capture.db's
// already-captured flow_segments (flow_capture_db.go — a SEPARATE research
// pipeline from monitor.go's live jam-point map, see that file's own doc
// comment) over a recent time window, groups rows that are the same
// physical named road (or, when unnamed, the same ~100m coordinate cell)
// within the same monitored region, and surfaces the ones that were
// congested often enough across that window to be worth tracking
// continuously as a dedicated internal/traveltime Route instead of only
// ever showing up as an ad-hoc jam alert.
//
// "Congested" reuses flow_segments' own tti_fused column — the same TTI
// figure, and by default the same LOS-D threshold (1.96), as the official
// LOS A-F classification already calibrated in internal/trafficmetrics
// (see its defaultLOSLevels) — rather than inventing a separate jamFactor
// cutoff. Dedup against routes already being tracked uses
// internal/traveltime.ListRoutes() directly (an in-process call, not an
// HTTP request — no import cycle: traveltime never imports this package).
//
// Chosen over monitor.go's live jam-point pipeline specifically because
// flow_capture rows carry the provider's own real road geometry
// (first/last lat,lng of an actual polyline, plus the street name) —
// a point cluster's centroid alone can't tell you where a candidate route
// should start/end.

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AlexxIT/go2rtc/internal/traveltime"
)

// HotspotDataQuality reports how complete the flow_segments rows in the
// queried [from, to] window actually are — "khi chọn ngày xong sẽ load
// trong db để suggest, nên kiểm tra phần db ... xem có đầy đủ dữ liệu
// link; jf; speed": different date ranges can have different capture
// coverage (a region added/removed, an outage, a provider response that
// came back without shape.links for a stretch), so this is computed fresh
// per request rather than assumed constant. ScansExamined/WithGeometry
// count flow_segments rows (sub_index=-1, i.e. whole-road rows, not
// subSegments) BEFORE hotspotGroupKey's own geometry filter runs — the
// hotspots list below only ever reflects WithGeometry of these, so a low
// GeometryRate here is the reason a date range "has fewer suggestions
// than expected" even though ScansExamined looks healthy.
type HotspotDataQuality struct {
	ScansExamined  int     `json:"scansExamined"`
	WithGeometry   int     `json:"withGeometry"`
	GeometryRate   float64 `json:"geometryRate"`
	ZeroSpeedCount int     `json:"zeroSpeedCount"` // speed_mps == 0 AND free_flow_mps == 0 — a reading that likely never carried real speed data
}

// HotspotReport is computeHotspots' full result: the data-quality summary
// above, alongside the ranked suggestions themselves.
type HotspotReport struct {
	DataQuality HotspotDataQuality  `json:"dataQuality"`
	Hotspots    []HotspotSuggestion `json:"hotspots"`
}

// HotspotSuggestion is one candidate route a TabTtiSuggest holder may want
// to start tracking continuously, derived from recurring congestion seen
// in the flow-capture history.
type HotspotSuggestion struct {
	Key            string  `json:"key"` // region+description (or region+grid cell) — stable within one response, for the "+ Thêm vào theo dõi" button to reference back
	Description    string  `json:"description"`
	RegionName     string  `json:"regionName"`
	ScanCount      int     `json:"scanCount"`
	CongestedCount int     `json:"congestedCount"`
	CongestionRate float64 `json:"congestionRate"` // congestedCount / scanCount
	AvgJamFactor   float64 `json:"avgJamFactor"`
	WorstTTI       float64 `json:"worstTti"`
	WorstLOSLabel  string  `json:"worstLosLabel"`
	LengthM        float64 `json:"lengthM"`
	FirstLat       float64 `json:"firstLat"`
	FirstLng       float64 `json:"firstLng"`
	LastLat        float64 `json:"lastLat"`
	LastLng        float64 `json:"lastLng"`
	LastSeenAt     string  `json:"lastSeenAt,omitempty"` // RFC3339, most recent congested scan
	Covered        bool    `json:"covered"`              // an existing traveltime Route already runs through both endpoints
	CoveredByRoute string  `json:"coveredByRoute,omitempty"`
}

// defaultDedupRadiusM: how close a candidate's own endpoint must be to an
// existing route's own point (origin, destination, or any waypoint) to
// call that candidate already tracked — same order of magnitude as
// Config.ClusterRadius/PersistRadius elsewhere in this package.
const defaultDedupRadiusM = 400.0

// defaultHotspotMinTTI is LOS D's own TTI boundary in
// internal/trafficmetrics's defaultLOSLevels — "cần chủ động triển khai
// biện pháp phòng ngừa ùn tắc" per the official LOS A-F standard, the
// threshold past which sustained congestion is worth tracking rather than
// just alerting on.
const defaultHotspotMinTTI = 1.96

type hotspotAgg struct {
	description, regionName              string
	scanCount, congestedCount            int
	jfSum                                float64
	worstTTI                             float64
	worstLOS                             string
	lengthM                              float64
	firstLat, firstLng, lastLat, lastLng float64
	latestScannedAt, lastCongestedAt     time.Time
}

// computeHotspots is tti_suggest_api.go's handleTtiSuggestHotspots's own
// implementation, split out for direct unit testing. from/to are
// mandatory and range-checked by validateHistoryRange (flow_capture_
// history.go) — the same cap the flowcapture page's history
// endpoints use, since this reads the exact same ever-growing
// flow_segments table. Different [from, to] windows surface different
// hotspots (a road jammed in last month's data may be clear this month,
// or vice versa), so the caller always picks the window explicitly;
// there is no implicit "last N days" fallback. Zero/negative
// minTTI/minScans/minRate/limit fall back to sensible defaults.
func computeHotspots(from, to time.Time, minTTI float64, minScans int, minRate float64, limit int) (*HotspotReport, error) {
	if err := validateHistoryRange(from, to); err != nil {
		return nil, err
	}
	if minTTI <= 0 {
		minTTI = defaultHotspotMinTTI
	}
	if minScans <= 0 {
		minScans = 3
	}
	if minRate <= 0 {
		minRate = 0.2
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	report := &HotspotReport{Hotspots: []HotspotSuggestion{}}
	if flowCaptureDB == nil {
		return report, nil
	}

	dq, err := computeHotspotDataQuality(from, to)
	if err != nil {
		return nil, err
	}
	report.DataQuality = dq

	fromStr, toStr := from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339)
	rows, err := flowCaptureDB.Query(`
		SELECT fsc.region_name, fsc.scanned_at, fs.description, fs.length_m,
		       fs.jam_factor, fs.tti_fused, fs.los_label,
		       fs.first_lat, fs.first_lng, fs.last_lat, fs.last_lng
		FROM flow_segments fs
		JOIN flow_scans fsc ON fsc.id = fs.scan_id
		WHERE fsc.scanned_at >= ? AND fsc.scanned_at <= ? AND fs.sub_index = -1
		  AND fs.first_lat IS NOT NULL AND fs.last_lat IS NOT NULL
	`, fromStr, toStr)
	if err != nil {
		return nil, fmt.Errorf("query flow_segments: %w", err)
	}
	defer rows.Close()

	groups := map[string]*hotspotAgg{}

	for rows.Next() {
		var regionName, scannedAtStr, description, losLabel string
		var lengthM, jamFactor, ttiFused, firstLat, firstLng, lastLat, lastLng float64
		if err := rows.Scan(&regionName, &scannedAtStr, &description, &lengthM, &jamFactor, &ttiFused, &losLabel,
			&firstLat, &firstLng, &lastLat, &lastLng); err != nil {
			return nil, fmt.Errorf("scan flow_segments row: %w", err)
		}
		scannedAt, err := time.Parse(time.RFC3339, scannedAtStr)
		if err != nil {
			continue // malformed row — skip it rather than fail the whole report
		}

		key := hotspotGroupKey(regionName, description, firstLat, firstLng)
		g := groups[key]
		if g == nil {
			g = &hotspotAgg{description: description, regionName: regionName}
			groups[key] = g
		}
		g.scanCount++
		g.jfSum += jamFactor
		if ttiFused >= minTTI {
			g.congestedCount++
			if scannedAt.After(g.lastCongestedAt) {
				g.lastCongestedAt = scannedAt
			}
		}
		if ttiFused > g.worstTTI {
			g.worstTTI = ttiFused
			g.worstLOS = losLabel
		}
		// Representative geometry/length always comes from the most
		// recently seen scan for this group, so a road the provider has
		// since re-described (renamed, slightly reshaped) suggests its
		// current shape, not a stale one from early in the window.
		if g.latestScannedAt.IsZero() || scannedAt.After(g.latestScannedAt) {
			g.latestScannedAt = scannedAt
			g.lengthM = lengthM
			g.firstLat, g.firstLng, g.lastLat, g.lastLng = firstLat, firstLng, lastLat, lastLng
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate flow_segments rows: %w", err)
	}

	existingRoutes := traveltime.ListRoutes()

	out := make([]HotspotSuggestion, 0, len(groups))
	for key, g := range groups {
		if g.scanCount < minScans {
			continue
		}
		rate := float64(g.congestedCount) / float64(g.scanCount)
		if rate < minRate {
			continue
		}
		covered, coveredBy := hotspotCoveredByRoute(g.firstLat, g.firstLng, g.lastLat, g.lastLng, existingRoutes)
		lastSeen := ""
		if !g.lastCongestedAt.IsZero() {
			lastSeen = g.lastCongestedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, HotspotSuggestion{
			Key:            key,
			Description:    g.description,
			RegionName:     g.regionName,
			ScanCount:      g.scanCount,
			CongestedCount: g.congestedCount,
			CongestionRate: round2(rate),
			AvgJamFactor:   round2(g.jfSum / float64(g.scanCount)),
			WorstTTI:       round2(g.worstTTI),
			WorstLOSLabel:  g.worstLOS,
			LengthM:        math.Round(g.lengthM),
			FirstLat:       g.firstLat,
			FirstLng:       g.firstLng,
			LastLat:        g.lastLat,
			LastLng:        g.lastLng,
			LastSeenAt:     lastSeen,
			Covered:        covered,
			CoveredByRoute: coveredBy,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].CongestionRate != out[j].CongestionRate {
			return out[i].CongestionRate > out[j].CongestionRate
		}
		return out[i].CongestedCount > out[j].CongestedCount
	})
	if len(out) > limit {
		out = out[:limit]
	}
	report.Hotspots = out
	return report, nil
}

// computeHotspotDataQuality answers "trong khoảng ngày đã chọn, dữ liệu
// flow_segments đã lưu đầy đủ chưa — có hình học (link), jf, speed
// không?" — a cheap pair of COUNT(*) queries (not the full row scan
// computeHotspots itself does) over the same [from, to] window, so a
// caller can tell a genuinely-quiet road apart from a date range whose
// capture data was simply incomplete.
func computeHotspotDataQuality(from, to time.Time) (HotspotDataQuality, error) {
	fromStr, toStr := from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339)
	var dq HotspotDataQuality
	err := flowCaptureDB.QueryRow(`
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN fs.first_lat IS NOT NULL AND fs.last_lat IS NOT NULL THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN fs.speed_mps = 0 AND fs.free_flow_mps = 0 THEN 1 ELSE 0 END), 0)
		FROM flow_segments fs
		JOIN flow_scans fsc ON fsc.id = fs.scan_id
		WHERE fsc.scanned_at >= ? AND fsc.scanned_at <= ? AND fs.sub_index = -1
	`, fromStr, toStr).Scan(&dq.ScansExamined, &dq.WithGeometry, &dq.ZeroSpeedCount)
	if err != nil {
		return dq, fmt.Errorf("query data quality: %w", err)
	}
	if dq.ScansExamined > 0 {
		dq.GeometryRate = round2(float64(dq.WithGeometry) / float64(dq.ScansExamined))
	}
	return dq, nil
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// hotspotGroupKey groups by (region, street name) when the provider named
// the road; an unnamed result instead groups by a coarse ~0.001°
// (~100m) grid cell of its own start point, so unrelated unnamed segments
// elsewhere in the same region don't collapse into one bucket.
func hotspotGroupKey(regionName, description string, lat, lng float64) string {
	if description != "" {
		return regionName + "|" + description
	}
	return fmt.Sprintf("%s|@%.3f,%.3f", regionName, lat, lng)
}

// hotspotCoveredByRoute reports whether an existing internal/traveltime
// Route already runs through this candidate: BOTH of the candidate's own
// endpoints must lie within defaultDedupRadiusM of some point (origin,
// destination, or any waypoint) of the SAME route. Requiring both
// endpoints — not just one — avoids flagging "covered" when a candidate
// merely touches one end of an unrelated route that happens to start
// nearby.
func hotspotCoveredByRoute(firstLat, firstLng, lastLat, lastLng float64, routes []*traveltime.Route) (bool, string) {
	for _, r := range routes {
		nearFirst, nearLast := false, false
		for _, p := range routePoints(r) {
			if haversine(firstLat, firstLng, p[0], p[1]) <= defaultDedupRadiusM {
				nearFirst = true
			}
			if haversine(lastLat, lastLng, p[0], p[1]) <= defaultDedupRadiusM {
				nearLast = true
			}
		}
		if nearFirst && nearLast {
			return true, r.Name
		}
	}
	return false, ""
}

func routePoints(r *traveltime.Route) [][2]float64 {
	var pts [][2]float64
	if lat, lng, ok := parseLatLng(r.Origin); ok {
		pts = append(pts, [2]float64{lat, lng})
	}
	if lat, lng, ok := parseLatLng(r.Destination); ok {
		pts = append(pts, [2]float64{lat, lng})
	}
	if r.Waypoints != "" {
		for _, wp := range strings.Split(r.Waypoints, "|") {
			if lat, lng, ok := parseLatLng(wp); ok {
				pts = append(pts, [2]float64{lat, lng})
			}
		}
	}
	return pts
}

func parseLatLng(s string) (lat, lng float64, ok bool) {
	parts := strings.SplitN(strings.TrimSpace(s), ",", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	lat, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	lng, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return lat, lng, true
}

// HotspotTimelineSlot is one 30-minute time-of-day bucket (0-47, Vietnam
// local time) — same slot width www/heatmap-replay.html's per-day
// histogram uses, just summed across every day in the queried range
// instead of split per calendar day: "7h-7h30 cộng dồn qua nhiều ngày".
type HotspotTimelineSlot struct {
	Slot  int    `json:"slot"` // 0-47
	Label string `json:"label"`
	Count int    `json:"count"` // số lần đạt mức ùn (TTI >= minTTI) trong khung giờ này, cộng dồn qua mọi ngày trong [from,to]
}

// HotspotTimeline is computeHotspotTimeline's full result: always exactly
// 48 slots (zero-filled where there's no congested reading), index ==
// slot.
type HotspotTimeline struct {
	RegionName  string                `json:"regionName"`
	Description string                `json:"description"`
	From        string                `json:"from"`
	To          string                `json:"to"`
	MinTTI      float64               `json:"minTti"`
	Slots       []HotspotTimelineSlot `json:"slots"`
}

func hotspotTimelineSlotLabel(slot int) string {
	h, m := slot/2, (slot%2)*30
	h2, m2 := h, m+30
	if m2 == 60 {
		h2, m2 = h+1, 0
	}
	return fmt.Sprintf("%02d:%02d-%02d:%02d", h, m, h2%24, m2)
}

// computeHotspotTimeline answers "với MỘT tuyến/đoạn đường cụ thể (click
// từ bảng điểm nóng), nó hay ùn vào khung giờ nào trong ngày?" — same
// from/to window and minTTI threshold already chosen on the page's own
// filter (not a separate "last N days"), bucketed every 30 minutes and
// summed across every day the range covers.
func computeHotspotTimeline(region, description string, from, to time.Time, minTTI float64) (*HotspotTimeline, error) {
	if err := validateHistoryRange(from, to); err != nil {
		return nil, err
	}
	if description == "" {
		return nil, fmt.Errorf("description is required")
	}
	if minTTI <= 0 {
		minTTI = defaultHotspotMinTTI
	}

	tl := &HotspotTimeline{
		RegionName: region, Description: description,
		From: from.UTC().Format(time.RFC3339), To: to.UTC().Format(time.RFC3339),
		MinTTI: minTTI, Slots: make([]HotspotTimelineSlot, 48),
	}
	for i := range tl.Slots {
		tl.Slots[i] = HotspotTimelineSlot{Slot: i, Label: hotspotTimelineSlotLabel(i)}
	}
	if flowCaptureDB == nil {
		return tl, nil
	}

	rows, err := flowCaptureDB.Query(`
		SELECT fsc.scanned_at, fs.tti_fused
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
		var scannedAtStr string
		var ttiFused float64
		if err := rows.Scan(&scannedAtStr, &ttiFused); err != nil {
			return nil, fmt.Errorf("scan timeline row: %w", err)
		}
		if ttiFused < minTTI {
			continue
		}
		scannedAt, err := time.Parse(time.RFC3339, scannedAtStr)
		if err != nil {
			continue // malformed row — skip rather than fail the whole report
		}
		local := scannedAt.In(vnLocation)
		slot := local.Hour()*2 + local.Minute()/30
		tl.Slots[slot].Count++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate timeline rows: %w", err)
	}
	return tl, nil
}
