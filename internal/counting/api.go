package counting

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/auth"
)

func registerAPI() {
	api.HandleFunc("api/counting/config", handleConfig)
	api.HandleFunc("api/counting/status", handleStatus)
	api.HandleFunc("api/counting/start", handleStart)
	api.HandleFunc("api/counting/stop", handleStop)
	api.HandleFunc("api/counting/cameras", handleCameras)
	api.HandleFunc("api/counting/data", handleData)
	api.HandleFunc("api/counting/summary", handleSummary)
	api.HandleFunc("api/counting/events", handleEvents)
	api.HandleFunc("api/counting/debug", handleDebug)
}

func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user.Role != auth.RoleAdmin {
		http.Error(w, "admin only", http.StatusForbidden)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	data, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

// GET/PUT /api/counting/config
func handleConfig(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, getConfig())
		return
	}
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var newCfg Config
	if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	wasRunning := cfg.Running
	cfgMu.Lock()
	cfg = newCfg
	cfgMu.Unlock()
	if err := saveConfig(); err != nil {
		log.Error().Err(err).Msg("[counting] save config")
	}
	if wasRunning && !newCfg.Running {
		mgr.stopAll()
	} else if !wasRunning && newCfg.Running {
		mgr.startAll()
	} else if newCfg.Running {
		mgr.stopAll()
		mgr.startAll()
	}
	writeJSON(w, getConfig())
}

// GET /api/counting/status
func handleStatus(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	writeJSON(w, map[string]any{
		"running":  cfg.Running,
		"cameras":  mgr.statuses(),
		"totalCam": len(cfg.Cameras),
	})
}

// POST /api/counting/start
func handleStart(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	id := r.URL.Query().Get("id")
	c := getConfig()
	if id != "" {
		for _, cam := range c.Cameras {
			if cam.ID == id {
				mgr.startCamera(cam)
				break
			}
		}
	} else {
		cfgMu.Lock()
		cfg.Running = true
		cfgMu.Unlock()
		_ = saveConfig()
		mgr.startAll()
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// POST /api/counting/stop
func handleStop(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	id := r.URL.Query().Get("id")
	if id != "" {
		mgr.stopCamera(id)
	} else {
		cfgMu.Lock()
		cfg.Running = false
		cfgMu.Unlock()
		_ = saveConfig()
		mgr.stopAll()
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// GET/POST/PUT/DELETE /api/counting/cameras
func handleCameras(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, getConfig().Cameras)

	case http.MethodPost:
		var cam CameraConfig
		if err := json.NewDecoder(r.Body).Decode(&cam); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if cam.ID == "" {
			cam.ID = newCameraID(cam.StreamName)
		}
		// Default to horizontal line at 50% counting both directions
		if cam.LineHPos == 0 && cam.LineVPos == 0 {
			cam.LineHPos = 0.5
		}
		if !cam.CountDown && !cam.CountUp && !cam.CountRight && !cam.CountLeft {
			cam.CountDown = true
			cam.CountUp = true
		}
		if cam.Tier == 0 {
			cam.Tier = 1
		}
		cfgMu.Lock()
		cfg.Cameras = append(cfg.Cameras, cam)
		cfgMu.Unlock()
		_ = saveConfig()
		if cam.Enabled && cfg.Running {
			mgr.startCamera(cam)
		}
		writeJSON(w, cam)

	case http.MethodPut:
		var cam CameraConfig
		if err := json.NewDecoder(r.Body).Decode(&cam); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cfgMu.Lock()
		found := false
		for i, c := range cfg.Cameras {
			if c.ID == cam.ID {
				cfg.Cameras[i] = cam
				found = true
				break
			}
		}
		cfgMu.Unlock()
		if !found {
			http.Error(w, "camera not found", http.StatusNotFound)
			return
		}
		_ = saveConfig()
		mgr.stopCamera(cam.ID)
		if cam.Enabled && cfg.Running {
			mgr.startCamera(cam)
		}
		writeJSON(w, cam)

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		mgr.stopCamera(id)
		cfgMu.Lock()
		cameras := cfg.Cameras[:0]
		for _, c := range cfg.Cameras {
			if c.ID != id {
				cameras = append(cameras, c)
			}
		}
		cfg.Cameras = cameras
		cfgMu.Unlock()
		_ = saveConfig()
		writeJSON(w, map[string]bool{"ok": true})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// GET /api/counting/data?date=YYYY-MM-DD&camera=id
func handleData(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	q := r.URL.Query()
	date := q.Get("date")
	if date == "" {
		dates, _ := mgr.store.listDates()
		writeJSON(w, dates)
		return
	}
	events, err := mgr.store.getEvents(date)
	if err != nil {
		writeJSON(w, []CountEvent{})
		return
	}
	camID := q.Get("camera")
	if camID != "" {
		filtered := events[:0]
		for _, ev := range events {
			if ev.CameraID == camID {
				filtered = append(filtered, ev)
			}
		}
		events = filtered
	}
	writeJSON(w, events)
}

// GET /api/counting/summary?date=YYYY-MM-DD
func handleSummary(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	summary, err := mgr.store.hourlySummary(date)
	if err != nil {
		writeJSON(w, []HourlySummary{})
		return
	}
	writeJSON(w, summary)
}

// GET /api/counting/events?limit=N
func handleEvents(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	limit := 100
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	events := evRing.recent(limit)
	if events == nil {
		events = []CountEvent{}
	}
	writeJSON(w, events)
}

// GET /api/counting/debug?camera=id
// Returns an annotated JPEG showing the MOG2 mask, blobs, tracks, and counting lines.
func handleDebug(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	id := r.URL.Query().Get("camera")
	if id == "" {
		http.Error(w, "camera required", http.StatusBadRequest)
		return
	}
	mgr.mu.Lock()
	e, ok := mgr.workers[id]
	mgr.mu.Unlock()
	if !ok {
		http.Error(w, "camera not running", http.StatusNotFound)
		return
	}
	e.worker.debugMu.Lock()
	frame := e.worker.debugJPEG
	e.worker.debugMu.Unlock()
	if len(frame) == 0 {
		http.Error(w, "no debug frame yet — waiting for first snapshot", http.StatusNotFound)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("Cache-Control", "no-cache, no-store")
	_, _ = w.Write(frame)
}

func newCameraID(streamName string) string {
	base := strings.ReplaceAll(streamName, " ", "_")
	if base == "" {
		base = "cam"
	}
	return "c_" + base
}
