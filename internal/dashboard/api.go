package dashboard

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/AlexxIT/go2rtc/internal/auth"
	"github.com/AlexxIT/go2rtc/internal/traffic"
	"github.com/AlexxIT/go2rtc/internal/traveltime"
)

func Init() {
	http.HandleFunc("/api/dashboard/summary", handleSummary)
	http.HandleFunc("/api/dashboard/history", handleHistory)
	http.HandleFunc("/api/traffic-heatmap", handleTrafficHeatmap)
}

type summary struct {
	UpdatedAt  string                       `json:"updatedAt"`
	Traffic    traffic.TrafficSnapshot      `json:"traffic"`
	TravelTime traveltime.TravelTimeSnapshot `json:"travelTime"`
}

func handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if user.Role != auth.RoleAdmin && !auth.HasTab(r.Context(), auth.TabDashboard) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	date := r.URL.Query().Get("date")
	if date == "" {
		http.Error(w, "missing date parameter", http.StatusBadRequest)
		return
	}
	entries, err := traveltime.GetLogsForDate(date, 2000)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []traveltime.LogEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entries)
}

// handleTrafficHeatmap GET /api/traffic-heatmap
// Returns the current persistent jam points for the map heatmap overlay.
// Accessible to any authenticated user (no dashboard-tab required).
func handleTrafficHeatmap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	snap := traffic.LatestSnapshot()
	pts := snap.Points
	if pts == nil {
		pts = []traffic.DashPoint{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(pts)
}

func handleSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if user.Role != auth.RoleAdmin && !auth.HasTab(r.Context(), auth.TabDashboard) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	loc, _ := time.LoadLocation("Asia/Ho_Chi_Minh")
	if loc == nil {
		loc = time.UTC
	}

	s := summary{
		UpdatedAt:  time.Now().In(loc).Format(time.RFC3339),
		Traffic:    traffic.LatestSnapshot(),
		TravelTime: traveltime.LatestSnapshot(),
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s)
}
