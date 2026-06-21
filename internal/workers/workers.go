package workers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/rs/zerolog"
)

var log zerolog.Logger

// Worker represents a remote go2rtc worker server.
type Worker struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	URL      string `json:"url"`      // e.g. "http://192.168.1.2:1984"
	Username string `json:"username"`
	Password string `json:"password"`
	Enabled  bool   `json:"enabled"`
	RTSPBase string `json:"rtspBase,omitempty"` // RTSP URL that yolo_counter on this worker should pull from, e.g. "rtsp://server1-ip:8554"
}

// WorkerStatus is the runtime status of a worker (cached, not persisted).
type WorkerStatus struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	URL          string `json:"url"`
	Enabled      bool   `json:"enabled"`
	Online       bool   `json:"online"`
	Error        string `json:"error,omitempty"`
	LastCheck    string `json:"lastCheck"`
	LastSync     string `json:"lastSync,omitempty"`
	Cameras      int    `json:"cameras,omitempty"`
	Running      bool   `json:"running,omitempty"`
	YoloModel    string `json:"yoloModel,omitempty"`
	Training     bool   `json:"training,omitempty"`
	TrainedModel string `json:"trainedModel,omitempty"` // last auto-pulled model

	// Hardware stats fetched from /api/system/stats during health check.
	CPUPercent float64 `json:"cpu_percent,omitempty"`
	MemPercent float64 `json:"mem_percent,omitempty"`
	MemUsed    uint64  `json:"mem_used,omitempty"`
	MemTotal   uint64  `json:"mem_total,omitempty"`
	NetInRate  uint64  `json:"net_in_rate,omitempty"`
	NetOutRate uint64  `json:"net_out_rate,omitempty"`
}

var (
	workersMu sync.RWMutex
	workers   []*Worker
	storeFile string
)

// statusCache stores *WorkerStatus keyed by worker ID.
var statusCache sync.Map

// SetEventImporter registers the function workers will call to import remote events.
// This is called during startup by the counting package to avoid a circular import.
var eventImporter func(workerID, workerName, date string, events json.RawMessage) error

// SetEventImporter registers the callback used to import events fetched from workers.
func SetEventImporter(fn func(workerID, workerName, date string, events json.RawMessage) error) {
	eventImporter = fn
}

// Init initialises the workers module.
func Init() {
	log = app.GetLogger("workers")

	storeFile = "workers.json"
	if app.ConfigPath != "" {
		storeFile = filepath.Join(filepath.Dir(app.ConfigPath), "workers.json")
	}

	if err := loadWorkers(); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Warn().Err(err).Msg("[workers] could not load workers.json, starting empty")
	}

	registerAPI()

	go runHealthLoop()
	go runSyncLoop()

	log.Info().Str("store", storeFile).Msg("[workers] ready")
}

// ── CRUD ─────────────────────────────────────────────────────────────────────

func listWorkers() []*Worker {
	workersMu.RLock()
	defer workersMu.RUnlock()
	out := make([]*Worker, len(workers))
	copy(out, workers)
	return out
}

func getWorkerByID(id string) *Worker {
	workersMu.RLock()
	defer workersMu.RUnlock()
	for _, w := range workers {
		if w.ID == id {
			return w
		}
	}
	return nil
}

func addWorker(w *Worker) error {
	w.ID = fmt.Sprintf("w_%x", time.Now().UnixNano())
	workersMu.Lock()
	workers = append(workers, w)
	workersMu.Unlock()
	return saveWorkers()
}

func updateWorker(w *Worker) error {
	workersMu.Lock()
	defer workersMu.Unlock()
	for i, existing := range workers {
		if existing.ID == w.ID {
			workers[i] = w
			tokenCache.Delete(w.ID) // evict cached token on credential change
			return saveWorkers()
		}
	}
	return errors.New("worker not found: " + w.ID)
}

func deleteWorker(id string) error {
	workersMu.Lock()
	defer workersMu.Unlock()
	for i, w := range workers {
		if w.ID == id {
			workers = append(workers[:i], workers[i+1:]...)
			statusCache.Delete(id)
			tokenCache.Delete(id)
			return saveWorkers()
		}
	}
	return errors.New("worker not found: " + id)
}

func saveWorkers() error {
	data, err := json.MarshalIndent(workers, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(storeFile, data, 0644)
}

func loadWorkers() error {
	data, err := os.ReadFile(storeFile)
	if err != nil {
		return err
	}
	workersMu.Lock()
	defer workersMu.Unlock()
	return json.Unmarshal(data, &workers)
}

// ── Status cache ─────────────────────────────────────────────────────────────

func getStatus(id string) *WorkerStatus {
	if v, ok := statusCache.Load(id); ok {
		return v.(*WorkerStatus)
	}
	return nil
}

func setStatus(s *WorkerStatus) {
	statusCache.Store(s.ID, s)
}

// RequestWorker makes an authenticated HTTP request to a named worker.
// Used by the counting package to push/pull camera config from remote workers.
func RequestWorker(id, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	wk := getWorkerByID(id)
	if wk == nil {
		return nil, fmt.Errorf("worker not found: %s", id)
	}
	return workerRequest(wk, method, path, body, contentType)
}

// GetWorkerRTSPBase returns the configured RTSP base URL for the named worker.
func GetWorkerRTSPBase(id string) string {
	wk := getWorkerByID(id)
	if wk == nil {
		return ""
	}
	return wk.RTSPBase
}

// GetEnabledWorkerIDs returns the IDs of all enabled workers.
// Used by the counting package to push YOLO config to every worker,
// not just those with currently-active cameras.
func GetEnabledWorkerIDs() []string {
	workersMu.RLock()
	defer workersMu.RUnlock()
	var ids []string
	for _, w := range workers {
		if w.Enabled {
			ids = append(ids, w.ID)
		}
	}
	return ids
}

func allStatuses() []*WorkerStatus {
	list := listWorkers()
	out := make([]*WorkerStatus, 0, len(list))
	for _, w := range list {
		if s := getStatus(w.ID); s != nil {
			out = append(out, s)
		} else {
			// Return a placeholder so the UI knows the worker exists.
			out = append(out, &WorkerStatus{
				ID:      w.ID,
				Name:    w.Name,
				URL:     w.URL,
				Enabled: w.Enabled,
			})
		}
	}
	return out
}
