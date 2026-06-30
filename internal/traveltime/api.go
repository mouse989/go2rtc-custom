package traveltime

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/AlexxIT/go2rtc/internal/auth"
)

func registerHandlers() {
	http.HandleFunc("/api/traveltime/config", handleConfig)
	http.HandleFunc("/api/traveltime/appearance", handleAppearance)
	http.HandleFunc("/api/traveltime/routes", handleRoutes)
	http.HandleFunc("/api/traveltime/routes/order", handleRoutesOrder)
	http.HandleFunc("/api/traveltime/status", handleStatus)
	http.HandleFunc("/api/traveltime/scheduler/start", handleSchedulerStart)
	http.HandleFunc("/api/traveltime/scheduler/stop", handleSchedulerStop)
	http.HandleFunc("/api/traveltime/scheduler/run-now", handleSchedulerRunNow)
	http.HandleFunc("/api/traveltime/logs", handleLogs)
	http.HandleFunc("/api/traveltime/logs/dates", handleLogDates)
	http.HandleFunc("/api/traveltime/forecast", handleForecast)
	http.HandleFunc("/api/traveltime/holidays", handleHolidays)
	http.HandleFunc("/api/traveltime/holidays/preview", handleHolidayPreview)
}

func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user.Role != auth.RoleAdmin {
		http.Error(w, "admin only", http.StatusForbidden)
		return false
	}
	return true
}

