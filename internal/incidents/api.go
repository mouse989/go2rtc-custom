package incidents

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/AlexxIT/go2rtc/internal/auth"
)

func registerHandlers() {
	http.HandleFunc("/api/incidents", handleIncidents)
	http.HandleFunc("/api/incidents/meta", handleMeta)
	http.HandleFunc("/api/incidents/years", handleYears)
	http.HandleFunc("/api/incidents/import", handleImport)
	http.HandleFunc("/api/incidents/export", handleExport)
	http.HandleFunc("/api/incidents/time-slots", handleTimeSlots)
	http.HandleFunc("/api/incidents/recompute-slots", handleRecomputeSlots)
	http.HandleFunc("/api/incidents/active", handleActive)
	http.HandleFunc("/api/incidents/end", handleEnd)
	http.HandleFunc("/api/incidents/category-icons", handleCategoryIcons)
	http.HandleFunc("/api/incidents/category-icons/color", handleCategoryIconColor)
	http.HandleFunc("/api/incidents/category-icons/image", handleCategoryIconImage)
}

func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user.Role != auth.RoleAdmin {
		http.Error(w, "admin only", http.StatusForbidden)
		return false
	}
	return true
}

// parseListFilter reads the year/from/to/category/unit/q query params shared
// by the list and export endpoints, so both always filter identically.
func parseListFilter(r *http.Request) ListFilter {
	year, err := strconv.Atoi(r.URL.Query().Get("year"))
	if err != nil {
		year = time.Now().Year()
	}
	f := ListFilter{
		Year:     year,
		Category: r.URL.Query().Get("category"),
		Unit:     r.URL.Query().Get("unit"),
		Q:        r.URL.Query().Get("q"),
	}
	// Parsed in vnLocation, not UTC: the UI's date inputs mean a Vietnam
	// calendar day (00:00–23:59:59 ICT). Parsing with the default UTC zone
	// would shift that window 7 hours later in absolute time, so an
	// incident timestamped e.g. 06:05 ICT on the "from" day — stored in
	// vnLocation per Incident.Time's convention — would wrongly compare as
	// before the (UTC-interpreted) start of that day and get excluded.
	if s := r.URL.Query().Get("from"); s != "" {
		f.From, _ = time.ParseInLocation("2006-01-02", s, vnLocation)
	}
	if s := r.URL.Query().Get("to"); s != "" {
		if t, err := time.ParseInLocation("2006-01-02", s, vnLocation); err == nil {
			f.To = t.Add(24*time.Hour - time.Second)
		}
	}
	return f
}

