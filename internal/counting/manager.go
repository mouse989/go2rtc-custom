package counting

import (
	"context"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/workers"
)

// CameraStatus holds the live state of one camera worker.
type CameraStatus struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Stream          string `json:"streamName"`
	Running         bool   `json:"running"`
	Total           int    `json:"total"`
	TotalDown       int    `json:"totalDown,omitempty"`
	TotalUp         int    `json:"totalUp,omitempty"`
	TotalRight      int    `json:"totalRight,omitempty"`
	TotalLeft       int    `json:"totalLeft,omitempty"`
	TotalCar        int    `json:"totalCar,omitempty"`
	TotalMotorcycle int    `json:"totalMotorcycle,omitempty"`
	TotalBus        int    `json:"totalBus,omitempty"`
	TotalTruck      int    `json:"totalTruck,omitempty"`
	FramesProcessed int    `json:"framesProcessed"`
	LastFrameAt     int64  `json:"lastFrameAt"` // unix seconds, 0 = never
	StartedAt       int64  `json:"startedAt"`   // unix seconds
	LastErr         string `json:"lastErr,omitempty"`
	Tier            int    `json:"tier"`
	WorkerID        string                    `json:"workerId,omitempty"` // set if camera is handled by a remote worker
	DirTypeCounts   map[string]map[string]int `json:"dirTypeCounts,omitempty"` // live direction×type breakdown from yolo_counter
}

// Manager owns and supervises all camera workers.
type Manager struct {
	mu      sync.Mutex
	workers map[string]*workerEntry  // local cameras
	remotes map[string]*remoteEntry  // cameras delegated to a remote worker
	store   *dailyStore
}

type workerEntry struct {
	worker *CameraWorker
	cancel context.CancelFunc
}

func newManager() *Manager {
	return &Manager{
		workers: make(map[string]*workerEntry),
		remotes: make(map[string]*remoteEntry),
		store:   newDailyStore(),
	}
}

// startAll starts workers for all enabled cameras in config.
func (m *Manager) startAll() {
	c := getConfig()
	for _, cam := range c.Cameras {
		if cam.Enabled {
			m.startCamera(cam)
		}
	}
	total := len(m.workers) + len(m.remotes)
	if total > 0 {
		log.Info().Int("cameras", total).Msg("[counting] started")
	}
	// Start retention cleanup goroutine
	go m.runCleanup()
}

// stopAll stops all running workers (local and remote).
func (m *Manager) stopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, e := range m.workers {
		e.cancel()
		delete(m.workers, id)
	}
	for id, e := range m.remotes {
		e.cancel()
		delete(m.remotes, id)
	}
}

// startCamera starts counting for a single camera (idempotent).
func (m *Manager) startCamera(cam CameraConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.workers[cam.ID]; ok {
		return // already running locally
	}
	if _, ok := m.remotes[cam.ID]; ok {
		return // already running on remote worker
	}
	ctx, cancel := context.WithCancel(context.Background())
	if cam.WorkerID != "" {
		e := &remoteEntry{cam: cam, cancel: cancel}
		e.status = CameraStatus{ID: cam.ID, Name: cam.Name, Stream: cam.StreamName, Tier: cam.Tier, WorkerID: cam.WorkerID}
		m.remotes[cam.ID] = e
		go m.runRemoteCamera(ctx, e)
		return
	}
	w := newCameraWorker(cam, m.store)
	m.workers[cam.ID] = &workerEntry{worker: w, cancel: cancel}
	go w.run(ctx)
}

// stopCamera stops counting for a single camera.
func (m *Manager) stopCamera(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.workers[id]; ok {
		e.cancel()
		delete(m.workers, id)
	}
	if e, ok := m.remotes[id]; ok {
		e.cancel()
		delete(m.remotes, id)
	}
}

// isRunning reports whether a camera worker is active (local or remote).
func (m *Manager) isRunning(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.workers[id]; ok {
		return true
	}
	_, ok := m.remotes[id]
	return ok
}

// statuses returns live status for all configured cameras.
func (m *Manager) statuses() []CameraStatus {
	c := getConfig()
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]CameraStatus, 0, len(c.Cameras))
	for _, cam := range c.Cameras {
		st := CameraStatus{
			ID:       cam.ID,
			Name:     cam.Name,
			Stream:   cam.StreamName,
			Tier:     cam.Tier,
			WorkerID: cam.WorkerID,
		}
		if e, ok := m.workers[cam.ID]; ok {
			st.Running = true
			st.Total = e.worker.total
			st.TotalDown = e.worker.totalDown
			st.TotalUp = e.worker.totalUp
			st.TotalRight = e.worker.totalRight
			st.TotalLeft = e.worker.totalLeft
			st.TotalCar = e.worker.totalCar
			st.TotalMotorcycle = e.worker.totalMotorcycle
			st.TotalBus = e.worker.totalBus
			st.TotalTruck = e.worker.totalTruck
			st.DirTypeCounts = e.worker.dirTypeCounts
			st.FramesProcessed = e.worker.framesProcessed
			st.LastFrameAt = e.worker.lastFrameAt
			st.StartedAt = e.worker.startedAt
			st.LastErr = e.worker.lastErr
		} else if re, ok := m.remotes[cam.ID]; ok {
			re.mu.Lock()
			st = re.status
			re.mu.Unlock()
		}
		out = append(out, st)
	}
	return out
}

// getTotal returns the live vehicle total for a camera.
func (m *Manager) getTotal(id string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.workers[id]; ok {
		return e.worker.total
	}
	return 0
}

// syncYoloToWorkers pushes YOLO config (model, conf, frameWidth) to all enabled
// workers, not just those with currently-active cameras.
func (m *Manager) syncYoloToWorkers() {
	seen := make(map[string]bool)
	// Active remote cameras
	m.mu.Lock()
	for _, e := range m.remotes {
		seen[e.cam.WorkerID] = true
	}
	m.mu.Unlock()
	// All enabled workers (even without running cameras)
	for _, id := range workers.GetEnabledWorkerIDs() {
		seen[id] = true
	}
	for workerID := range seen {
		go remoteSyncYoloConfig(workerID)
	}
}

// runCleanup periodically deletes old data files.
func (m *Manager) runCleanup() {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		c := getConfig()
		if c.Storage.Enabled && c.Storage.RetentionDays > 0 {
			m.store.deleteOlderThan(c.Storage.RetentionDays)
		}
	}
}
