package counting

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/auth"
)

// CameraWorker processes a single camera stream for vehicle counting.
type CameraWorker struct {
	cam     CameraConfig
	bg      *bgModel
	tracker *Tracker
	counter *LineCrossCounter
	store   *dailyStore

	// snapshot file mtime tracking — avoids reprocessing the same image
	lastMtime time.Time

	// per-direction totals — read under Manager.mu
	totalDown, totalUp, totalRight, totalLeft int
	total                                     int
	framesProcessed                           int
	lastFrameAt                               int64 // unix seconds
	lastErr                                   string
	startedAt                                 int64 // unix seconds

	// debug: last annotated frame as JPEG
	debugMu   sync.Mutex
	debugJPEG []byte
}

func newCameraWorker(cam CameraConfig, store *dailyStore) *CameraWorker {
	c := getConfig()
	lhp := cam.LineHPos
	// Default: horizontal line at 50% if no directions are configured
	if lhp <= 0 && !cam.CountRight && !cam.CountLeft {
		lhp = 0.5
	}
	countDown := cam.CountDown
	countUp := cam.CountUp
	// If no direction flags set at all, default to counting both directions
	if !cam.CountDown && !cam.CountUp && !cam.CountRight && !cam.CountLeft {
		countDown = true
		countUp = true
	}
	return &CameraWorker{
		cam:  cam,
		bg:   newBGModel(c.LearningRate, c.Threshold),
		tracker: newTracker(),
		counter: &LineCrossCounter{
			LineHPos:   lhp,
			LineVPos:   cam.LineVPos,
			CountDown:  countDown,
			CountUp:    countUp,
			CountRight: cam.CountRight,
			CountLeft:  cam.CountLeft,
		},
		store:     store,
		startedAt: time.Now().Unix(),
	}
}

// run is the main loop for a single camera. It exits when ctx is cancelled.
func (w *CameraWorker) run(ctx context.Context) {
	fps := w.cam.FPS
	if fps <= 0 {
		fps = getConfig().DefaultFPS
	}
	// Tier reduces processing rate to save CPU on lower-priority cameras
	switch w.cam.Tier {
	case 2:
		fps /= 2
	case 3:
		fps /= 4
	}
	if fps < 0.05 {
		fps = 0.05
	}
	interval := time.Duration(float64(time.Second) / fps)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			processed, err := w.processFrame()
			if err != nil {
				w.lastErr = err.Error()
				log.Debug().Str("cam", w.cam.ID).Err(err).Msg("[counting] frame error")
			} else {
				w.lastErr = ""
				if processed {
					w.framesProcessed++
					w.lastFrameAt = time.Now().Unix()
				}
			}
		}
	}
}

// processFrame reads the latest snapshot from disk and runs the counting pipeline.
// Returns (true, nil) when a new frame was processed, (false, nil) when the snapshot
// hasn't changed yet, or (false, err) on failure.
func (w *CameraWorker) processFrame() (bool, error) {
	path := auth.SnapshotFilePath(w.cam.StreamName)
	fi, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("snapshot not found (%s): %w", path, err)
	}

	// Skip if the file hasn't been updated since last processing
	if !fi.ModTime().After(w.lastMtime) {
		return false, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read snapshot: %w", err)
	}

	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return false, fmt.Errorf("jpeg decode: %w", err)
	}

	w.lastMtime = fi.ModTime()
	w.processImage(img)
	return true, nil
}

func (w *CameraWorker) processImage(img image.Image) {
	c := getConfig()

	gray, fw, fh := toGrayscale(img)

	mask := w.bg.apply(gray, fw, fh)
	if mask == nil {
		// First frame — initializing background model, nothing to count yet
		w.saveDebugFrame(gray, nil, nil, fw, fh)
		return
	}

	mask = morphOpen(mask, fw, fh)
	blobs := findBlobs(mask, fw, fh, c.BlobMinArea, c.BlobMaxArea)
	tracks := w.tracker.Update(blobs)
	result := w.counter.Process(tracks, fw, fh)

	w.saveDebugFrame(gray, mask, tracks, fw, fh)

	if result.Total == 0 {
		return
	}

	ts := time.Now().Unix()

	emitDir := func(count int, dir string) {
		if count <= 0 {
			return
		}
		w.total += count
		ev := CountEvent{
			Timestamp: ts,
			CameraID:  w.cam.ID,
			Name:      w.cam.Name,
			Count:     count,
			Total:     w.total,
			Direction: dir,
		}
		evRing.add(ev)
		if c.Storage.Enabled {
			_ = w.store.append(ev)
		}
	}

	w.totalDown += result.Down
	w.totalUp += result.Up
	w.totalRight += result.Right
	w.totalLeft += result.Left

	emitDir(result.Down, "down")
	emitDir(result.Up, "up")
	emitDir(result.Right, "right")
	emitDir(result.Left, "left")

	log.Debug().Str("cam", w.cam.ID).
		Int("down", result.Down).Int("up", result.Up).
		Int("right", result.Right).Int("left", result.Left).
		Int("total", w.total).Msg("[counting]")
}

