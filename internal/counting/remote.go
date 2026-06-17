package counting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/workers"
)

// remoteEntry tracks a camera delegated to a remote worker.
type remoteEntry struct {
	cam    CameraConfig
	cancel context.CancelFunc
	mu     sync.Mutex
	status CameraStatus
}

// runRemoteCamera manages the lifecycle of a camera on a remote worker:
// registers it, polls its status, and cleans up when cancelled.
func (m *Manager) runRemoteCamera(ctx context.Context, e *remoteEntry) {
	defer func() {
		if err := remoteDeleteCamera(e.cam); err != nil {
			log.Warn().Err(err).Str("cam", e.cam.ID).Str("worker", e.cam.WorkerID).Msg("[counting] remote unregister failed")
		} else {
			log.Info().Str("cam", e.cam.ID).Str("worker", e.cam.WorkerID).Msg("[counting] remote camera unregistered")
		}
	}()

	// Register camera on the remote worker, retrying until success or cancelled.
	for {
		if err := remotePushCamera(e.cam); err != nil {
			log.Warn().Err(err).Str("cam", e.cam.ID).Str("worker", e.cam.WorkerID).Msg("[counting] remote register failed, retrying in 15s")
			select {
			case <-ctx.Done():
				return
			case <-time.After(15 * time.Second):
				continue
			}
		}
		break
	}
	log.Info().Str("cam", e.cam.ID).Str("worker", e.cam.WorkerID).Msg("[counting] remote camera registered")

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	missedPolls := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			found, err := remotePollStatus(e)
			if err != nil {
				missedPolls++
				if missedPolls >= 3 {
					// Worker may have restarted — re-register.
					log.Warn().Err(err).Str("cam", e.cam.ID).Msg("[counting] remote poll failed, re-registering")
					_ = remotePushCamera(e.cam)
					missedPolls = 0
				}
			} else if !found {
				// Camera disappeared from remote (e.g. remote restarted) — re-register.
				log.Info().Str("cam", e.cam.ID).Msg("[counting] camera missing on remote, re-registering")
				_ = remotePushCamera(e.cam)
				missedPolls = 0
			} else {
				missedPolls = 0
			}
		}
	}
}

// remotePushCamera registers or updates a camera on its remote worker.
// It also fills in RTSPBase from the worker config if not already set.
func remotePushCamera(cam CameraConfig) error {
	if cam.RTSPBase == "" {
		if base := workers.GetWorkerRTSPBase(cam.WorkerID); base != "" {
			cam.RTSPBase = base
		}
	}

	body, _ := json.Marshal(cam)
	// Use PUT so it's idempotent — works for both create and update.
	resp, err := workers.RequestWorker(cam.WorkerID, http.MethodPost, "/api/counting/cameras", bytes.NewReader(body), "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("remote returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	// Also trigger start for this camera in case remote counting is enabled but
	// the camera wasn't yet active (e.g. remote restarted with counting=false).
	startResp, err2 := workers.RequestWorker(cam.WorkerID, http.MethodPost, "/api/counting/start?id="+cam.ID, nil, "")
	if err2 == nil {
		startResp.Body.Close()
	}
	return nil
}

// remoteDeleteCamera removes a camera from its remote worker.
func remoteDeleteCamera(cam CameraConfig) error {
	resp, err := workers.RequestWorker(cam.WorkerID, http.MethodDelete, "/api/counting/cameras?id="+cam.ID, nil, "")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// remotePollStatus fetches the camera's live status from its remote worker and
// updates the remoteEntry cache. Returns (found, error).
func remotePollStatus(e *remoteEntry) (bool, error) {
	resp, err := workers.RequestWorker(e.cam.WorkerID, http.MethodGet, "/api/counting/status", nil, "")
	if err != nil {
		e.mu.Lock()
		e.status.Running = false
		e.status.LastErr = err.Error()
		e.mu.Unlock()
		return false, err
	}
	defer resp.Body.Close()

	var result struct {
		Cameras []CameraStatus `json:"cameras"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}

	for _, st := range result.Cameras {
		if st.ID == e.cam.ID {
			st.WorkerID = e.cam.WorkerID // ensure WorkerID is always set
			e.mu.Lock()
			e.status = st
			e.mu.Unlock()
			return true, nil
		}
	}

	// Not found on remote.
	e.mu.Lock()
	e.status.Running = false
	e.status.LastErr = "not found on remote worker"
	e.mu.Unlock()
	return false, nil
}
