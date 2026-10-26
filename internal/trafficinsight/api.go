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
)

func Init() {
	http.HandleFunc("/api/trafficinsight/today", handleToday)
	http.HandleFunc("/api/trafficinsight/averages", handleAverages)
	http.HandleFunc("/api/trafficinsight/daily-series", handleDailySeries)
	http.HandleFunc("/api/trafficinsight/weekday-matrix", handleWeekdayMatrix)
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
	Avg7  AggregateMetrics `json:"avg7"`
	Avg30 AggregateMetrics `json:"avg30"`
}

// GET /api/trafficinsight/averages — today's own aggregate plus the
// trailing 7-day and 30-day averages (today inclusive, so "today" also
// anchors both baselines).
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

	todayMetrics, err := GetDailyMetrics(today.Format("2006-01-02"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	avg7, _ := GetRangeAverage(today.AddDate(0, 0, -6), today)
	avg30, _ := GetRangeAverage(today.AddDate(0, 0, -29), today)

	writeJSON(w, averagesResponse{Today: todayMetrics, Avg7: avg7, Avg30: avg30})
}

// GET /api/trafficinsight/daily-series?days=N (default 30, capped at 90)
func handleDailySeries(w http.ResponseWriter, r *http.Request) {
	if !requireViewer(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	days := 30
	if s := r.URL.Query().Get("days"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			days = n
		}
	}
	writeJSON(w, GetDailySeries(days))
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
	writeJSON(w, GetWeekdayMatrix(weeks))
}
