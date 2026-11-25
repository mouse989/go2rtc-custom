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
}

// handleTtiSuggestHotspots  GET /api/tti-suggest/hotspots?days=&minTti=&minScans=&minRate=&limit=
// Every query param is optional; computeHotspots fills in its own defaults.
func handleTtiSuggestHotspots(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserFromContext(r.Context()); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()
	days, _ := strconv.Atoi(q.Get("days"))
	minTTI, _ := strconv.ParseFloat(q.Get("minTti"), 64)
	minScans, _ := strconv.Atoi(q.Get("minScans"))
	minRate, _ := strconv.ParseFloat(q.Get("minRate"), 64)
	limit, _ := strconv.Atoi(q.Get("limit"))

	out, err := computeHotspots(days, minTTI, minScans, minRate, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}
