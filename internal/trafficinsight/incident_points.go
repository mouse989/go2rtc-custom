package trafficinsight

// incident_points.go — backend for the "Sự cố GT" (event) layer on the
// Traffic Insight map, toggled alongside the JF jam-point layer and sharing
// the exact same hôm nay/theo ngày date filter (unlike map.html's own
// "Traffic Event" layer, which is always a last-N-minutes "currently
// active" window — see internal/incidents/api.go's handleActive — this
// layer is just "every coordinate-tagged incident logged that Vietnam-local
// calendar day", so toggling "hôm nay" vs. a past date behaves exactly like
// it already does for jam points).
//
// Reuses incidents.List directly rather than incidents/api.go's own
// /api/incidents endpoints, which are gated by auth.TabIncidents — same
// reasoning as the rest of this package (see insight.go's package doc):
// keeps a TabTrafficInsight-only viewer able to see this layer without also
// needing Incidents-tab access.

import (
	"net/http"
	"time"

	"github.com/AlexxIT/go2rtc/internal/incidents"
)

// IncidentPoint is one incident's map-marker data.
type IncidentPoint struct {
	ID       string  `json:"id"`
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	Category string  `json:"category"`
	Content  string  `json:"content"`
	Location string  `json:"location"`
	Time     string  `json:"time"` // RFC3339
}

// GetIncidentPointsForDate returns every incident with a coordinate
// (Lat/Lng set) logged within the Vietnam-local calendar day "date"
// (YYYY-MM-DD). Incidents with no coordinate are skipped — same filter
// internal/incidents/api.go's handleActive already applies for map.html's
// own incident layer — there's nothing to place on a map for them.
func GetIncidentPointsForDate(date string) ([]IncidentPoint, error) {
	start, end, err := dayBounds(date)
	if err != nil {
		return nil, err
	}
	list, err := incidents.List(incidents.ListFilter{Year: start.Year(), From: start, To: end})
	if err != nil {
		return nil, err
	}
	out := make([]IncidentPoint, 0, len(list))
	for _, in := range list {
		if in.Lat == 0 && in.Lng == 0 {
			continue
		}
		out = append(out, IncidentPoint{
			ID:       in.ID,
			Lat:      in.Lat,
			Lng:      in.Lng,
			Category: in.Category,
			Content:  in.Content,
			Location: in.Location,
			Time:     in.Time.Format(time.RFC3339),
		})
	}
	return out, nil
}

// handleIncidentCategoryIcons / handleIncidentCategoryIconImage are thin
// passthroughs onto incidents.ListCategoryIcons/GetCategoryIconImage — the
// same per-category marker icon/color map.html's own "Sự cố GT" layer
// reads via GET /api/incidents/category-icons(/image) — so this page's
// incident markers render identically to map.html's instead of a flat
// placeholder dot. Deliberately NOT calling through to those endpoints
// directly: both are gated by requireTab (auth.TabIncidents), which would
// 403 a TabTrafficInsight-only viewer — same reasoning as
// GetIncidentPointsForDate above.
func handleIncidentCategoryIcons(w http.ResponseWriter, r *http.Request) {
	if !requireViewer(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, incidents.ListCategoryIcons())
}

// GET /api/trafficinsight/incident-category-icons/image?category=... —
// same response shape and Cache-Control header as
// internal/incidents/api.go's handleCategoryIconImage, just reachable
// without TabIncidents.
func handleIncidentCategoryIconImage(w http.ResponseWriter, r *http.Request) {
	if !requireViewer(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	category := r.URL.Query().Get("category")
	data, contentType, ok := incidents.GetCategoryIconImage(category)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = w.Write(data)
}
