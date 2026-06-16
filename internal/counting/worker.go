package counting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// CameraWorker polls the Python YOLO service for a single camera.
type CameraWorker struct {
	cam    CameraConfig
	store  *dailyStore
	client *http.Client

	// per-direction totals — read under Manager.mu
	totalDown, totalUp, totalRight, totalLeft int
	total                                     int
	totalCar, totalMotorcycle, totalBus, totalTruck int
	framesProcessed                           int
	lastFrameAt                               int64 // unix seconds
	lastErr                                   string
	startedAt                                 int64 // unix seconds

	// incremental event polling: last event timestamp seen
	lastEventTs int64

	// debug: last annotated JPEG from the Python service
	debugMu   sync.Mutex
	debugJPEG []byte
}

func newCameraWorker(cam CameraConfig, store *dailyStore) *CameraWorker {
	return &CameraWorker{
		cam:       cam,
		store:     store,
		client:    &http.Client{Timeout: 5 * time.Second},
		startedAt: time.Now().Unix(),
	}
}

func (w *CameraWorker) yoloBaseURL() string {
	u := getConfig().YoloURL
	if u == "" {
		u = "http://localhost:8765"
	}
	return u
}

// run is the main loop for a single camera. It exits when ctx is cancelled.
func (w *CameraWorker) run(ctx context.Context) {
	// Register with YOLO service, retrying until success or context cancelled.
	for {
		if err := w.registerCamera(); err != nil {
			w.lastErr = fmt.Sprintf("YOLO service unavailable: %v", err)
			log.Debug().Str("cam", w.cam.ID).Err(err).Msg("[counting] register retry")
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		w.lastErr = ""
		break
	}

	syncTicker := time.NewTicker(2 * time.Second)
	defer syncTicker.Stop()
	debugTicker := time.NewTicker(1 * time.Second)
	defer debugTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.unregisterCamera()
			return
		case <-syncTicker.C:
			w.syncStats()
			w.pollEvents()
		case <-debugTicker.C:
			w.refreshDebug()
		}
	}
}

// registerCamera sends the camera config to the YOLO service.
func (w *CameraWorker) registerCamera() error {
	c := getConfig()
	conf := c.YoloConf
	if conf <= 0 {
		conf = 0.35
	}
	fw := c.FrameWidth
	if fw <= 0 {
		fw = 320
	}
	fps := w.cam.FPS
	if fps <= 0 {
		fps = c.DefaultFPS
	}

	body := map[string]any{
		"id":         w.cam.ID,
		"name":       w.cam.Name,
		"streamName": w.cam.StreamName,
		"lineHPos":   w.cam.LineHPos,
		"lineVPos":   w.cam.LineVPos,
		"countDown":  w.cam.CountDown,
		"countUp":    w.cam.CountUp,
		"countRight": w.cam.CountRight,
		"countLeft":  w.cam.CountLeft,
		"fps":        fps,
		"tier":       w.cam.Tier,
		"frameWidth": fw,
		"yoloConf":   conf,
	}

	data, _ := json.Marshal(body)
	url := fmt.Sprintf("%s/cameras/%s", w.yoloBaseURL(), w.cam.ID)
	resp, err := w.client.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("YOLO service returned %d", resp.StatusCode)
	}
	return nil
}

// unregisterCamera tells the YOLO service to stop processing this camera.
func (w *CameraWorker) unregisterCamera() {
	url := fmt.Sprintf("%s/cameras/%s", w.yoloBaseURL(), w.cam.ID)
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return
	}
	resp, err := w.client.Do(req)
	if err != nil {
		log.Debug().Str("cam", w.cam.ID).Err(err).Msg("[counting] unregister")
		return
	}
	resp.Body.Close()
}

