package traffic

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/AlexxIT/go2rtc/internal/auth"
)

func registerAPIHandlers() {
	http.HandleFunc("/api/traffic/config", handleConfig)
	http.HandleFunc("/api/traffic/start", handleStart)
	http.HandleFunc("/api/traffic/stop", handleStop)
	http.HandleFunc("/api/traffic/scan", handleScan)
	http.HandleFunc("/api/traffic/status", handleStatus)
	http.HandleFunc("/api/traffic/logs", handleLogs)
	http.HandleFunc("/api/traffic/data", handleData)
	http.HandleFunc("/api/traffic/persistent-export", handlePersistentExport)
}

func requireTrafficAccess(w http.ResponseWriter, r *http.Request) bool {
	caller, ok := auth.UserFromContext(r.Context())
	if !ok || caller.Role != auth.RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

// requireTrafficViewer allows any authenticated user to read scan history
// (handleData only); the Dashboard tab's own gate in
// internal/auth/middleware.go's tabExtraPaths[TabDashboard] already limits
// who reaches this path. Config/start/stop/scan/status/logs stay
// admin-only via requireTrafficAccess above since they mutate state or
// trigger external VietMap API calls.
func requireTrafficViewer(w http.ResponseWriter, r *http.Request) bool {
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

func handleConfig(w http.ResponseWriter, r *http.Request) {
	if !requireTrafficAccess(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, getConfig())

	case http.MethodPut:
		var newCfg Config
		if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		wasRunning := isRunning()

		cfgMu.Lock()
		cfg = newCfg
		cfgMu.Unlock()

		if err := saveConfig(); err != nil {
			http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Restart worker if it was running
		if wasRunning {
			stopWorker()
			if newCfg.Running {
				startWorker()
			}
		} else if newCfg.Running {
			startWorker()
		}

		writeJSON(w, getConfig())

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleStart(w http.ResponseWriter, r *http.Request) {
	if !requireTrafficAccess(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	startWorker()
	cfgMu.Lock()
	cfg.Running = true
	cfgMu.Unlock()
	_ = saveConfig()
	w.WriteHeader(http.StatusOK)
}

func handleStop(w http.ResponseWriter, r *http.Request) {
	if !requireTrafficAccess(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stopWorker()
	cfgMu.Lock()
	cfg.Running = false
	cfgMu.Unlock()
	_ = saveConfig()
	w.WriteHeader(http.StatusOK)
}

func handleScan(w http.ResponseWriter, r *http.Request) {
	if !requireTrafficAccess(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	go func() {
		if err := runScan(); err != nil {
			addLog("error", "manual scan failed: "+err.Error())
		}
	}()
	w.WriteHeader(http.StatusAccepted)
}

type statusResponse struct {
	Running    bool   `json:"running"`
	LastScanAt string `json:"lastScanAt"`
	NextScanAt string `json:"nextScanAt"`
	NextScanIn int    `json:"nextScanInSec"`
	Raw        int    `json:"raw"`
	Filtered   int    `json:"filtered"`
	Persistent int    `json:"persistent"`
	LastError  string `json:"lastError"`
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	if !requireTrafficAccess(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	last, next, raw, filtered, persistent, errStr, running := getScanState()

	var lastStr, nextStr string
	var nextIn int

	if !last.IsZero() {
		lastStr = last.Format("15:04:05 02/01/2006")
	}
	if !next.IsZero() {
		nextStr = next.Format("15:04:05 02/01/2006")
		secs := int(time.Until(next).Seconds())
		if secs < 0 {
			secs = 0
		}
		nextIn = secs
	}

	writeJSON(w, statusResponse{
		Running:    running,
		LastScanAt: lastStr,
		NextScanAt: nextStr,
		NextScanIn: nextIn,
		Raw:        raw,
		Filtered:   filtered,
		Persistent: persistent,
		LastError:  errStr,
	})
}

func handleLogs(w http.ResponseWriter, r *http.Request) {
	if !requireTrafficAccess(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	logs := getLogs()
	if logs == nil {
		logs = []LogEntry{}
	}
	writeJSON(w, logs)
}

// handleData lists stored scan files, or returns one file's content when
// the "file" query parameter is given.
func handleData(w http.ResponseWriter, r *http.Request) {
	if !requireTrafficViewer(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if name := r.URL.Query().Get("file"); name != "" {
		data, err := readScanFile(name)
		if err != nil {
			http.Error(w, "not found: "+err.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
		_, _ = w.Write(data)
		return
	}

	files, err := listScanFiles(100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, files)
}

// handlePersistentExport  GET /api/traffic/persistent-export?from=RFC3339&to=RFC3339
// (admin only — same gate as every other Traffic Monitor tool, requireTrafficAccess)
// Returns the flattened persistent-point rows for the "Xuất điểm Persistent
// (CSV)" tool in www/traffic.html; the CSV itself is assembled client-side.
func handlePersistentExport(w http.ResponseWriter, r *http.Request) {
	if !requireTrafficAccess(w, r) {
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
