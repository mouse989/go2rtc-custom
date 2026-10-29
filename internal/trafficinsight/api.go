package trafficinsight

// api.go — read-only HTTP surface for the Traffic Insight trend page
// (www/trafficinsight.html). Every handler here only accepts GET and only
// requires an authenticated caller — same convention
// internal/dashboard/*.go's handlers use — because the actual page-level
// restriction is TabTrafficInsight's entry in
// internal/auth/middleware.go's tabExtraPaths, checked before the request
// ever reaches here.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/AlexxIT/go2rtc/internal/auth"
	"github.com/AlexxIT/go2rtc/internal/traffic"
	"github.com/AlexxIT/go2rtc/internal/traveltime"
)

func Init() {
	http.HandleFunc("/api/trafficinsight/today", handleToday)
	http.HandleFunc("/api/trafficinsight/averages", handleAverages)
	http.HandleFunc("/api/trafficinsight/daily-series", handleDailySeries)
	http.HandleFunc("/api/trafficinsight/hourly-series", handleHourlySeries)
	http.HandleFunc("/api/trafficinsight/weekday-matrix", handleWeekdayMatrix)
	http.HandleFunc("/api/trafficinsight/jam-points", handleJamPoints)
	http.HandleFunc("/api/trafficinsight/incident-points", handleIncidentPoints)
	http.HandleFunc("/api/trafficinsight/incident-category-icons", handleIncidentCategoryIcons)
	http.HandleFunc("/api/trafficinsight/incident-category-icons/image", handleIncidentCategoryIconImage)
	http.HandleFunc("/api/trafficinsight/incident-stats", handleIncidentStats)
	http.HandleFunc("/api/trafficinsight/incident-time-slots", handleIncidentTimeSlots)
	http.HandleFunc("/api/trafficinsight/route-tti", handleRouteTTI)
	http.HandleFunc("/api/trafficinsight/route-filters", handleRouteFilters)
}

// parseRouteFilter reads ?region=&roadType= into a RouteFilter — either
// or both may be omitted (empty = no constraint on that dimension, see
// RouteFilter.Empty). Used by every TTI-related endpoint below except
// /today and /averages, which intentionally stay unfiltered (they back
// the "Chỉ số giao thông hôm nay" strip and "So sánh: hôm nay — TB7 —
// TB30" card, not one of the four modules this filter was added for).
func parseRouteFilter(r *http.Request) RouteFilter {
	return RouteFilter{
		Region:   r.URL.Query().Get("region"),
		RoadType: r.URL.Query().Get("roadType"),
	}
}

type routeFiltersResponse struct {
	Regions   []string `json:"regions"`
	RoadTypes []string `json:"roadTypes"`
}