// saveDebugFrame builds and stores an annotated JPEG for the /api/counting/debug endpoint.
func (w *CameraWorker) saveDebugFrame(gray, mask []byte, tracks []*Track, fw, fh int) {
	out := image.NewRGBA(image.Rect(0, 0, fw, fh))

	// Grayscale base
	for y := 0; y < fh; y++ {
		for x := 0; x < fw; x++ {
			g := gray[y*fw+x]
			out.SetRGBA(x, y, color.RGBA{R: g, G: g, B: g, A: 255})
		}
	}

	// Foreground mask pixels in red tint
	if mask != nil {
		for y := 0; y < fh; y++ {
			for x := 0; x < fw; x++ {
				if mask[y*fw+x] > 0 {
					g := gray[y*fw+x]
					out.SetRGBA(x, y, color.RGBA{R: 220, G: g / 3, B: g / 3, A: 255})
				}
			}
		}
	}

	// Horizontal counting line (bright green) with direction arrows
	if w.counter.LineHPos > 0 {
		lineY := int(w.counter.LineHPos * float64(fh))
		if lineY >= fh {
			lineY = fh - 1
		}
		for x := 0; x < fw; x++ {
			out.SetRGBA(x, lineY, color.RGBA{R: 0, G: 230, B: 0, A: 255})
		}
		if w.counter.CountDown {
			drawArrow(out, fw/4, lineY, 0, 10)
		}
		if w.counter.CountUp {
			drawArrow(out, fw*3/4, lineY, 0, -10)
		}
	}

	// Vertical counting line (cyan) with direction arrows
	if w.counter.LineVPos > 0 {
		lineX := int(w.counter.LineVPos * float64(fw))
		if lineX >= fw {
			lineX = fw - 1
		}
		for y := 0; y < fh; y++ {
			out.SetRGBA(lineX, y, color.RGBA{R: 0, G: 220, B: 220, A: 255})
		}
		if w.counter.CountRight {
			drawArrow(out, lineX, fh/4, 10, 0)
		}
		if w.counter.CountLeft {
			drawArrow(out, lineX, fh*3/4, -10, 0)
		}
	}

	// Active track centroids as yellow 5×5 dots
	for _, tr := range tracks {
		cx, cy := int(tr.X), int(tr.Y)
		dot := color.RGBA{R: 255, G: 220, B: 0, A: 255}
		for dy := -2; dy <= 2; dy++ {
			for dx := -2; dx <= 2; dx++ {
				px, py := cx+dx, cy+dy
				if px >= 0 && px < fw && py >= 0 && py < fh {
					out.SetRGBA(px, py, dot)
				}
			}
		}
	}

	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, out, &jpeg.Options{Quality: 75})

	w.debugMu.Lock()
	w.debugJPEG = buf.Bytes()
	w.debugMu.Unlock()
}

// drawArrow draws a directional arrow tip at (cx,cy) pointing in direction (dx,dy).
func drawArrow(img *image.RGBA, cx, cy, dx, dy int) {
	col := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	b := img.Bounds()
	set := func(x, y int) {
		if x >= b.Min.X && x < b.Max.X && y >= b.Min.Y && y < b.Max.Y {
			img.SetRGBA(x, y, col)
		}
	}
	// Shaft
	for i := 1; i <= 5; i++ {
		set(cx-dx*i/5, cy-dy*i/5)
	}
	// Arrowhead
	if dy != 0 {
		tip := cy + dy
		set(cx, tip)
		set(cx-1, tip-dy)
		set(cx+1, tip-dy)
	} else {
		tip := cx + dx
		set(tip, cy)
		set(tip-dx, cy-1)
		set(tip-dx, cy+1)
	}
}
