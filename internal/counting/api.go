package counting

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/auth"
	"github.com/AlexxIT/go2rtc/internal/workers"
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
	api.HandleFunc("api/counting/yolo-status", handleYoloStatus)
	api.HandleFunc("api/counting/collect", handleCollect)
	api.HandleFunc("api/counting/train", handleTrain)
	api.HandleFunc("api/counting/dataset-images", handleDatasetImages)
	api.HandleFunc("api/counting/dataset-image", handleDatasetImage)
	api.HandleFunc("api/counting/dataset-label", handleDatasetLabel)
	api.HandleFunc("api/counting/dataset-yaml", handleDatasetYaml)
	api.HandleFunc("api/counting/train-status", handleTrainStatus)
	api.HandleFunc("api/counting/models", handleModels)
	api.HandleFunc("api/counting/yolo-restart", handleYoloRestart)
	// Worker-side endpoints (called by Server 1 workers module)
	api.HandleFunc("api/counting/export", handleExport)
	api.HandleFunc("api/counting/models/download", handleModelsDownload)
	api.HandleFunc("api/counting/models/upload", handleModelsUpload)
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
	go mgr.syncYoloToWorkers()
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
		upserted := false
		for i, c := range cfg.Cameras {
			if c.ID == cam.ID {
				cfg.Cameras[i] = cam
				upserted = true
				break
			}
		}
		if !upserted {
			cfg.Cameras = append(cfg.Cameras, cam)
		}
		cfgMu.Unlock()
		_ = saveConfig()
		mgr.stopCamera(cam.ID)
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
// For remote cameras, proxies the request to the remote worker.
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
	re, isRemote := mgr.remotes[id]
	mgr.mu.Unlock()

	if isRemote {
		resp, err := workers.RequestWorker(re.cam.WorkerID, http.MethodGet, "/api/counting/debug?camera="+id, nil, "")
		if err != nil {
			http.Error(w, "remote debug unavailable: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			http.Error(w, strings.TrimSpace(string(b)), resp.StatusCode)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "no-cache, no-store")
		_, _ = io.Copy(w, resp.Body)
		return
	}

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

// GET /api/counting/yolo-status
// Proxies a health check to the Python YOLO service and returns its response.
func handleYoloStatus(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	yoloURL := getConfig().YoloURL
	if yoloURL == "" {
		yoloURL = "http://localhost:8765"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(yoloURL + "/health")
	if err != nil {
		writeJSON(w, map[string]any{"connected": false, "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	var health map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&health)
	health["connected"] = true
	writeJSON(w, health)
}

// POST /api/counting/collect?camera=id&frames=N
// For remote cameras, proxies the collect request to the remote worker.
func handleCollect(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("camera")
	frames := r.URL.Query().Get("frames")
	if frames == "" {
		frames = "50"
	}

	// Proxy to remote worker if camera is remote.
	mgr.mu.Lock()
	re, isRemote := mgr.remotes[id]
	mgr.mu.Unlock()
	if isRemote {
		path := fmt.Sprintf("/api/counting/collect?camera=%s&frames=%s", id, frames)
		resp, err := workers.RequestWorker(re.cam.WorkerID, http.MethodPost, path, nil, "")
		if err != nil {
			writeJSON(w, map[string]any{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}

	var streamName string
	for _, cam := range getConfig().Cameras {
		if cam.ID == id {
			streamName = cam.StreamName
			break
		}
	}

	yoloURL := getConfig().YoloURL
	if yoloURL == "" {
		yoloURL = "http://localhost:8765"
	}
	url := fmt.Sprintf("%s/collect/%s?frames=%s", yoloURL, id, frames)
	if streamName != "" {
		url += "&stream=" + neturl.QueryEscape(streamName)
	}
	client := &http.Client{Timeout: 600 * time.Second}
	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	if resp.StatusCode >= 400 {
		var detail struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(body, &detail)
		msg := detail.Detail
		if msg == "" {
			msg = string(body)
		}
		writeJSON(w, map[string]any{"error": msg})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

// POST /api/counting/train?worker=id
func handleTrain(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if wid := r.URL.Query().Get("worker"); wid != "" {
		body, _ := io.ReadAll(r.Body)
		proxyToWorker(w, wid, http.MethodPost, "/api/counting/train", bytes.NewReader(body), "application/json")
		return
	}
	yoloURL := getConfig().YoloURL
	if yoloURL == "" {
		yoloURL = "http://localhost:8765"
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(yoloURL+"/train", "application/json", r.Body)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	io.Copy(w, resp.Body)
}

// GET /api/counting/dataset-images?worker=id
func handleDatasetImages(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if wid := r.URL.Query().Get("worker"); wid != "" {
		proxyToWorker(w, wid, http.MethodGet, "/api/counting/dataset-images", nil, "")
		return
	}
	yoloURL := getConfig().YoloURL
	if yoloURL == "" {
		yoloURL = "http://localhost:8765"
	}
	proxyGet(w, yoloURL+"/dataset/images")
}

// GET /api/counting/dataset-image?file=name.jpg&worker=id
func handleDatasetImage(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	f := r.URL.Query().Get("file")
	if wid := r.URL.Query().Get("worker"); wid != "" {
		proxyToWorker(w, wid, http.MethodGet, "/api/counting/dataset-image?file="+neturl.QueryEscape(f), nil, "")
		return
	}
	yoloURL := getConfig().YoloURL
	if yoloURL == "" {
		yoloURL = "http://localhost:8765"
	}
	proxyGetRaw(w, yoloURL+"/dataset/image/"+f)
}

// GET /api/counting/dataset-label?file=name.jpg&worker=id (load existing boxes)
// POST /api/counting/dataset-label?worker=id (save boxes)
func handleDatasetLabel(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if wid := r.URL.Query().Get("worker"); wid != "" {
		if r.Method == http.MethodGet {
			f := r.URL.Query().Get("file")
			proxyToWorker(w, wid, http.MethodGet, "/api/counting/dataset-label?file="+neturl.QueryEscape(f), nil, "")
		} else {
			body, _ := io.ReadAll(r.Body)
			proxyToWorker(w, wid, http.MethodPost, "/api/counting/dataset-label", bytes.NewReader(body), "application/json")
		}
		return
	}
	yoloURL := getConfig().YoloURL
	if yoloURL == "" {
		yoloURL = "http://localhost:8765"
	}
	if r.Method == http.MethodGet {
		f := r.URL.Query().Get("file")
		proxyGet(w, yoloURL+"/dataset/label/"+neturl.PathEscape(f))
		return
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(yoloURL+"/dataset/label", "application/json", r.Body)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	io.Copy(w, resp.Body)
}

// GET /api/counting/train-status?worker=id
func handleTrainStatus(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if wid := r.URL.Query().Get("worker"); wid != "" {
		proxyToWorker(w, wid, http.MethodGet, "/api/counting/train-status", nil, "")
		return
	}
	yoloURL := getConfig().YoloURL
	if yoloURL == "" {
		yoloURL = "http://localhost:8765"
	}
	proxyGet(w, yoloURL+"/train/status")
}

// GET /api/counting/models — pretrained base names already shown in counting.html
// plus any custom weights found under yolo_counter's models/ dir (populated by
// training, see /train in counter.py).
func handleModels(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	yoloURL := getConfig().YoloURL
	if yoloURL == "" {
		yoloURL = "http://localhost:8765"
	}
	proxyGet(w, yoloURL+"/models")
}

// POST /api/counting/yolo-restart — restarts the yolo_counter subprocess so
// a newly-saved config (e.g. a different --model) takes effect immediately.
func handleYoloRestart(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	ok := restartYolo()
	writeJSON(w, map[string]any{"ok": ok, "restarted": ok})
}

// POST /api/counting/dataset-yaml?worker=id
func handleDatasetYaml(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if wid := r.URL.Query().Get("worker"); wid != "" {
		proxyToWorker(w, wid, http.MethodPost, "/api/counting/dataset-yaml", nil, "")
		return
	}
	yoloURL := getConfig().YoloURL
	if yoloURL == "" {
		yoloURL = "http://localhost:8765"
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(yoloURL+"/dataset/yaml", "application/json", nil)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	io.Copy(w, resp.Body)
}

// proxyToWorker forwards a request to a remote worker and copies the response back.
func proxyToWorker(w http.ResponseWriter, workerID, method, path string, body io.Reader, contentType string) {
	resp, err := workers.RequestWorker(workerID, method, path, body, contentType)
	if err != nil {
		http.Error(w, "worker unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func proxyGet(w http.ResponseWriter, url string) {
	yoloURL := getConfig().YoloURL
	if yoloURL == "" {
		yoloURL = "http://localhost:8765"
	}
	// If url doesn't start with http, prepend yoloURL
	if !strings.HasPrefix(url, "http") {
		url = yoloURL + url
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	io.Copy(w, resp.Body)
}

func proxyGetRaw(w http.ResponseWriter, url string) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	if ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	io.Copy(w, resp.Body)
}

// ── Worker-side endpoints (called by Hub / Server 1) ────────────────────────

// GET /api/counting/export?date=YYYY-MM-DD
// Returns all counting events for the given date as a JSON array.
// Used by Hub to pull data from this worker.
func handleExport(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	date := r.URL.Query().Get("date")
	if date == "" {
		http.Error(w, "date required (YYYY-MM-DD)", http.StatusBadRequest)
		return
	}
	events, err := mgr.store.getEvents(date)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, []CountEvent{})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if events == nil {
		events = []CountEvent{}
	}
	writeJSON(w, events)
}

// GET /api/counting/models/download?file=models/trained_xxx.pt
// Serves a model weight file for download by the Hub.
func handleModelsDownload(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	file := r.URL.Query().Get("file")
	if file == "" {
		http.Error(w, "file required", http.StatusBadRequest)
		return
	}
	// Resolve relative to yolo_counter dir (same as models/ logic in launcher)
	base := filepath.Dir(yoloExePath())
	fullPath := filepath.Join(base, filepath.FromSlash(file))
	// Security: must stay within base dir
	if !strings.HasPrefix(fullPath, base) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(fullPath)}))
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, fullPath)
}

// POST /api/counting/models/upload
// Accepts a multipart file upload (field "model") and saves it into models/.
func handleModelsUpload(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(512 << 20); err != nil { // 512 MB
		http.Error(w, "parse form: "+err.Error(), http.StatusBadRequest)
		return
	}
	f, fh, err := r.FormFile("model")
	if err != nil {
		http.Error(w, "field 'model' required: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer f.Close()
	base := filepath.Dir(yoloExePath())
	modelsDir := filepath.Join(base, "models")
	if err := os.MkdirAll(modelsDir, 0755); err != nil {
		http.Error(w, "mkdir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	dest := filepath.Join(modelsDir, filepath.Base(fh.Filename))
	out, err := os.Create(dest)
	if err != nil {
		http.Error(w, "create: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer out.Close()
	if _, err := io.Copy(out, f); err != nil {
		http.Error(w, "write: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"saved": "models/" + filepath.Base(fh.Filename)})
}

// yoloExePath returns the path to the yolo_counter executable (or best guess).
func yoloExePath() string {
	exeName := "yolo_counter"
	if runtime.GOOS == "windows" {
		exeName += ".exe"
	}
	self, err := os.Executable()
	if err != nil {
		return exeName
	}
	return filepath.Join(filepath.Dir(self), exeName)
}