func requireLogin(w http.ResponseWriter, r *http.Request) bool {
	_, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ── Config ───────────────────────────────────────────────────────────────────

func handleConfig(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, getConfig())

	case http.MethodPut, http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			http.Error(w, "read error: "+err.Error(), http.StatusBadRequest)
			return
		}
		var newCfg Config
		if err := json.Unmarshal(body, &newCfg); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if newCfg.IntervalMin <= 0 {
			newCfg.IntervalMin = 15
		}
		wasRunning := isRunning()

		cfgMu.Lock()
		cfg = newCfg
		cfgMu.Unlock()

		if err := saveConfig(); err != nil {
			http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		if wasRunning && !newCfg.Running {
			stopScheduler()
		} else if !wasRunning && newCfg.Running {
			startScheduler()
		} else if wasRunning && newCfg.Running {
			// Interval may have changed — restart to apply new interval.
			stopScheduler()
			startScheduler()
		}

		writeJSON(w, getConfig())

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ── Appearance ───────────────────────────────────────────────────────────────

// GET  /api/traveltime/appearance — any logged-in user (reads shared config)
// PUT  /api/traveltime/appearance — admin only (saves to traveltime.json)
func handleAppearance(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !requireLogin(w, r) {
			return
		}
		cfgMu.RLock()
		a := cfg.Appearance
		cfgMu.RUnlock()
		writeJSON(w, a)

	case http.MethodPut, http.MethodPost:
		if !requireAdmin(w, r) {
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			http.Error(w, "read error: "+err.Error(), http.StatusBadRequest)
			return
		}
		var a TTIAppearance
		if err := json.Unmarshal(body, &a); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(a.Tiers) == 0 {
			http.Error(w, "at least one tier required", http.StatusBadRequest)
			return
		}
		cfgMu.Lock()
		cfg.Appearance = a
		cfgMu.Unlock()
		if err := saveConfig(); err != nil {
			http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		cfgMu.RLock()
		out := cfg.Appearance
		cfgMu.RUnlock()
		writeJSON(w, out)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ── Routes ───────────────────────────────────────────────────────────────────

func handleRoutes(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		list := listRoutes()
		if list == nil {
			list = []*Route{}
		}
		writeJSON(w, list)

	case http.MethodPost:
		var rt Route
		if err := json.NewDecoder(r.Body).Decode(&rt); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if rt.Name == "" || rt.Origin == "" || rt.Destination == "" {
			http.Error(w, "name, origin and destination are required", http.StatusBadRequest)
			return
		}
		created, err := createRoute(&rt)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(created)

	case http.MethodPut:
		var rt Route
		if err := json.NewDecoder(r.Body).Decode(&rt); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if rt.ID == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		if rt.Name == "" || rt.Origin == "" || rt.Destination == "" {
			http.Error(w, "name, origin and destination are required", http.StatusBadRequest)
			return
		}
		if err := updateRoute(&rt); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		if err := deleteRoute(id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// PUT /api/traveltime/routes/order — reorder routes in persistent storage.
func handleRoutesOrder(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPut && r.Method != http.MethodPatch {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var ids []string
	if err := json.NewDecoder(r.Body).Decode(&ids); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := reorderRoutes(ids); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Scheduler ────────────────────────────────────────────────────────────────

func handleStatus(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, getSchedulerStatus())
}

func handleSchedulerStart(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		IntervalMin int `json:"intervalMin"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.IntervalMin > 0 {
		cfgMu.Lock()
		cfg.IntervalMin = body.IntervalMin
		cfgMu.Unlock()
	}
	cfgMu.Lock()
	cfg.Running = true
	cfgMu.Unlock()
	_ = saveConfig()

	if isRunning() {
		stopScheduler()
	}
	startScheduler()
	writeJSON(w, getSchedulerStatus())
}

func handleSchedulerStop(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfgMu.Lock()
	cfg.Running = false
	cfgMu.Unlock()
	_ = saveConfig()
	stopScheduler()
	writeJSON(w, getSchedulerStatus())
}

func handleSchedulerRunNow(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	RunNow()
	writeJSON(w, map[string]string{"message": "Đang thu thập..."})
}

// ── Logs ─────────────────────────────────────────────────────────────────────

func handleLogs(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	date := r.URL.Query().Get("date")
	limit := 500
	if ls := r.URL.Query().Get("limit"); ls != "" {
		if v, err := strconv.Atoi(ls); err == nil && v > 0 {
			limit = v
		}
	}
	entries, err := getLogs(date, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, entries)
}

func handleLogDates(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	dates, err := listLogDates()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, dates)
}

// ── Holidays ─────────────────────────────────────────────────────────────────

// GET    /api/traveltime/holidays?year=2026   → list entries (year=0 = all)
// POST   /api/traveltime/holidays             → create/update (body: HolidayEntry JSON)
// DELETE /api/traveltime/holidays?id=xxx      → delete entry
func handleHolidays(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		year := 0
		if ys := r.URL.Query().Get("year"); ys != "" {
			if v, err := strconv.Atoi(ys); err == nil {
				year = v
			}
		}
		writeJSON(w, listHolidayEntries(year))

	case http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, 16<<10))
		if err != nil {
			http.Error(w, "read error: "+err.Error(), http.StatusBadRequest)
			return
		}
		var e HolidayEntry
		if err := json.Unmarshal(body, &e); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := upsertHolidayEntry(&e); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, e)

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		if err := deleteHolidayEntry(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// GET /api/traveltime/holidays/preview?date=2026-04-07
// Returns how the system classifies a given date (custom + hardcoded merged).
func handleHolidayPreview(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ds := r.URL.Query().Get("date")
	var d time.Time
	if ds == "" {
		d = time.Now().In(loc())
	} else {
		var err error
		d, err = time.ParseInLocation("2006-01-02", ds, loc())
		if err != nil {
			http.Error(w, "invalid date (expected YYYY-MM-DD)", http.StatusBadRequest)
			return
		}
	}
	kind, label := HolidayKind(d)
	kindStr := "none"
	switch kind {
	case hkTet:
		kindStr = "tet"
	case hkNational:
		kindStr = "national"
	case hkSchoolBreak:
		kindStr = "school_break"
	case hkPreHoliday:
		kindStr = "pre_holiday"
	}
	writeJSON(w, map[string]string{
		"date":  d.Format("2006-01-02"),
		"kind":  kindStr,
		"label": label,
	})
}
