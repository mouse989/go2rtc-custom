package monitor

import (
	"encoding/json"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/auth"
	"github.com/AlexxIT/go2rtc/internal/counting"
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/internal/traffic"
	"github.com/rs/zerolog"
)

var log zerolog.Logger

// Stats is the full snapshot returned by /api/system/stats.
type Stats struct {
	// Sampled every 2 s by background goroutine
	CPUPercent float64 `json:"cpu_percent"`
	MemTotal   uint64  `json:"mem_total"`   // bytes
	MemUsed    uint64  `json:"mem_used"`    // bytes
	MemPercent float64 `json:"mem_percent"` // 0-100
	DiskTotal  uint64  `json:"disk_total"`  // bytes (C:\)
	DiskUsed   uint64  `json:"disk_used"`   // bytes
	UptimeSec  uint64  `json:"uptime_sec"`  // system uptime

	// Go process stats (always available)
	GoRoutines  int    `json:"goroutines"`
	GoMemAlloc  uint64 `json:"go_mem_alloc"`  // bytes currently allocated
	GoMemSys    uint64 `json:"go_mem_sys"`    // bytes from OS
	GoNumCPU    int    `json:"go_num_cpu"`
	GoVersion   string `json:"go_version"`

	// Network (bytes/sec, sampled over 2 s window)
	NetInRate  uint64 `json:"net_in_rate"`  // bytes/s total across all adapters
	NetOutRate uint64 `json:"net_out_rate"` // bytes/s total across all adapters

	// GPU stats (nil when no NVIDIA GPU / nvidia-smi present)
	GPUs []GPUInfo `json:"gpus,omitempty"`

	// Streaming / users (computed per request)
	ActiveUsers     int `json:"active_users"`     // distinct users active in last 5 min
	StreamsTotal    int `json:"streams_total"`    // configured cameras
	StreamsActive   int `json:"streams_active"`   // cameras with >=1 viewer right now
	StreamConsumers int `json:"stream_consumers"` // total viewing sessions

	// Counting cameras on this server
	CamerasTotal    int `json:"cameras_total"`    // counting cameras configured (local + remote delegation)
	CamerasAnalyzing int `json:"cameras_analyzing"` // cameras actively analyzing on this server

	// Traffic history storage (computed per request)
	TrafficFiles int   `json:"traffic_files"` // daily history files on disk
	TrafficBytes int64 `json:"traffic_bytes"` // their total size

	// Server start time
	StartTime int64 `json:"start_time"` // unix timestamp
	Timestamp int64 `json:"timestamp"`
}

var (
	mu       sync.RWMutex
	latest   Stats
	startTime = time.Now()
)

// Init registers the /api/system/stats endpoint and starts the background poller.
func Init() {
	log = app.GetLogger("monitor")

	// Prime the CPU sampler (two readings needed for delta)
	initPlatform()

	// Background goroutine samples every 2 seconds
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			sample()
		}
	}()

	// Register HTTP handlers
	http.HandleFunc("/api/system/stats", statsHandler)
	http.HandleFunc("/api/system/activity", activityHandler)
	log.Info().Msg("[monitor] ready")
}

func sample() {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	cpu := sampleCPU()
	memTotal, memAvail := sampleMemory()
	diskTotal, diskFree := sampleDisk()
	uptime := sampleUptime()
	netIn, netOut := sampleNetwork()
	gpus := sampleGPU()

	memUsed := memTotal - memAvail
	memPct := 0.0
	if memTotal > 0 {
		memPct = float64(memUsed) / float64(memTotal) * 100
	}

	mu.Lock()
	latest = Stats{
		CPUPercent:  cpu,
		MemTotal:    memTotal,
		MemUsed:     memUsed,
		MemPercent:  memPct,
		DiskTotal:   diskTotal,
		DiskUsed:    diskTotal - diskFree,
		UptimeSec:   uptime,
		GoRoutines:  runtime.NumGoroutine(),
		GoMemAlloc:  ms.Alloc,
		GoMemSys:    ms.Sys,
		GoNumCPU:    runtime.NumCPU(),
		GoVersion:   runtime.Version(),
		NetInRate:   netIn,
		NetOutRate:  netOut,
		GPUs:        gpus,
		StartTime:   startTime.Unix(),
		Timestamp:   time.Now().Unix(),
	}
	mu.Unlock()
}

// activityHandler GET /api/system/activity
// Returns active users and per-stream consumer counts.
func activityHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	caller, ok := auth.UserFromContext(r.Context())
	if !ok || (caller.Role != auth.RoleAdmin && !auth.HasTab(r.Context(), auth.TabMonitor)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	type response struct {
		Users            []auth.ActiveUserDetail  `json:"users"`
		Streams          []streams.StreamDetail   `json:"streams"`
		SnapshotSessions []auth.SnapshotSession   `json:"snapshot_sessions"`
	}
	resp := response{
		Users:            auth.ActiveUserDetails(5 * time.Minute),
		Streams:          streams.GetStreamDetails(),
		SnapshotSessions: auth.GetSnapshotSessions(),
	}
	if resp.Users == nil {
		resp.Users = []auth.ActiveUserDetail{}
	}
	if resp.Streams == nil {
		resp.Streams = []streams.StreamDetail{}
	}
	if resp.SnapshotSessions == nil {
		resp.SnapshotSessions = []auth.SnapshotSession{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func statsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	caller, ok := auth.UserFromContext(r.Context())
	if !ok || (caller.Role != auth.RoleAdmin && !auth.HasTab(r.Context(), auth.TabMonitor)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	mu.RLock()
	s := latest
	mu.RUnlock()

	// Always fresh Go stats
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s.GoRoutines = runtime.NumGoroutine()
	s.GoMemAlloc = ms.Alloc
	s.Timestamp = time.Now().Unix()

	// Streaming / user counters (cheap, computed per request)
	s.ActiveUsers = auth.ActiveUsers(5 * time.Minute)
	s.StreamsTotal, s.StreamsActive, s.StreamConsumers = streams.GetStreamStats()
	s.TrafficFiles, s.TrafficBytes = traffic.HistoryStats()
	s.CamerasTotal, s.CamerasAnalyzing = counting.GetLocalStats()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s)
}
