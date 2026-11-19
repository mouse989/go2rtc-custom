package traveltime

// segment_capture_api.go — admin-only HTTP surface for the per-month
// segment-capture store (segment_capture_db.go) and its one-time legacy
// migration (segment_capture_migrate.go): listing available months,
// querying a route's historical per-leg TTI within one month, and
// triggering/polling the legacy-file migration. Same admin-only gate
// handleLogs already uses for the aggregate log's own historical viewer —
// this is the same sensitivity level, just the per-leg store instead.

import (
	"encoding/json"
	"net/http"
	"strconv"
)

func registerSegmentCaptureHandlers() {
	http.HandleFunc("/api/traveltime/segment-capture/months", handleSegmentCaptureMonths)
	http.HandleFunc("/api/traveltime/segment-capture/history", handleSegmentCaptureHistory)
	http.HandleFunc("/api/traveltime/segment-capture/migrate/status", handleSegmentCaptureMigrateStatus)
	http.HandleFunc("/api/traveltime/segment-capture/migrate", handleSegmentCaptureMigrateTrigger)
}

// GET /api/traveltime/segment-capture/months
func handleSegmentCaptureMonths(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	months, err := ListSegmentCaptureMonths()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, months)
}

// GET /api/traveltime/segment-capture/history?month=2026-01&routeId=...&from=...&to=...&limit=...
func handleSegmentCaptureHistory(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	month := q.Get("month")
	if !segmentMonthKeyRe.MatchString(month) {
		http.Error(w, `month phải theo định dạng "YYYY-MM"`, http.StatusBadRequest)
		return
	}
	limit := 0
	if ls := q.Get("limit"); ls != "" {
		if v, err := strconv.Atoi(ls); err == nil {
			limit = v
		}
	}
	hist, err := QueryRouteSegmentHistory(QueryRouteSegmentHistoryParams{
		MonthKey: month,
		RouteID:  q.Get("routeId"),
		From:     q.Get("from"),
		To:       q.Get("to"),
		Limit:    limit,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, hist)
}

// GET /api/traveltime/segment-capture/migrate/status
func handleSegmentCaptureMigrateStatus(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, GetSegmentCaptureMigrationStatus())
}

// POST /api/traveltime/segment-capture/migrate
func handleSegmentCaptureMigrateTrigger(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := TriggerSegmentCaptureMigration(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(GetSegmentCaptureMigrationStatus())
}
