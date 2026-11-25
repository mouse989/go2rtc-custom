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
	"time"

	"github.com/AlexxIT/go2rtc/internal/auth"
)

func registerTtiSuggestHandlers() {
	http.HandleFunc("/api/tti-suggest/hotspots", handleTtiSuggestHotspots)
}

// handleTtiSuggestHotspots  GET /api/tti-suggest/hotspots
//
//	?from=RFC3339&to=RFC3339&minTti=&minScans=&minRate=&limit=
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

	q := r.URL.Query()
	fromStr, toStr := q.Get("from"), q.Get("to")
	if fromStr == "" || toStr == "" {
		http.Error(w, "from and to are required (RFC3339)", http.StatusBadRequest)
		return
	}
	from, err := time.Parse(time.RFC3339, fromStr)
	if err != nil {
		http.Error(w, "invalid from: "+err.Error(), http.StatusBadRequest)
		return
	}
	to, err := time.Parse(time.RFC3339, toStr)
	if err != nil {
		http.Error(w, "invalid to: "+err.Error(), http.StatusBadRequest)
		return
	}

	minTTI, _ := strconv.ParseFloat(q.Get("minTti"), 64)
	minScans, _ := strconv.Atoi(q.Get("minScans"))
	minRate, _ := strconv.ParseFloat(q.Get("minRate"), 64)
	limit, _ := strconv.Atoi(q.Get("limit"))

	report, err := computeHotspots(from, to, minTTI, minScans, minRate, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, report)
}