// requireTab reports whether the caller has the incidents tab (or is admin),
// writing a 401/403 and returning false if not.
func requireTab(w http.ResponseWriter, r *http.Request) bool {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	if user.Role == auth.RoleAdmin {
		return true
	}
	if !auth.HasTab(r.Context(), auth.TabIncidents) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func requireImportPermission(w http.ResponseWriter, r *http.Request) bool {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	if user.Role == auth.RoleAdmin || user.AllowIncidentsImport {
		return true
	}
	http.Error(w, "forbidden: import requires elevated permission", http.StatusForbidden)
	return false
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func handleYears(w http.ResponseWriter, r *http.Request) {
	if !requireTab(w, r) {
		return
	}
	years := ListYears()
	if len(years) == 0 {
		years = []int{time.Now().Year()}
	}
	writeJSON(w, years)
}

func handleMeta(w http.ResponseWriter, r *http.Request) {
	if !requireTab(w, r) {
		return
	}
	writeJSON(w, Meta())
}

func handleIncidents(w http.ResponseWriter, r *http.Request) {
	if !requireTab(w, r) {
		return
	}

	switch r.Method {
	case http.MethodGet:
		list, err := List(parseListFilter(r))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, list)

	case http.MethodPost:
		var in Incident
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		user, _ := auth.UserFromContext(r.Context())
		if err := Create(&in, user.Username); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, in)

	case http.MethodPut:
		year, err := strconv.Atoi(r.URL.Query().Get("year"))
		if err != nil {
			http.Error(w, "missing/invalid year", http.StatusBadRequest)
			return
		}
		var in Incident
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		user, _ := auth.UserFromContext(r.Context())
		if err := Update(year, &in, user.Username); err != nil {
			status := http.StatusBadRequest
			if err == errNotFound {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, in)

	case http.MethodDelete:
		year, err := strconv.Atoi(r.URL.Query().Get("year"))
		if err != nil {
			http.Error(w, "missing/invalid year", http.StatusBadRequest)
			return
		}
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		if err := Delete(year, id); err != nil {
			status := http.StatusBadRequest
			if err == errNotFound {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleImport(w http.ResponseWriter, r *http.Request) {
	if !requireTab(w, r) || !requireImportPermission(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseMultipartForm(64 << 20); err != nil { // 64MB cap
		http.Error(w, "file too large or malformed upload: "+err.Error(), http.StatusBadRequest)
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing file field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	dryRun := r.FormValue("dry_run") == "1"

	user, _ := auth.UserFromContext(r.Context())
	result, err := ImportExcel(file, user.Username, dryRun)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, result)
}

// handleExport streams an .xlsx of the incidents matching the same
// year/from/to/category/unit/q filters as the list view, so "export" always
// means exactly what's currently on screen.
func handleExport(w http.ResponseWriter, r *http.Request) {
	if !requireTab(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	list, err := List(parseListFilter(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	filename := "su_co_giao_thong_" + time.Now().Format("20060102_150405") + ".xlsx"
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	if err := WriteExcel(w, list); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// handleTimeSlots: GET is readable by any tab holder (the entry form needs it
// for its live Khung giờ preview); PUT (change the windows) is admin-only.
func handleTimeSlots(w http.ResponseWriter, r *http.Request) {
	if !requireTab(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		windows, defaultLabel := GetTimeWindows()
		writeJSON(w, map[string]any{"windows": windows, "default_label": defaultLabel})

	case http.MethodPut:
		if !requireAdmin(w, r) {
			return
		}
		var req struct {
			Windows      []TimeWindow `json:"windows"`
			DefaultLabel string       `json:"default_label"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := SetTimeWindows(req.Windows, req.DefaultLabel); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		windows, defaultLabel := GetTimeWindows()
		writeJSON(w, map[string]any{"windows": windows, "default_label": defaultLabel})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleRecomputeSlots re-derives Khung giờ for every saved incident using
// the current window config — needed after changing the windows (or fixing a
// classification bug) so already-stored records aren't left stale.
func handleRecomputeSlots(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	updated, err := RecomputeAllTimeSlots()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]int{"updated": updated})
}

// handleActive serves the Map page's "🚨 Traffic Event" layer: every
// incident that has a coordinate (Lat/Lng set) and has not been marked
// ended (EndTime unset) — see ActiveList. Unlike the main list endpoint this
// ignores the year/from/to/category/unit/q query params the list view uses,
// and is not time-window-bound at all: an incident keeps showing, however
// old, until someone ends it via handleEnd.
func handleActive(w http.ResponseWriter, r *http.Request) {
	if !requireTab(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	out, err := ActiveList()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}

// handleEnd marks an incident resolved (records EndTime = now), via the
// incidents.html table's green "✓" button. Same permission level as
// edit/delete — any Incidents-tab holder, not admin-only.
func handleEnd(w http.ResponseWriter, r *http.Request) {
	if !requireTab(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	year, err := strconv.Atoi(r.URL.Query().Get("year"))
	if err != nil {
		http.Error(w, "missing/invalid year", http.StatusBadRequest)
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	user, _ := auth.UserFromContext(r.Context())
	in, err := EndIncident(year, id, user.Username)
	if err != nil {
		status := http.StatusBadRequest
		if err == errNotFound {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, in)
}

// handleCategoryIcons: GET lists every category's icon/color metadata
// (any Incidents-tab holder — map.html/dashboard2.html need this to draw
// markers); PUT uploads/replaces one category's icon (admin, raw image
// bytes as the body, category given as a query param so arbitrary
// Unicode/slash-containing category strings never need to be safe as a
// URL path segment); DELETE clears one category's mapping (admin).
func handleCategoryIcons(w http.ResponseWriter, r *http.Request) {
	if !requireTab(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, ListCategoryIcons())

	case http.MethodPut:
		if !requireAdmin(w, r) {
			return
		}
		category := r.URL.Query().Get("category")
		r.Body = http.MaxBytesReader(w, r.Body, maxCategoryIconUploadBytes)
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "file too large or malformed upload: "+err.Error(), http.StatusBadRequest)
			return
		}
		user, _ := auth.UserFromContext(r.Context())
		ci, err := SetCategoryIcon(category, data, user.Username)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, ci)

	case http.MethodDelete:
		category := r.URL.Query().Get("category")
		if err := DeleteCategoryIcon(category); err != nil {
			status := http.StatusBadRequest
			if err == errNotFound {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleCategoryIconColor: PUT (admin) sets or clears one category's
// marker background color independently of its icon.
func handleCategoryIconColor(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Category string `json:"category"`
		Color    string `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	user, _ := auth.UserFromContext(r.Context())
	ci, err := SetCategoryColor(req.Category, req.Color, user.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, ci)
}

// handleCategoryIconImage serves one category's uploaded icon bytes
// (?category=...) — any Incidents-tab holder, needed to render markers.
func handleCategoryIconImage(w http.ResponseWriter, r *http.Request) {
	if !requireTab(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	category := r.URL.Query().Get("category")
	data, contentType, ok := GetCategoryIconImage(category)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = w.Write(data)
}
