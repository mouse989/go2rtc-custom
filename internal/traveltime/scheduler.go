package traveltime

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/auth"
)

var (
	schedMu   sync.Mutex
	schedStop chan struct{}

	schedState struct {
		mu          sync.RWMutex
		running     bool
		lastRun     time.Time
		nextRun     time.Time
		lastStatus  string
		successRuns int
		errorCount  int
	}
)

func isRunning() bool {
	schedMu.Lock()
	defer schedMu.Unlock()
	return schedStop != nil
}

func startScheduler() {
	schedMu.Lock()
	defer schedMu.Unlock()
	if schedStop != nil {
		return
	}
	ch := make(chan struct{})
	schedStop = ch
	go runScheduler(ch)
	log.Info().Msg("[traveltime] scheduler started")
}

func stopScheduler() {
	schedMu.Lock()
	ch := schedStop
	schedStop = nil
	schedMu.Unlock()
	if ch != nil {
		close(ch)
		schedState.mu.Lock()
		schedState.running = false
		schedState.mu.Unlock()
		log.Info().Msg("[traveltime] scheduler stopped")
	}
}

func runScheduler(stop chan struct{}) {
	schedState.mu.Lock()
	schedState.running = true
	schedState.mu.Unlock()

	safeRun := func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error().Interface("panic", r).Msg("[traveltime] runOnce panic recovered")
			}
		}()
		runOnce()
	}

	safeRun()

	for {
		cfgMu.RLock()
		interval := cfg.IntervalMin
		cfgMu.RUnlock()
		if interval <= 0 {
			interval = 15
		}
		dur := time.Duration(interval) * time.Minute
		next := time.Now().Add(dur)
		schedState.mu.Lock()
		schedState.nextRun = next
		schedState.mu.Unlock()

		select {
		case <-stop:
			return
		case <-time.After(dur):
			safeRun()
		}
	}
}

// RunNow fires a single collection cycle in a goroutine.
func RunNow() {
	go runOnce()
}

func runOnce() {
	apiKey := auth.GetSettings().VietmapAPIKey
	if apiKey == "" {
		setStatus("⚠️ Chưa cấu hình VietMap API Key trong Settings")
		return
	}

	routes := listRoutes()
	if len(routes) == 0 {
		setStatus("⚠️ Chưa có tuyến đường nào")
		return
	}

	log.Info().Int("routes", len(routes)).Msg("[traveltime] collecting")
	setStatus("⏳ Đang thu thập...")
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)

	client := &http.Client{Timeout: 15 * time.Second}
	ctx := context.Background()

	var entries []LogEntry
	errors := 0

	for _, r := range routes {
		stats, err := getRouteStats(ctx, client, apiKey, r)
		if err != nil {
			errors++
			log.Error().Err(err).Str("route", r.Name).Msg("[traveltime] collect failed")
			continue
		}
		wp := r.Waypoints
		if wp == "" {
			wp = "None"
		}
		entries = append(entries, LogEntry{
			Timestamp:   timestamp,
			RouteID:     r.ID,
			RouteName:   r.Name,
			Origin:      r.Origin,
			Destination: r.Destination,
			Waypoints:   wp,
			LengthM:     stats.LengthM,
			DurationSec: stats.DurationSec,
			BaseSec:     stats.BaseSec,
			DelaySec:    stats.DelaySec,
			TTI:         stats.TTI,
		})
		log.Info().Str("route", r.Name).Float64("tti", stats.TTI).Msg("[traveltime] collected")
	}

	if len(entries) > 0 {
		if err := appendLogs(entries); err != nil {
			log.Error().Err(err).Msg("[traveltime] log write failed")
		}
		cfgMu.RLock()
		gsURL := cfg.GoogleScriptURL
		cfgMu.RUnlock()
		if gsURL != "" {
			go pushToGoogleSheet(gsURL, timestamp, entries)
		}
	}

	schedState.mu.Lock()
	schedState.lastRun = time.Now()
	if errors > 0 {
		schedState.errorCount += errors
	}
	schedState.successRuns++
	n := schedState.successRuns
	schedState.mu.Unlock()

	msg := fmt.Sprintf("✅ %d/%d tuyến · lần #%d", len(entries), len(routes), n)
	if errors > 0 {
		msg = fmt.Sprintf("⚠️ %d ok, %d lỗi · lần #%d", len(entries), errors, n)
	}
	setStatus(msg)
	log.Info().Str("status", msg).Msg("[traveltime] done")
}

func setStatus(msg string) {
	schedState.mu.Lock()
	schedState.lastStatus = msg
	schedState.mu.Unlock()
}

// SchedulerStatus is returned by the status API endpoint.
type SchedulerStatus struct {
	Running     bool   `json:"running"`
	IntervalMin int    `json:"intervalMin"`
	LastRun     string `json:"lastRun,omitempty"`
	NextRun     string `json:"nextRun,omitempty"`
	LastStatus  string `json:"lastStatus"`
	SuccessRuns int    `json:"successRuns"`
	ErrorCount  int    `json:"errorCount"`
}

func getSchedulerStatus() SchedulerStatus {
	schedState.mu.RLock()
	defer schedState.mu.RUnlock()

	cfgMu.RLock()
	interval := cfg.IntervalMin
	cfgMu.RUnlock()

	s := SchedulerStatus{
		Running:     schedState.running,
		IntervalMin: interval,
		LastStatus:  schedState.lastStatus,
		SuccessRuns: schedState.successRuns,
		ErrorCount:  schedState.errorCount,
	}
	l := loc()
	if !schedState.lastRun.IsZero() {
		s.LastRun = schedState.lastRun.In(l).Format(time.RFC3339)
	}
	if !schedState.nextRun.IsZero() {
		s.NextRun = schedState.nextRun.In(l).Format(time.RFC3339)
	}
	return s
}