// yoloStatus is the shape returned by GET /cameras on the Python service.
type yoloStatus struct {
	Total           int     `json:"total"`
	TotalDown       int     `json:"totalDown"`
	TotalUp         int     `json:"totalUp"`
	TotalRight      int     `json:"totalRight"`
	TotalLeft       int     `json:"totalLeft"`
	TotalCar        int     `json:"totalCar"`
	TotalMotorcycle int     `json:"totalMotorcycle"`
	TotalBus        int     `json:"totalBus"`
	TotalTruck      int     `json:"totalTruck"`
	FramesProcessed int     `json:"framesProcessed"`
	LastFrameAt     float64 `json:"lastFrameAt"`
	LastErr         string  `json:"lastErr"`
	Running         bool    `json:"running"`
}

// syncStats fetches live stats from the YOLO service and updates the worker.
func (w *CameraWorker) syncStats() {
	url := w.yoloBaseURL() + "/cameras"
	resp, err := w.client.Get(url)
	if err != nil {
		w.lastErr = fmt.Sprintf("YOLO service unavailable: %v", err)
		return
	}
	defer resp.Body.Close()

	var all map[string]yoloStatus
	if err := json.NewDecoder(resp.Body).Decode(&all); err != nil {
		w.lastErr = fmt.Sprintf("YOLO stats decode error: %v", err)
		return
	}

	st, ok := all[w.cam.ID]
	if !ok {
		// Service may have restarted — re-register
		log.Info().Str("cam", w.cam.ID).Msg("[counting] camera missing from YOLO, re-registering")
		if err := w.registerCamera(); err != nil {
			w.lastErr = fmt.Sprintf("YOLO service unavailable: %v", err)
		}
		return
	}

	w.total = st.Total
	w.totalDown = st.TotalDown
	w.totalUp = st.TotalUp
	w.totalRight = st.TotalRight
	w.totalLeft = st.TotalLeft
	w.totalCar = st.TotalCar
	w.totalMotorcycle = st.TotalMotorcycle
	w.totalBus = st.TotalBus
	w.totalTruck = st.TotalTruck
	w.framesProcessed = st.FramesProcessed
	w.lastFrameAt = int64(st.LastFrameAt)
	if st.LastErr != "" {
		w.lastErr = st.LastErr
	} else {
		w.lastErr = ""
	}
}

// yoloEvent is one entry from GET /events on the Python service.
type yoloEvent struct {
	Ts           float64 `json:"ts"`
	CameraID     string  `json:"cameraId"`
	Name         string  `json:"name"`
	Count        int     `json:"count"`
	Total        int     `json:"total"`
	Dir          string  `json:"dir"`
	VehicleClass string  `json:"vehicleClass"`
}

// pollEvents fetches new events from the YOLO service since the last seen timestamp.
func (w *CameraWorker) pollEvents() {
	url := fmt.Sprintf("%s/events?camera=%s&since=%d&limit=50",
		w.yoloBaseURL(), w.cam.ID, w.lastEventTs)

	resp, err := w.client.Get(url)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var events []yoloEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return
	}

	c := getConfig()
	// Events arrive newest-first from Python. Snapshot prevTs before the loop so
	// ALL events newer than prevTs are accepted (not just the first/newest one).
	prevTs := w.lastEventTs

	for _, ev := range events {
		ts := int64(ev.Ts)
		if ts <= prevTs {
			continue
		}
		if ts > w.lastEventTs {
			w.lastEventTs = ts
		}

		ce := CountEvent{
			Timestamp:    ts,
			CameraID:     ev.CameraID,
			Name:         ev.Name,
			Count:        ev.Count,
			Total:        ev.Total,
			Direction:    ev.Dir,
			VehicleClass: ev.VehicleClass,
		}
		evRing.add(ce)
		if c.Storage.Enabled {
			_ = w.store.append(ce)
		}
	}
}

// refreshDebug fetches the latest annotated JPEG from the YOLO service.
func (w *CameraWorker) refreshDebug() {
	url := fmt.Sprintf("%s/debug/%s", w.yoloBaseURL(), w.cam.ID)
	resp, err := w.client.Get(url)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil || len(data) == 0 {
		return
	}

	w.debugMu.Lock()
	w.debugJPEG = data
	w.debugMu.Unlock()
}
