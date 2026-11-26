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

	"github.com/AlexxIT/go2rtc/internal/trafficmetrics"
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
// above, alongside the ranked suggestions themselves. TotalMatched is how
// many groups passed minScans BEFORE the limit cutoff below trimmed the
// list — "chọn khung thời gian dài/ngắn vẫn chỉ ra 50 điểm nóng dù rõ
// ràng có nhiều hơn": Hotspots is always capped at limit, but
// TotalMatched lets the caller tell "that's really all of them" apart
// from "there are more, raise limit to see them".
type HotspotReport struct {
	DataQuality  HotspotDataQuality  `json:"dataQuality"`
	TotalMatched int                 `json:"totalMatched"`
	Hotspots     []HotspotSuggestion `json:"hotspots"`
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
	// Geometry is the representative scan's own real road polyline
	// ([lat,lng] pairs, in order), reconstructed from flow_scans.raw_json
	// (flow_capture_parse.go's Links, flattened) rather than synthesized
	// as a straight line between FirstLat/Lng and LastLat/Lng — "biểu
	// diễn trên map theo đoạn tuyến đúng nguyên văn dữ liệu raw, không
	// tách ra thành từng điểm rồi nối lại". Omitted (empty) when the
	// representative scan's raw_json can no longer be read/parsed, or
	// didn't carry a matching result's geometry — callers fall back to
	// the straight FirstLat/Lng-LastLat/Lng line in that case, same as
	// before this field existed.
	Geometry [][2]float64 `json:"geometry,omitempty"`
	// WorstTTILowSpeedWarning flags that the scan which produced WorstTTI
	// had its own speed_mps at or below trafficmetrics.MinSpeedMps — "TTi
	// cao nhất hiện nay đang khá lớn ... kiểm tra xem đây là số cộng dồn
	// qua các lần lấy hay sao?": it is NOT a cumulative/summed figure
	// (WorstTTI is tracked as a plain per-scan max, see the loop below),
	// but CalculateTTIFromSpeed floors a near-zero/zero speed reading to
	// MinSpeedMps before dividing, so once a reading's own speed is at or
	// under that floor, the resulting TTI is driven almost entirely by
	// FreeFlowMps/MinSpeedMps rather than a precisely measured ratio — a
	// true standstill (real gridlock) and a noisy/stale GPS sample
	// reporting near-zero speed while traffic is merely slow both floor to
	// the same inflated number. This is a hint to double-check the
	// underlying reading, not proof of a bad one — flagged here rather
	// than silently producing a large WorstTTI with no way to tell why.
	WorstTTILowSpeedWarning bool `json:"worstTtiLowSpeedWarning,omitempty"`
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

// defaultGridRadiusM is hotspotGroupKey's own grid-cell size for an
// unnamed road (no description to group by instead) when the caller
// doesn't specify one — admin-chosen per request (www/tti-suggest.html's
// "Bán kính gộp" select), not a fixed constant baked into the grouping
// itself: a bigger radius merges more scans into one candidate (fewer,
// coarser hotspots) and a smaller one keeps them apart (more, finer
// hotspots) — there's no universally-correct size for every deployment's
// own road density.
const defaultGridRadiusM = 100.0

// defaultHotspotLimit/maxHotspotLimit bound how many ranked suggestions
// computeHotspots returns. A caller that sends no limit (or an invalid
// one, <= 0) gets defaultHotspotLimit; an explicit limit above
// maxHotspotLimit is clamped DOWN to maxHotspotLimit, not reset to the
// default — unlike an earlier version of this clamp, which reset ANY
// out-of-range value (including an intentional "give me 500") straight
// back to 50, so a caller could never actually see more than the
// default regardless of how explicitly it asked.
const (
	defaultHotspotLimit = 50
	maxHotspotLimit     = 2000
)

type hotspotAgg struct {
	description, regionName              string
	scanCount, congestedCount            int
	jfSum                                float64
	worstTTI                             float64
	worstLOS                             string
	worstTTISpeedMps                     float64 // the speed_mps that produced worstTTI — see HotspotSuggestion.WorstTTILowSpeedWarning
	lengthM                              float64
	firstLat, firstLng, lastLat, lastLng float64
	latestScannedAt, lastCongestedAt     time.Time
	scanID                               int64 // flow_scans.id the representative (latest) row came from — see enrichHotspotGeometry
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
// minTTI/minScans/gridRadiusM/limit fall back to sensible defaults.
// There is deliberately no minimum-congestion-rate filter — every group
// that clears minScans is a candidate; CongestionRate is only ever a
// ranking signal (see the sort below), never an exclusion one.
//
// wardIDs (ward_boundary.go) restricts candidates to rows whose own
// first_lat/first_lng falls inside one of those wards — nil/empty means
// no boundary restriction at all (www/tti-suggest.html's "Không giới
// hạn"); passing EVERY currently-imported ward ID is how that same page
// expresses "Toàn TP.HCM (168 đơn vị)" instead, which is NOT a no-op: it
// still excludes a scan whose point falls outside every imported ward
// (e.g. a Flow Capture region configured to extend past the city's own
// administrative boundary).
//
// A group only becomes a candidate at all once congestedCount >=
// minCongestedCount — i.e. it must have hit minTTI at least that many
// times somewhere in [from, to]. "Chọn mức ùn tối thiểu > F nhưng
// bảng/bản đồ vẫn vẽ điểm E/C/D": a road whose own worst-ever TTI never
// reached the chosen threshold has no business appearing as a
// "hotspot" for that threshold at all, even though it still clears
// minScans — minCongestedCount <= 0 (or omitted) falls back to 1, i.e.
// "reached it at least once", which is the minimum this check can ever
// enforce. This is NOT the min-congestion-RATE filter deliberately left
// out below (CongestionRate stays a sort key, never an exclusion one):
// minCongestedCount is a raw count ("số lần xuất hiện tối thiểu" —
// exactly HotspotSuggestion.CongestedCount, the same number the table
// itself shows), so a road reaching minTTI in 1 scan out of 500 still
// qualifies at minCongestedCount=1, but is excluded once the caller
// raises that bar past 1 to focus on roads that keep recurring rather
// than a one-off reading. Once congestedCount >= 1 holds at all,
// WorstTTI/WorstLOSLabel (the group's own all-time max, tracked below
// regardless of minTTI) is guaranteed to already be >= minTTI — so a
// surviving group's displayed worst LOS can never read below whatever
// LOS band the chosen minTTI corresponds to, independently of how high
// minCongestedCount itself is set.
func computeHotspots(from, to time.Time, minTTI float64, minScans, minCongestedCount int, gridRadiusM float64, limit int, wardIDs []string) (*HotspotReport, error) {
	if err := validateHistoryRange(from, to); err != nil {
		return nil, err
	}
	if minTTI <= 0 {
		minTTI = defaultHotspotMinTTI
	}
	if minScans <= 0 {
		minScans = 3
	}
	if minCongestedCount <= 0 {
		minCongestedCount = 1
	}
	if gridRadiusM <= 0 {
		gridRadiusM = defaultGridRadiusM
	}
	if limit <= 0 {
		limit = defaultHotspotLimit
	} else if limit > maxHotspotLimit {
		limit = maxHotspotLimit
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

	selectedWards := wardsByIDs(wardIDs)

	fromStr, toStr := from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339)
	rows, err := flowCaptureDB.Query(`
		SELECT fsc.region_name, fsc.scanned_at, fs.description, fs.length_m,
		       fs.jam_factor, fs.tti_fused, fs.los_label, fs.speed_mps,
		       fs.first_lat, fs.first_lng, fs.last_lat, fs.last_lng, fs.scan_id
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
		var lengthM, jamFactor, ttiFused, speedMps, firstLat, firstLng, lastLat, lastLng float64
		var scanID int64
		if err := rows.Scan(&regionName, &scannedAtStr, &description, &lengthM, &jamFactor, &ttiFused, &losLabel, &speedMps,
			&firstLat, &firstLng, &lastLat, &lastLng, &scanID); err != nil {
			return nil, fmt.Errorf("scan flow_segments row: %w", err)
		}
		scannedAt, err := time.Parse(time.RFC3339, scannedAtStr)
		if err != nil {
			continue // malformed row — skip it rather than fail the whole report
		}
		if !pointInWards(firstLat, firstLng, selectedWards) {
			continue // outside the chosen administrative-boundary filter
		}

		key := hotspotGroupKey(regionName, description, firstLat, firstLng, gridRadiusM)
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
			g.worstTTISpeedMps = speedMps
		}
		// Representative geometry/length always comes from the most
		// recently seen scan for this group, so a road the provider has
		// since re-described (renamed, slightly reshaped) suggests its
		// current shape, not a stale one from early in the window.
		if g.latestScannedAt.IsZero() || scannedAt.After(g.latestScannedAt) {
			g.latestScannedAt = scannedAt
			g.lengthM = lengthM
			g.firstLat, g.firstLng, g.lastLat, g.lastLng = firstLat, firstLng, lastLat, lastLng
			g.scanID = scanID
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate flow_segments rows: %w", err)
	}

	existingRoutes := traveltime.ListRoutes()

	out := make([]HotspotSuggestion, 0, len(groups))
	scanIDByKey := make(map[string]int64, len(groups))
	for key, g := range groups {
		if g.scanCount < minScans {
			continue
		}
		if g.congestedCount < minCongestedCount {
			continue // didn't reach minTTI often enough — see computeHotspots' own doc comment
		}
		rate := float64(g.congestedCount) / float64(g.scanCount)
		covered, coveredBy := hotspotCoveredByRoute(g.firstLat, g.firstLng, g.lastLat, g.lastLng, existingRoutes)
		lastSeen := ""
		if !g.lastCongestedAt.IsZero() {
			lastSeen = g.lastCongestedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, HotspotSuggestion{
			Key:                     key,
			Description:             g.description,
			RegionName:              g.regionName,
			ScanCount:               g.scanCount,
			CongestedCount:          g.congestedCount,
			CongestionRate:          round2(rate),
			AvgJamFactor:            round2(g.jfSum / float64(g.scanCount)),
			WorstTTI:                round2(g.worstTTI),
			WorstLOSLabel:           g.worstLOS,
			LengthM:                 math.Round(g.lengthM),
			FirstLat:                g.firstLat,
			FirstLng:                g.firstLng,
			LastLat:                 g.lastLat,
			LastLng:                 g.lastLng,
			LastSeenAt:              lastSeen,
			Covered:                 covered,
			CoveredByRoute:          coveredBy,
			WorstTTILowSpeedWarning: g.worstTTISpeedMps <= trafficmetrics.MinSpeedMps,
		})
		scanIDByKey[key] = g.scanID
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].CongestionRate != out[j].CongestionRate {
			return out[i].CongestionRate > out[j].CongestionRate
		}
		return out[i].CongestedCount > out[j].CongestedCount
	})
	report.TotalMatched = len(out)
	if len(out) > limit {
		out = out[:limit]
	}
	// Real road geometry only ever gets fetched/parsed for the final,
	// post-limit list — never for every match before truncation — so
	// raising "Số lượng hiển thị tối đa" is the only thing that scales
	// this cost up.
	enrichHotspotGeometry(out, scanIDByKey)
	report.Hotspots = out
	return report, nil
}

// enrichHotspotGeometry fills in each hotspot's Geometry (its real road
// polyline) by re-parsing its representative scan's raw_json — see
// HotspotSuggestion.Geometry's own doc comment for why this beats a
// straight FirstLat/Lng-LastLat/Lng line. Caches one parsed result set
// per scan_id, so hotspots sharing a scan (the common case — most
// candidates from one region share that region's single latest scan in
// the window) only pay the raw_json fetch+parse cost once.
func enrichHotspotGeometry(out []HotspotSuggestion, scanIDByKey map[string]int64) {
	parsedByScan := map[int64][]flowResult{}
	for i := range out {
		scanID, ok := scanIDByKey[out[i].Key]
		if !ok {
			continue
		}
		results, cached := parsedByScan[scanID]
		if !cached {
			raw, err := rawFlowScanJSON(scanID)
			if err != nil {
				parsedByScan[scanID] = nil
				continue
			}
			parsed, _, err := parseFlowResults([]byte(raw))
			if err != nil {
				parsedByScan[scanID] = nil
				continue
			}
			results = parsed
			parsedByScan[scanID] = results
		}
		if results == nil {
			continue
		}
		if match := findMatchingFlowResult(results, out[i].Description, out[i].FirstLat, out[i].FirstLng); match != nil {
			out[i].Geometry = flattenFlowResultPoints(match)
		}
	}
}

// findMatchingFlowResult picks the one result (within one scan's parsed
// raw_json) that produced a given hotspot's representative row. A named
// road matches by its own description (unique enough within one scan in
// practice — the provider doesn't return the same street name twice for
// one bbox query); an unnamed one has no such key, so it matches by
// whichever result's own first point is closest to the hotspot's own
// FirstLat/Lng — the EXACT point that row's first_lat/first_lng came
// from in the first place, so this is an exact (or near-exact, modulo
// float round-tripping) match, not a fuzzy guess.
func findMatchingFlowResult(results []flowResult, description string, firstLat, firstLng float64) *flowResult {
	if description != "" {
		for i := range results {
			if results[i].Description == description {
				return &results[i]
			}
		}
		return nil
	}
	const matchToleranceM = 5.0
	var best *flowResult
	bestDist := math.MaxFloat64
	for i := range results {
		r := &results[i]
		if len(r.Links) == 0 || len(r.Links[0].Points) == 0 {
			continue
		}
		p := r.Links[0].Points[0]
		if d := haversine(firstLat, firstLng, p[0], p[1]); d < bestDist {
			bestDist = d
			best = r
		}
	}
	if best != nil && bestDist <= matchToleranceM {
		return best
	}
	return nil
}

// flattenFlowResultPoints concatenates every link's own polyline, in
// order, into one continuous [lat,lng] path for the whole result.
func flattenFlowResultPoints(r *flowResult) [][2]float64 {
	var pts [][2]float64
	for _, link := range r.Links {
		pts = append(pts, link.Points...)
	}
	return pts
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

// earthRadiusM backs metersPerDegreeLat/metersPerDegreeLng below —
// WGS-84's mean radius, same value monitor.go's own haversine uses.
const earthRadiusM = 6371000.0

// metersPerDegreeLat is constant everywhere on the sphere (lines of
// latitude are evenly spaced); metersPerDegreeLng shrinks toward the
// poles (lines of longitude converge), so it depends on the point's own
// latitude.
func metersPerDegreeLat() float64 { return earthRadiusM * math.Pi / 180 }
func metersPerDegreeLng(latDeg float64) float64 {
	return earthRadiusM * math.Pi / 180 * math.Cos(latDeg*math.Pi/180)
}

// hotspotGroupKey groups by (region, street name) when the provider named
// the road; an unnamed result instead groups by a grid cell of its own
// start point, sized so each cell is gridRadiusM wide — so unrelated
// unnamed segments elsewhere in the same region don't collapse into one
// bucket. Snaps lat/lng to the cell's own floor boundary (not just
// rounded to N decimal places) so gridRadiusM can be ANY size the caller
// picks, not only the handful of sizes a fixed decimal-places format
// string happens to land on.
func hotspotGroupKey(regionName, description string, lat, lng, gridRadiusM float64) string {
	if description != "" {
		return regionName + "|" + description
	}
	if gridRadiusM <= 0 {
		gridRadiusM = defaultGridRadiusM
	}
	cellSizeLat := gridRadiusM / metersPerDegreeLat()
	cellSizeLng := gridRadiusM / metersPerDegreeLng(lat)
	cellLat := math.Floor(lat/cellSizeLat) * cellSizeLat
	cellLng := math.Floor(lng/cellSizeLng) * cellSizeLng
	return fmt.Sprintf("%s|@%.6f,%.6f", regionName, cellLat, cellLng)
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

// weekdayNamesVI mirrors www/heatmap-replay.html's own WEEKDAY_NAMES_VI
// exactly (index == time.Weekday(), 0 = Sunday) — computed server-side
// here instead of asking the client to redo it from a date string.
var weekdayNamesVI = [...]string{"Chủ Nhật", "Thứ Hai", "Thứ Ba", "Thứ Tư", "Thứ Năm", "Thứ Sáu", "Thứ Bảy"}

// HotspotTimelineDayCount is one calendar day's own count within a single
// 30-minute slot — "nếu click vào từng khung giờ sẽ thể hiện được chi
// tiết: khung giờ đó, từng ngày - thứ... bao nhiêu lần". Only days that
// actually had >= 1 occurrence in that slot are included (a 62-day range
// × 48 slots full of mostly-zero entries would be a lot of dead weight
// for no benefit).
type HotspotTimelineDayCount struct {
	Date    string `json:"date"`    // "2026-11-22", Vietnam local calendar day
	Weekday string `json:"weekday"` // "Chủ Nhật"
	Count   int    `json:"count"`
}

// HotspotTimelineSlot is one 30-minute time-of-day bucket (0-47, Vietnam
// local time) — same slot width www/heatmap-replay.html's per-day
// histogram uses, just summed across every day in the queried range
// instead of split per calendar day: "7h-7h30 cộng dồn qua nhiều ngày".
// ByDay is that same total broken back down by day for display once a
// viewer clicks into one particular slot.
type HotspotTimelineSlot struct {
	Slot  int                       `json:"slot"` // 0-47
	Label string                    `json:"label"`
	Count int                       `json:"count"`           // số lần đạt mức ùn (TTI >= minTTI) trong khung giờ này, cộng dồn qua mọi ngày trong [from,to]
	ByDay []HotspotTimelineDayCount `json:"byDay,omitempty"` // sorted by Date ascending; only days with Count > 0
}

// HotspotTimeline is computeHotspotTimeline's full result: always exactly
// 48 slots (zero-filled where there's no congested reading), index ==
// slot.
type HotspotTimeline struct {
	Key    string                `json:"key"`
	From   string                `json:"from"`
	To     string                `json:"to"`
	MinTTI float64               `json:"minTti"`
	Slots  []HotspotTimelineSlot `json:"slots"`
}

func hotspotTimelineSlotLabel(slot int) string {
	h, m := slot/2, (slot%2)*30
	h2, m2 := h, m+30
	if m2 == 60 {
		h2, m2 = h+1, 0
	}
	return fmt.Sprintf("%02d:%02d-%02d:%02d", h, m, h2%24, m2)
}

// computeHotspotTimeline answers "với MỘT điểm nóng cụ thể (click từ
// bảng điểm nóng), nó hay ùn vào khung giờ nào trong ngày?" — same
// from/to window and minTTI threshold already chosen on the page's own
// filter (not a separate "last N days"), bucketed every 30 minutes and
// summed across every day the range covers.
//
// key is exactly one HotspotSuggestion.Key from computeHotspots' own
// output (region+description, or region+grid-cell for an unnamed road —
// see hotspotGroupKey). Matching rows to key here RECOMPUTES
// hotspotGroupKey per row instead of filtering by description directly,
// so a grid-cell-grouped (unnamed) hotspot gets its own timeline too, not
// only named roads: a flat `description = ?` filter can't express "this
// row's own lat/lng lands in the same grid cell" — only
// hotspotGroupKey's own logic can. region narrows the SQL query first
// (every candidate row must be in this region anyway, per how
// hotspotGroupKey built the key) before that per-row recomputation runs.
// gridRadiusM MUST be the same value the caller used to generate key via
// computeHotspots in the first place — a different radius recomputes
// different cell boundaries and key would simply never match any row.
func computeHotspotTimeline(region, key string, from, to time.Time, minTTI, gridRadiusM float64) (*HotspotTimeline, error) {
	if err := validateHistoryRange(from, to); err != nil {
		return nil, err
	}
	if key == "" {
		return nil, fmt.Errorf("key is required")
	}
	if minTTI <= 0 {
		minTTI = defaultHotspotMinTTI
	}
	if gridRadiusM <= 0 {
		gridRadiusM = defaultGridRadiusM
	}

	tl := &HotspotTimeline{
		Key:    key,
		From:   from.UTC().Format(time.RFC3339),
		To:     to.UTC().Format(time.RFC3339),
		MinTTI: minTTI, Slots: make([]HotspotTimelineSlot, 48),
	}
	for i := range tl.Slots {
		tl.Slots[i] = HotspotTimelineSlot{Slot: i, Label: hotspotTimelineSlotLabel(i)}
	}
	if flowCaptureDB == nil {
		return tl, nil
	}

	rows, err := flowCaptureDB.Query(`
		SELECT fsc.scanned_at, fs.description, fs.first_lat, fs.first_lng, fs.tti_fused
		FROM flow_segments fs
		JOIN flow_scans fsc ON fsc.id = fs.scan_id
		WHERE fsc.scanned_at >= ? AND fsc.scanned_at <= ? AND fs.sub_index = -1
		  AND fsc.region_name = ? AND fs.first_lat IS NOT NULL AND fs.last_lat IS NOT NULL
	`, from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339), region)
	if err != nil {
		return nil, fmt.Errorf("query flow_segments: %w", err)
	}
	defer rows.Close()

	// dayCounts[slot][dateKey] accumulates the per-day breakdown
	// alongside tl.Slots[slot].Count itself — built into HotspotTimelineSlot.ByDay
	// once every row has been seen (so each slot's days end up sorted,
	// rather than sorting 48 times inside the loop).
	dayCounts := make([]map[string]int, 48)

	for rows.Next() {
		var scannedAtStr, description string
		var firstLat, firstLng, ttiFused float64
		if err := rows.Scan(&scannedAtStr, &description, &firstLat, &firstLng, &ttiFused); err != nil {
			return nil, fmt.Errorf("scan timeline row: %w", err)
		}
		if hotspotGroupKey(region, description, firstLat, firstLng, gridRadiusM) != key {
			continue
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
		if dayCounts[slot] == nil {
			dayCounts[slot] = map[string]int{}
		}
		dayCounts[slot][local.Format("2006-01-02")]++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate timeline rows: %w", err)
	}

	for slot, counts := range dayCounts {
		if len(counts) == 0 {
			continue
		}
		dates := make([]string, 0, len(counts))
		for d := range counts {
			dates = append(dates, d)
		}
		sort.Strings(dates)
		byDay := make([]HotspotTimelineDayCount, len(dates))
		for i, d := range dates {
			t, err := time.ParseInLocation("2006-01-02", d, vnLocation)
			weekday := ""
			if err == nil {
				weekday = weekdayNamesVI[t.Weekday()]
			}
			byDay[i] = HotspotTimelineDayCount{Date: d, Weekday: weekday, Count: counts[d]}
		}
		tl.Slots[slot].ByDay = byDay
	}

	return tl, nil
}
