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
	api.HandleFunc("api/counting/stream", handleStream)
	api.HandleFunc("api/counting/sse", handleSSE)
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
	api.HandleFunc("api/counting/yolo-sync", handleYoloSyncWorker)
	// Dataset endpoints — always handled by main server's local YOLO service.
	// Use dataset-push to copy the dataset to a remote worker before training there.
	api.HandleFunc("api/counting/dataset-push", handleDatasetPush)
	api.HandleFunc("api/counting/dataset-import", handleDatasetImport)
	// Worker-side endpoints (called by Server 1 workers module)
	api.HandleFunc("api/counting/export", handleExport)
	api.HandleFunc("api/counting/models/download", handleModelsDownload)
	api.HandleFunc("api/counting/models/upload", handleModelsUpload)
	// Traffic counting stations (map layer)
	api.HandleFunc("api/counting/stations", handleStations)
	api.HandleFunc("api/counting/station-types", handleStationTypes)
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
	if id != "" {
		// Start a single camera: mark it Enabled and ensure global running flag is on,
		// then persist so the camera auto-resumes after a system restart.
		cfgMu.Lock()
		for i, cam := range cfg.Cameras {
			if cam.ID == id {
				cfg.Cameras[i].Enabled = true
				cfg.Running = true
				mgr.startCamera(cfg.Cameras[i])
				break
			}
		}
		cfgMu.Unlock()
		_ = saveConfig()
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
		// Stop a single camera and clear its Enabled flag so it does NOT auto-resume.
		mgr.stopCamera(id)
		cfgMu.Lock()
		for i := range cfg.Cameras {
			if cfg.Cameras[i].ID == id {
				cfg.Cameras[i].Enabled = false
				break
			}
		}
		cfgMu.Unlock()
		_ = saveConfig()
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
// Returns a list of available dates when date param is omitted.
// Returns []Slot5 (5-minute aggregates) when date is given.
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
	slots, err := mgr.store.getSlots(date, q.Get("camera"))
	if err != nil {
		writeJSON(w, []*Slot5{})
		return
	}
	if slots == nil {
		slots = []*Slot5{}
	}
	writeJSON(w, slots)
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

// pipeStream copies a no-timeout HTTP response body back to the client as MJPEG/SSE.
func pipeStream(w http.ResponseWriter, resp *http.Response, defaultCT string) {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		http.Error(w, strings.TrimSpace(string(b)), resp.StatusCode)
		return
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = defaultCT
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// GET /api/counting/stream?camera=id
// Proxies the MJPEG stream from the Python YOLO service for the given camera.
// For remote cameras, proxies through the remote worker (no timeout).
func handleStream(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	id := r.URL.Query().Get("camera")
	if id == "" {
		http.Error(w, "camera required", http.StatusBadRequest)
		return
	}

	// Remote cameras: proxy through the worker (no timeout).
	mgr.mu.Lock()
	re, isRemote := mgr.remotes[id]
	mgr.mu.Unlock()
	if isRemote {
		resp, err := workers.RequestWorkerStream(re.cam.WorkerID, http.MethodGet, "/api/counting/stream?camera="+neturl.QueryEscape(id))
		if err != nil {
			http.Error(w, "remote stream unavailable: "+err.Error(), http.StatusBadGateway)
			return
		}
		pipeStream(w, resp, "multipart/x-mixed-replace; boundary=frame")
		return
	}

	yoloURL := getConfig().YoloURL
	if yoloURL == "" {
		yoloURL = "http://localhost:8765"
	}

	streamURL := fmt.Sprintf("%s/stream/%s", yoloURL, neturl.PathEscape(id))
	resp, err := (&http.Client{}).Get(streamURL) // no timeout — stream runs indefinitely
	if err != nil {
		http.Error(w, "YOLO stream unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	pipeStream(w, resp, "multipart/x-mixed-replace; boundary=frame")
}

// GET /api/counting/sse?camera=id
// Proxies the SSE stream of per-frame YOLO detection data from the Python service.
// For remote cameras, proxies through the remote worker (no timeout).
func handleSSE(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	id := r.URL.Query().Get("camera")
	if id == "" {
		http.Error(w, "camera required", http.StatusBadRequest)
		return
	}

	// Remote cameras: proxy through the worker.
	mgr.mu.Lock()
	re, isRemote := mgr.remotes[id]
	mgr.mu.Unlock()
	if isRemote {
		resp, err := workers.RequestWorkerStream(re.cam.WorkerID, http.MethodGet, "/api/counting/sse?camera="+neturl.QueryEscape(id))
		if err != nil {
			http.Error(w, "remote SSE unavailable: "+err.Error(), http.StatusBadGateway)
			return
		}
		pipeStream(w, resp, "text/event-stream")
		return
	}

	yoloURL := getConfig().YoloURL
	if yoloURL == "" {
		yoloURL = "http://localhost:8765"
	}

	sseURL := fmt.Sprintf("%s/sse/%s", yoloURL, neturl.PathEscape(id))
	resp, err := (&http.Client{}).Get(sseURL) // no timeout — SSE connection stays open
	if err != nil {
		http.Error(w, "YOLO SSE unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	pipeStream(w, resp, "text/event-stream")
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
// Always collects on the main server — the main server's go2rtc proxy has RTSP
// streams for all cameras regardless of which worker processes them.
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

// GET /api/counting/dataset-images — always returns images from main server's local dataset.
func handleDatasetImages(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	yoloURL := getConfig().YoloURL
	if yoloURL == "" {
		yoloURL = "http://localhost:8765"
	}
	proxyGet(w, yoloURL+"/dataset/images")
}

// GET /api/counting/dataset-image?file=name.jpg — always from main server's local dataset.
func handleDatasetImage(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	f := r.URL.Query().Get("file")
	yoloURL := getConfig().YoloURL
	if yoloURL == "" {
		yoloURL = "http://localhost:8765"
	}
	proxyGetRaw(w, yoloURL+"/dataset/image/"+f)
}

// GET /api/counting/dataset-label?file=name.jpg — load existing boxes (always local).
// POST /api/counting/dataset-label — save annotated boxes (always local).
func handleDatasetLabel(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
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

// POST /api/counting/yolo-sync?worker=<id> — immediately push YOLO config
// (model, conf, frameWidth) from Server 1 to the specified worker and restart
// its yolo_counter. Useful when a worker was offline during a config change.
func handleYoloSyncWorker(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	workerID := r.URL.Query().Get("worker")
	if workerID == "" {
		http.Error(w, "worker param required", http.StatusBadRequest)
		return
	}
	go remoteSyncYoloConfig(workerID)
	writeJSON(w, map[string]any{"ok": true, "worker": workerID})
}

// POST /api/counting/dataset-yaml — generates dataset.yaml on the main server (always local).
// When training on a remote worker, dataset-push uploads images+labels first, then the
// remote worker generates its own yaml via its own dataset-yaml endpoint.
func handleDatasetYaml(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	// If ?worker= is provided, generate yaml on that worker (used after dataset-push).
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

// POST /api/counting/dataset-push?worker=id
// Exports the local dataset as a zip and imports it on the remote worker so it can train.
func handleDatasetPush(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	workerID := r.URL.Query().Get("worker")
	if workerID == "" {
		http.Error(w, "worker param required", http.StatusBadRequest)
		return
	}
	yoloURL := getConfig().YoloURL
	if yoloURL == "" {
		yoloURL = "http://localhost:8765"
	}
	// Step 1: export local dataset as zip.
	exportResp, err := (&http.Client{Timeout: 120 * time.Second}).Get(yoloURL + "/dataset/export")
	if err != nil {
		http.Error(w, "export failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer exportResp.Body.Close()
	if exportResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(exportResp.Body)
		http.Error(w, "export: "+strings.TrimSpace(string(b)), exportResp.StatusCode)
		return
	}
	zipData, err := io.ReadAll(exportResp.Body)
	if err != nil {
		http.Error(w, "read export: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Step 2: import zip on the remote worker.
	importResp, err := workers.RequestWorker(workerID, http.MethodPost, "/api/counting/dataset-import",
		bytes.NewReader(zipData), "application/zip")
	if err != nil {
		http.Error(w, "import on worker failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer importResp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(importResp.StatusCode)
	io.Copy(w, importResp.Body)
}

// POST /api/counting/dataset-import
// Receives a zip file (from dataset-push) and imports it into the local YOLO dataset dir.
func handleDatasetImport(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	yoloURL := getConfig().YoloURL
	if yoloURL == "" {
		yoloURL = "http://localhost:8765"
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Post(
		yoloURL+"/dataset/import", "application/zip", bytes.NewReader(body))
	if err != nil {
		http.Error(w, "import failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
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
// Returns 5-minute slot aggregates for the given date as a JSON array.
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
	slots, err := mgr.store.getSlots(date, "")
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, []*Slot5{})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if slots == nil {
		slots = []*Slot5{}
	}
	writeJSON(w, slots)
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

// GET/POST/PUT/DELETE /api/counting/stations
func handleStations(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, listStations())
	case http.MethodPost:
		if !requireAdmin(w, r) {
			return
		}
		var s Station
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		created, err := createStation(s)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, created)
	case http.MethodPut:
		if !requireAdmin(w, r) {
			return
		}
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		var s Station
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		updated, err := updateStation(id, s)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, updated)
	case http.MethodDelete:
		if !requireAdmin(w, r) {
			return
		}
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		if err := deleteStation(id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// GET/POST/PUT/DELETE /api/counting/station-types
func handleStationTypes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, listStationTypes())
	case http.MethodPost:
		if !requireAdmin(w, r) {
			return
		}
		var t StationType
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		created, err := createStationType(t)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, created)
	case http.MethodPut:
		if !requireAdmin(w, r) {
			return
		}
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		var t StationType
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		updated, err := updateStationType(id, t)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, updated)
	case http.MethodDelete:
		if !requireAdmin(w, r) {
			return
		}
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		if err := deleteStationType(id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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
