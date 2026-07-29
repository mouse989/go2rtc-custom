package incidents

import (
	"encoding/json"
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
	if s := r.URL.Query().Get("from"); s != "" {
		f.From, _ = time.Parse("2006-01-02", s)
	}
	if s := r.URL.Query().Get("to"); s != "" {
		if t, err := time.Parse("2006-01-02", s); err == nil {
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
