//go:build linux

package monitor

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	linuxMu      sync.Mutex
	prevCPUIdle  uint64
	prevCPUTotal uint64
	cpuInited    bool
	prevNetIn    uint64
	prevNetOut   uint64
	prevNetTime  time.Time
	netInited    bool
)

// initPlatform is a no-op on Linux; state is initialised lazily on first sample call.
func initPlatform() {}

// sampleCPU returns the CPU usage percentage since the last call.
// Returns 0 on the first call (baseline capture).
func sampleCPU() float64 {
	linuxMu.Lock()
	defer linuxMu.Unlock()
	idle, total := readCPUStat()
	if !cpuInited {
		prevCPUIdle = idle
		prevCPUTotal = total
		cpuInited = true
		return 0
	}
	dIdle := idle - prevCPUIdle
	dTotal := total - prevCPUTotal
	prevCPUIdle = idle
	prevCPUTotal = total
	if dTotal == 0 {
		return 0
	}
	return 100.0 * float64(dTotal-dIdle) / float64(dTotal)
}

// readCPUStat reads the aggregate CPU tick counters from /proc/stat.
// Returns (idle, total) where idle includes iowait.
func readCPUStat() (idle, total uint64) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		// Fields: cpu user nice system idle iowait irq softirq steal guest guest_nice
		fields := strings.Fields(line)
		for _, s := range fields[1:] {
			v, _ := strconv.ParseUint(s, 10, 64)
			total += v
		}
		if len(fields) > 4 {
			idle, _ = strconv.ParseUint(fields[4], 10, 64)
		}
		if len(fields) > 5 {
			iowait, _ := strconv.ParseUint(fields[5], 10, 64)
			idle += iowait
		}
		return
	}
	return 0, 0
}

// sampleMemory returns (total, available) RAM in bytes from /proc/meminfo.
func sampleMemory() (total, avail uint64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		val, _ := strconv.ParseUint(fields[1], 10, 64)
		val *= 1024 // kB → bytes
		switch fields[0] {
		case "MemTotal:":
			total = val
		case "MemAvailable:":
			avail = val
		}
		if total > 0 && avail > 0 {
			break
		}
	}
	return
}

// sampleDisk returns (total, free) bytes for the root filesystem.
func sampleDisk() (total, free uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err != nil {
		return 0, 0
	}
	bsize := uint64(st.Bsize)
	return st.Blocks * bsize, st.Bavail * bsize
}

// sampleUptime returns system uptime in seconds from /proc/uptime.
func sampleUptime() uint64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	secs, _ := strconv.ParseFloat(fields[0], 64)
	return uint64(secs)
}

// readNetDev sums rx/tx bytes across all non-loopback interfaces from /proc/net/dev.
func readNetDev() (inBytes, outBytes uint64) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Scan() // skip header line 1
	scanner.Scan() // skip header line 2
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(strings.TrimSpace(line), ":", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.TrimSpace(parts[0]) == "lo" {
			continue
		}
		fields := strings.Fields(parts[1])
		// /proc/net/dev rx columns: bytes packets errs drop fifo frame compressed multicast
		// /proc/net/dev tx columns: bytes packets errs drop fifo colls carrier compressed
		if len(fields) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(fields[0], 10, 64)
		tx, _ := strconv.ParseUint(fields[8], 10, 64)
		inBytes += rx
		outBytes += tx
	}
	return
}

// sampleNetwork returns (inRate, outRate) in bytes/sec since the last call.
// Returns 0,0 on the first call (baseline capture).
func sampleNetwork() (inRate, outRate uint64) {
	linuxMu.Lock()
	defer linuxMu.Unlock()
	now := time.Now()
	in, out := readNetDev()
	if !netInited {
		prevNetIn = in
		prevNetOut = out
		prevNetTime = now
		netInited = true
		return 0, 0
	}
	dt := now.Sub(prevNetTime).Seconds()
	if dt > 0 && in >= prevNetIn && out >= prevNetOut {
		inRate = uint64(float64(in-prevNetIn) / dt)
		outRate = uint64(float64(out-prevNetOut) / dt)
	}
	prevNetIn = in
	prevNetOut = out
	prevNetTime = now
	return
}
