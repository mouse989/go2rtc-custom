package counting

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
)

// CameraWorker processes a single camera stream for vehicle counting.
type CameraWorker struct {
	cam     CameraConfig
	bg      *bgModel
	tracker *Tracker
	counter *LineCrossCounter
	store   *dailyStore
	client  *http.Client

	// live stats — read under Manager.mu
	total           int
	framesProcessed int
	lastFrameAt     int64  // unix seconds
	lastErr         string
	startedAt       int64  // unix seconds
}

func newCameraWorker(cam CameraConfig, store *dailyStore) *CameraWorker {
	c := getConfig()
	lp := cam.LinePos
	if lp <= 0 {
		lp = 0.5
	}
	axis := cam.LineAxis
	if axis == "" {
		axis = "h"
	}
	return &CameraWorker{
		cam:       cam,
		bg:        newBGModel(c.LearningRate, c.Threshold),
		tracker:   newTracker(),
		counter:   &LineCrossCounter{LinePos: lp, LineAxis: axis},
		store:     store,
		client:    &http.Client{Timeout: 10 * time.Second},
		startedAt: time.Now().Unix(),
	}
}

// run is the main loop for a single camera. It exits when ctx is cancelled.
func (w *CameraWorker) run(ctx context.Context) {
	fps := w.cam.FPS
	if fps <= 0 {
		fps = getConfig().DefaultFPS
	}
	interval := time.Duration(float64(time.Second) / fps)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.processFrame(); err != nil {
				w.lastErr = err.Error()
				log.Debug().Str("cam", w.cam.ID).Err(err).Msg("[counting] frame error")
			} else {
				w.lastErr = ""
				w.framesProcessed++
				w.lastFrameAt = time.Now().Unix()
			}
		}
	}
}

// processFrame fetches one JPEG frame and runs the full counting pipeline.
func (w *CameraWorker) processFrame() error {
	c := getConfig()
	fw := c.FrameWidth
	if fw <= 0 {
		fw = 320
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/api/frame.jpeg?src=%s&width=%d",
		api.Port, w.cam.StreamName, fw)

	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("X-Internal", "counting")

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("frame endpoint status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	img, err := jpeg.Decode(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("jpeg decode: %w", err)
	}

	w.processImage(img)
	return nil
}

func (w *CameraWorker) processImage(img image.Image) {
	c := getConfig()

	gray, fw, fh := toGrayscale(img)

	mask := w.bg.apply(gray, fw, fh)
	if mask == nil {
		return
	}

	mask = morphOpen(mask, fw, fh)
	blobs := findBlobs(mask, fw, fh, c.BlobMinArea, c.BlobMaxArea)
	tracks := w.tracker.Update(blobs)

	n := w.counter.Process(tracks, fw, fh)
	if n > 0 {
		w.total += n
		ev := CountEvent{
			Timestamp: time.Now().Unix(),
			CameraID:  w.cam.ID,
			Name:      w.cam.Name,
			Count:     n,
			Total:     w.total,
		}
		evRing.add(ev)
		if c.Storage.Enabled {
			_ = w.store.append(ev)
		}
		log.Debug().Str("cam", w.cam.ID).Int("crossed", n).Int("total", w.total).Msg("[counting]")
	}
}
