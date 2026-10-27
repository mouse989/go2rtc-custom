package trafficinsight

// incident_stats.go — backend for the "Thống kê sự cố giao thông" module
// ported from www/dashboard2.html (its own Diễn biến sự cố theo ngày /
// Theo khung giờ / Phân bố theo giờ trong ngày charts + Từ/Đến/loại filter
// tiles) onto www/trafficinsight.html. Same reasoning as the rest of this
// package: reuses incidents.List/GetTimeWindows directly rather than
// incidents/api.go's own TabIncidents-gated /api/incidents and
// /api/incidents/time-slots endpoints, so a TabTrafficInsight-only viewer
// can see this module without also needing Incidents-tab access.

import (
	"net/http"
	"sort"
	"time"

	"github.com/AlexxIT/go2rtc/internal/incidents"
)

// IncidentSummary is one incident's data for this module's charts —
// category/time/time_slot/congestion flag only, no coordinates (that's
// IncidentPoint, used by the map layer's "Sự cố GT" markers instead) and
// no free-text content/location/unit (those charts weren't ported).
type IncidentSummary struct {
	ID                  string `json:"id"`
	Category            string `json:"category"`
	Severity            int    `json:"severity,omitempty"`
	Time                string `json:"time"` // RFC3339
	TimeSlot            string `json:"timeSlot"`
	CongestionRiskPoint bool   `json:"congestionRiskPoint"`
}

// GetIncidentsForRange returns every incident (regardless of coordinate)
// whose Time falls within the Vietnam-local calendar days [from, to]
// (YYYY-MM-DD, both inclusive), oldest first. Spans the year boundary if
// the range crosses one — one incidents.List call per year touched,
// concatenated — same approach www/dashboard2.html's own
// fetchIncidentsRange did client-side (two /api/incidents calls), just
// done server-side here.
func GetIncidentsForRange(from, to string) ([]IncidentSummary, error) {
	fromStart, _, err := dayBounds(from)
	if err != nil {
		return nil, err
	}
	_, toEnd, err := dayBounds(to)
	if err != nil {
		return nil, err
	}

	years := map[int]bool{fromStart.Year(): true, toEnd.Year(): true}
	out := make([]IncidentSummary, 0, 64)
	for y := range years {
		list, err := incidents.List(incidents.ListFilter{Year: y, From: fromStart, To: toEnd})
		if err != nil {
			continue // a year with no file yet is not an error — nothing logged that year
		}
		for _, in := range list {
			out = append(out, IncidentSummary{
				ID:                  in.ID,
				Category:            in.Category,
				Severity:            in.Severity,
				Time:                in.Time.Format(time.RFC3339),
				TimeSlot:            in.TimeSlot,
				CongestionRiskPoint: in.CongestionRiskPoint,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out, nil
}

type incidentStatsResponse struct {
	From      string            `json:"from"`
	To        string            `json:"to"`
	Incidents []IncidentSummary `json:"incidents"`
}

// GET /api/trafficinsight/incident-stats?from=YYYY-MM-DD&to=YYYY-MM-DD —
// feeds the "Thống kê sự cố giao thông" tiles + Diễn biến sự cố theo ngày
// + Theo khung giờ charts (a single-day from==to range also serves the
// Phân bố theo giờ trong ngày chart's own date picker).
func handleIncidentStats(w http.ResponseWriter, r *http.Request) {
	if !requireViewer(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" || to == "" {
		http.Error(w, "from and to are required (YYYY-MM-DD)", http.StatusBadRequest)
		return
	}
	list, err := GetIncidentsForRange(from, to)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, incidentStatsResponse{From: from, To: to, Incidents: list})
}

// GET /api/trafficinsight/incident-time-slots — the admin-configured
// "Khung giờ" windows (internal/incidents/timeslots.go), needed by the
// "Theo khung giờ" chart to know which time_slot names are the configured
// peak windows (colored) vs. the default/outside-peak bucket (gray).
func handleIncidentTimeSlots(w http.ResponseWriter, r *http.Request) {
	if !requireViewer(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	windows, defaultLabel := incidents.GetTimeWindows()
	writeJSON(w, map[string]any{"windows": windows, "default_label": defaultLabel})
}
