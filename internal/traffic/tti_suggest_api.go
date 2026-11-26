package traffic

// tti_suggest_api.go — read-only API for www/tti-suggest.html's hotspot
// list (see tti_suggest.go for the aggregation itself). The handler only
// checks "is there an authenticated user at all" — the real restriction
// (must hold auth.TabTtiSuggest, or be admin) already ran in
// internal/auth/middleware.go's tabExtraPaths before the request reaches
// here, same "a page's own tab must gate its own API" precedent
// heatmap_replay_api.go/flowcapture_page_api.go already follow.

import (
	"net/http"
	"strconv"

	"github.com/AlexxIT/go2rtc/internal/auth"
)

func registerTtiSuggestHandlers() {
	http.HandleFunc("/api/tti-suggest/hotspots", handleTtiSuggestHotspots)
	http.HandleFunc("/api/tti-suggest/hotspot-timeline", handleTtiSuggestHotspotTimeline)
}

// handleTtiSuggestHotspots  GET /api/tti-suggest/hotspots
//
//	?from=RFC3339&to=RFC3339&minTti=&minScans=&gridRadiusM=&limit=
//
// from/to are mandatory (computeHotspots/validateHistoryRange reject a
// missing or overly wide range) — a different date window can surface
// entirely different hotspots, so there is no implicit default window.
// Every other param is optional; computeHotspots fills in its own
// defaults.
func handleTtiSuggestHotspots(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserFromContext(r.Context()); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	from, to, err := parseHistoryRangeParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	q := r.URL.Query()
	minTTI, _ := strconv.ParseFloat(q.Get("minTti"), 64)
	minScans, _ := strconv.Atoi(q.Get("minScans"))
	gridRadiusM, _ := strconv.ParseFloat(q.Get("gridRadiusM"), 64)
	limit, _ := strconv.Atoi(q.Get("limit"))

	report, err := computeHotspots(from, to, minTTI, minScans, gridRadiusM, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, report)
}

// handleTtiSuggestHotspotTimeline  GET /api/tti-suggest/hotspot-timeline
//
//	?region=&key=&from=RFC3339&to=RFC3339&minTti=&gridRadiusM=
//
// www/tti-suggest.html's "chi tiết theo khung giờ" chart for ONE hotspot
// row clicked in the table — same from/to range, minTti threshold and
// gridRadiusM already chosen on that page's own filter, just for this
// single hotspot (key is exactly the HotspotSuggestion.Key computeHotspots
// returned for it) instead of every road at once. Works for an unnamed,
// grid-cell-grouped hotspot too — not just a named road — since key
// (not description) is what computeHotspotTimeline matches rows against;
// gridRadiusM here MUST match whatever was used to generate that key.
func handleTtiSuggestHotspotTimeline(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserFromContext(r.Context()); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	from, to, err := parseHistoryRangeParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	q := r.URL.Query()
	region := q.Get("region")
	key := q.Get("key")
	minTTI, _ := strconv.ParseFloat(q.Get("minTti"), 64)
	gridRadiusM, _ := strconv.ParseFloat(q.Get("gridRadiusM"), 64)

	tl, err := computeHotspotTimeline(region, key, from, to, minTTI, gridRadiusM)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, tl)
}
