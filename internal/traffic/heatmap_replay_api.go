package traffic

// heatmap_replay_api.go — read-only API for www/heatmap-replay.html's
// "load from database" option: the same persistent-jam-point history
// persistent_export.go already serves to the admin-only "Xuất điểm
// Persistent (CSV)" tool (handlePersistentExport, requireTrafficAccess),
// just open to any viewer holding the TabHeatmapReplay tab instead of
// admin-only — same "a page's own tab must gate its own API, not borrow a
// stricter gate from another page" principle internal/trafficinsight's and
// internal/traffic's own flowcapture_page_api.go already follow. The real
// restriction (must hold TabHeatmapReplay, or be admin) already ran in
// internal/auth/middleware.go's tabExtraPaths[TabHeatmapReplay] before the
// request reaches here.

import (
	"net/http"
	"time"

	"github.com/AlexxIT/go2rtc/internal/auth"
)

func registerHeatmapReplayHandlers() {
	http.HandleFunc("/api/heatmap-replay/points", handleHeatmapReplayPoints)
}

// handleHeatmapReplayPoints  GET /api/heatmap-replay/points?from=RFC3339&to=RFC3339
func handleHeatmapReplayPoints(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserFromContext(r.Context()); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
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
	if to.Before(from) {
		http.Error(w, "to must not be before from", http.StatusBadRequest)
		return
	}

	rows, err := PersistentPointsInRange(from, to)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, rows)
}
