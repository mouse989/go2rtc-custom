package counting

import (
	"context"
	"sync"
	"time"
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
	FramesProcessed int    `json:"framesProcessed"`
	LastFrameAt     int64  `json:"lastFrameAt"` // unix seconds, 0 = never
	StartedAt       int64  `json:"startedAt"`   // unix seconds
	LastErr         string `json:"lastErr,omitempty"`
	Tier            int    `json:"tier"`
}

// Manager owns and supervises all camera workers.
type Manager struct {
	mu      sync.Mutex
	workers map[string]*workerEntry
	store   *dailyStore
}

type workerEntry struct {
	worker *CameraWorker
	cancel context.CancelFunc
}

func newManager() *Manager {
	return &Manager{
		workers: make(map[string]*workerEntry),
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
	if len(m.workers) > 0 {
		log.Info().Int("cameras", len(m.workers)).Msg("[counting] started")
	}
	// Start retention cleanup goroutine
	go m.runCleanup()
}

// stopAll stops all running workers.
func (m *Manager) stopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, e := range m.workers {
		e.cancel()
		delete(m.workers, id)
	}
}

// startCamera starts counting for a single camera (idempotent).
func (m *Manager) startCamera(cam CameraConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.workers[cam.ID]; ok {
		return // already running
	}
	ctx, cancel := context.WithCancel(context.Background())
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
}

// isRunning reports whether a camera worker is active.
func (m *Manager) isRunning(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.workers[id]
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
			ID:     cam.ID,
			Name:   cam.Name,
			Stream: cam.StreamName,
			Tier:   cam.Tier,
		}
		if e, ok := m.workers[cam.ID]; ok {
			st.Running = true
			st.Total = e.worker.total
			st.TotalDown = e.worker.totalDown
			st.TotalUp = e.worker.totalUp
			st.TotalRight = e.worker.totalRight
			st.TotalLeft = e.worker.totalLeft
			st.FramesProcessed = e.worker.framesProcessed
			st.LastFrameAt = e.worker.lastFrameAt
			st.StartedAt = e.worker.startedAt
			st.LastErr = e.worker.lastErr
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
