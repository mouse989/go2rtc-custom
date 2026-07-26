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
		list, err := List(f)
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
