package monitor

import (
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GPUInfo holds runtime stats for a single GPU returned by nvidia-smi.
type GPUInfo struct {
	Name        string  `json:"name"`
	Utilization float64 `json:"utilization"` // 0-100%
	MemUsed     uint64  `json:"mem_used"`    // bytes
	MemTotal    uint64  `json:"mem_total"`   // bytes
	MemPercent  float64 `json:"mem_percent"` // 0-100%
}

var (
	gpuMu         sync.Mutex
	cachedGPUs    []GPUInfo
	lastGPUSample time.Time
	gpuUnavail    bool      // nvidia-smi not present; retry after gpuNextRetry
	gpuNextRetry  time.Time
)

// sampleGPU queries nvidia-smi for per-GPU utilization and VRAM usage.
// Returns nil when no NVIDIA GPU / nvidia-smi is present.
// Results are cached for 2 s to match the main sample loop cadence.
// On failure, backs off for 60 s before retrying.
func sampleGPU() []GPUInfo {
	gpuMu.Lock()
	defer gpuMu.Unlock()

	now := time.Now()

	if gpuUnavail {
		if now.Before(gpuNextRetry) {
			return nil
		}
		gpuUnavail = false // retry window elapsed
	}

	if !lastGPUSample.IsZero() && now.Sub(lastGPUSample) < 2*time.Second {
		return cachedGPUs
	}

	out, err := exec.Command("nvidia-smi",
		"--query-gpu=name,utilization.gpu,memory.used,memory.total",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		gpuUnavail = true
		gpuNextRetry = now.Add(60 * time.Second)
		cachedGPUs = nil
		return nil
	}

	var gpus []GPUInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// nvidia-smi uses ", " (comma-space) as separator
		fields := strings.Split(line, ", ")
		if len(fields) < 4 {
			continue
		}
		name := strings.TrimSpace(fields[0])
		util, _ := strconv.ParseFloat(strings.TrimSpace(fields[1]), 64)
		memUsedMiB, _ := strconv.ParseUint(strings.TrimSpace(fields[2]), 10, 64)
		memTotalMiB, _ := strconv.ParseUint(strings.TrimSpace(fields[3]), 10, 64)
		memUsed := memUsedMiB * 1024 * 1024
		memTotal := memTotalMiB * 1024 * 1024
		memPct := 0.0
		if memTotal > 0 {
			memPct = float64(memUsed) / float64(memTotal) * 100
		}
		gpus = append(gpus, GPUInfo{
			Name:        name,
			Utilization: util,
			MemUsed:     memUsed,
			MemTotal:    memTotal,
			MemPercent:  memPct,
		})
	}
	cachedGPUs = gpus
	lastGPUSample = now
	return gpus
}