// GET /api/trafficinsight/route-filters — the Khu vực/Loại đường dropdown
// options for every TTI filter control on the page. A thin passthrough
// onto traveltime.ListRegions()/RoadTypes, same reasoning as this
// package's other passthroughs (e.g. handleIncidentCategoryIcons): the
// real traveltime.Init()-registered endpoints are admin-gated and would
// 403 a TabTrafficInsight-only viewer.
func handleRouteFilters(w http.ResponseWriter, r *http.Request) {
	if !requireViewer(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, routeFiltersResponse{Regions: traveltime.ListRegions(), RoadTypes: traveltime.RoadTypes})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func requireViewer(w http.ResponseWriter, r *http.Request) bool {
	_, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

// GET /api/trafficinsight/today
func handleToday(w http.ResponseWriter, r *http.Request) {
	if !requireViewer(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snap, err := GetTodaySnapshot()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, snap)
}

type averagesResponse struct {
	Today DailyMetrics     `json:"today"`
	Avg7  AggregateMetrics `json:"avg7"`  // time-matched (cutoff = now) — fair vs. today-so-far
	Avg30 AggregateMetrics `json:"avg30"` // time-matched (cutoff = now)
	// Avg7Full/Avg30Full are the same two date ranges (today excluded)
	// but NOT truncated — each day's own full 24h, for "what does the
	// whole day usually look like" context alongside the time-matched
	// figure above. See handleAverages' doc comment.
	Avg7Full  AggregateMetrics `json:"avg7Full"`
	Avg30Full AggregateMetrics `json:"avg30Full"`
}

// GET /api/trafficinsight/averages — today's own (naturally partial, up
// to now) aggregate plus the trailing 7-day and 30-day baselines, each in
// two forms over the exact same date range (today-7..today-1 /
// today-30..today-1, today itself always excluded — it's the subject
// being compared, not part of what it's compared against):
//
//   - Avg7/Avg30: "time-matched" — each baseline day is itself truncated
//     to today's current wall-clock time (GetRangeAverageUntil) before
//     being averaged, so "today so far" is compared fairly against "what
//     the same time-of-day window looked like on past days" — comparing a
//     partial today against full historical days would always make today
//     look emptier/better than it really is, purely because evening hours
//     haven't happened yet.
//   - Avg7Full/Avg30Full: the same days' ordinary full-24h average
//     (GetRangeAverage, unmodified) — not a fair comparison against a
//     still-in-progress today, but useful as "the whole picture" context
//     (e.g. "today is already at 80% of a typical FULL day's incidents").
func handleAverages(w http.ResponseWriter, r *http.Request) {
	if !requireViewer(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	now := time.Now().In(vnLocation)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, vnLocation)

	todayMetrics, err := GetDailyMetrics(today.Format("2006-01-02"), RouteFilter{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	yesterday := today.AddDate(0, 0, -1)
	avg7, _ := GetRangeAverageUntil(today.AddDate(0, 0, -7), yesterday, now, RouteFilter{})
	avg30, _ := GetRangeAverageUntil(today.AddDate(0, 0, -30), yesterday, now, RouteFilter{})
	avg7Full, _ := GetRangeAverage(today.AddDate(0, 0, -7), yesterday, RouteFilter{})
	avg30Full, _ := GetRangeAverage(today.AddDate(0, 0, -30), yesterday, RouteFilter{})

	writeJSON(w, averagesResponse{Today: todayMetrics, Avg7: avg7, Avg30: avg30, Avg7Full: avg7Full, Avg30Full: avg30Full})
}

// GET /api/trafficinsight/daily-series?days=N (default 30, capped at 90) —
// trailing N days from today. GET .../daily-series?from=YYYY-MM-DD&to=YYYY-MM-DD
// instead returns that explicit range (also capped at 90 days, trimmed
// from the `from` side) — the trend chart's "Từ/Đến" mode. `from`/`to`
// take priority over `days` when both forms are given.
func handleDailySeries(w http.ResponseWriter, r *http.Request) {
	if !requireViewer(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	filter := parseRouteFilter(r)
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from != "" && to != "" {
		series, err := GetDailySeriesRange(from, to, filter)
		if err != nil {
			http.Error(w, "invalid from/to: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, series)
		return
	}

	days := 30
	if s := r.URL.Query().Get("days"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			days = n
		}
	}
	writeJSON(w, GetDailySeries(days, filter))
}

// GET /api/trafficinsight/hourly-series?date=YYYY-MM-DD — the "Xu hướng
// theo giờ" chart's data: one Vietnam-local calendar day sliced into its
// 24 hour-of-day buckets (defaults to today when date is omitted).
func handleHourlySeries(w http.ResponseWriter, r *http.Request) {
	if !requireViewer(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().In(vnLocation).Format("2006-01-02")
	}
	series, err := GetHourlySeries(date, parseRouteFilter(r))
	if err != nil {
		http.Error(w, "invalid date: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, series)
}

type jamPointsResponse struct {
	Date   string              `json:"date"`
	Points []traffic.DashPoint `json:"points"`
}

// GET /api/trafficinsight/jam-points?date=YYYY-MM-DD — every persistent
// jam point recorded on that Vietnam-local calendar day (the map's "xem
// theo ngày" mode); the "hiện tại" mode instead uses /today's live
// points, which is the current in-memory scan result, not history.
func handleJamPoints(w http.ResponseWriter, r *http.Request) {
	if !requireViewer(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	date := r.URL.Query().Get("date")
	if date == "" {
		http.Error(w, "date is required (YYYY-MM-DD)", http.StatusBadRequest)
		return
	}
	if _, err := time.ParseInLocation("2006-01-02", date, vnLocation); err != nil {
		http.Error(w, "invalid date: "+err.Error(), http.StatusBadRequest)
		return
	}
	points, err := traffic.PersistentPointsForDate(date)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, jamPointsResponse{Date: date, Points: points})
}

type incidentPointsResponse struct {
	Date   string          `json:"date"`
	Points []IncidentPoint `json:"points"`
}

// GET /api/trafficinsight/incident-points?date=YYYY-MM-DD — every
// coordinate-tagged incident logged on that Vietnam-local calendar day (the
// map's "Sự cố GT" layer) — same required-date convention as /jam-points
// above, so the frontend passes today's date for "hôm nay" and shares the
// exact same filter the JF layer uses.
func handleIncidentPoints(w http.ResponseWriter, r *http.Request) {
	if !requireViewer(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	date := r.URL.Query().Get("date")
	if date == "" {
		http.Error(w, "date is required (YYYY-MM-DD)", http.StatusBadRequest)
		return
	}
	if _, err := time.ParseInLocation("2006-01-02", date, vnLocation); err != nil {
		http.Error(w, "invalid date: "+err.Error(), http.StatusBadRequest)
		return
	}
	points, err := GetIncidentPointsForDate(date)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, incidentPointsResponse{Date: date, Points: points})
}

type routeTTIResponse struct {
	From    string        `json:"from"`
	To      string        `json:"to"`      // == From for a single-day (?date=) query
	Hour    int           `json:"hour"`    // -1 when the comparison scope is the whole day/range
	SlotMin int           `json:"slotMin"` // width (minutes) of each Slots bucket — the travel-time module's configured collection interval
	Routes  []RouteTTIRow `json:"routes"`
}

// GET /api/trafficinsight/route-tti?date=YYYY-MM-DD&hour=H (date default:
// today; hour default: whole day) — the "Top 10 tuyến đường" panel's data
// for a single day (the panel's "hôm nay" / "theo ngày" toggle, same
// convention the map section above uses). GET .../route-tti?from=YYYY-MM-DD
// &to=YYYY-MM-DD&hour=H instead ranks routes over that whole date range
// (the panel's "theo khoảng ngày" mode) — from/to take priority over date
// when both forms are given. Either way, hour (0-23) scopes the ranking to
// just that hour-of-day; omitted/absent means the whole day or range.
func handleRouteTTI(w http.ResponseWriter, r *http.Request) {
	if !requireViewer(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	hour := -1
	if hs := r.URL.Query().Get("hour"); hs != "" {
		h, err := strconv.Atoi(hs)
		if err != nil || h < 0 || h > 23 {
			http.Error(w, "invalid hour: must be 0-23", http.StatusBadRequest)
			return
		}
		hour = h
	}

	filter := parseRouteFilter(r)
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from != "" && to != "" {
		routes, err := GetRouteTTIForRange(from, to, hour, filter)
		if err != nil {
			http.Error(w, "invalid from/to: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, routeTTIResponse{From: from, To: to, Hour: hour, SlotMin: RouteSlotMinutes(), Routes: routes})
		return
	}

	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().In(vnLocation).Format("2006-01-02")
	} else if _, err := time.ParseInLocation("2006-01-02", date, vnLocation); err != nil {
		http.Error(w, "invalid date: "+err.Error(), http.StatusBadRequest)
		return
	}
	routes, err := GetRouteTTIForDate(date, hour, filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, routeTTIResponse{From: date, To: date, Hour: hour, SlotMin: RouteSlotMinutes(), Routes: routes})
}

// GET /api/trafficinsight/weekday-matrix?weeks=N (default 6, capped at 12)
func handleWeekdayMatrix(w http.ResponseWriter, r *http.Request) {
	if !requireViewer(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	weeks := 6
	if s := r.URL.Query().Get("weeks"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			weeks = n
		}
	}
	writeJSON(w, GetWeekdayMatrix(weeks, parseRouteFilter(r)))
}
