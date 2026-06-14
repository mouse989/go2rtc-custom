//go:build windows

package monitor

import (
	"bufio"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ── CPU (GetSystemTimes) ──────────────────────────────────────────

type cpuTimes struct {
	idle, kernel, user uint64
}

var (
	cpuMu   sync.Mutex
	lastCPU cpuTimes
	lastPct float64
)

func ft2u64(ft windows.Filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}

func getSystemTimes() (idle, kernel, user windows.Filetime, ok bool) {
	r, _, _ := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	return idle, kernel, user, r != 0
}

func initPlatform() {
	// Warm-up first reading so the next call has a delta
	if idle, kernel, user, ok := getSystemTimes(); ok {
		cpuMu.Lock()
		lastCPU = cpuTimes{ft2u64(idle), ft2u64(kernel), ft2u64(user)}
		cpuMu.Unlock()
	}
	time.Sleep(200 * time.Millisecond)
}

func sampleCPU() float64 {
	idle, kernel, user, ok := getSystemTimes()
	if !ok {
		return lastPct
	}
	cur := cpuTimes{ft2u64(idle), ft2u64(kernel), ft2u64(user)}

	cpuMu.Lock()
	prev := lastCPU
	lastCPU = cur
	cpuMu.Unlock()

	dKernel := cur.kernel - prev.kernel // includes idle on Windows
	dUser   := cur.user   - prev.user
	dIdle   := cur.idle   - prev.idle
	total   := dKernel + dUser
	if total == 0 {
		return lastPct
	}
	pct := float64(total-dIdle) / float64(total) * 100
	lastPct = pct
	return pct
}

// ── Memory (GlobalMemoryStatusEx) ────────────────────────────────

type memoryStatusEx struct {
	Length                uint32
	MemoryLoad            uint32
	TotalPhys             uint64
	AvailPhys             uint64
	TotalPageFile         uint64
	AvailPageFile         uint64
	TotalVirtual          uint64
	AvailVirtual          uint64
	AvailExtendedVirtual  uint64
}

var (
	modKernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx = modKernel32.NewProc("GlobalMemoryStatusEx")
	procGetTickCount64       = modKernel32.NewProc("GetTickCount64")
	procGetSystemTimes       = modKernel32.NewProc("GetSystemTimes")
)

func sampleMemory() (total, avail uint64) {
	var ms memoryStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms)))
	return ms.TotalPhys, ms.AvailPhys
}

// ── Uptime ────────────────────────────────────────────────────────

func sampleUptime() uint64 {
	r1, _, _ := procGetTickCount64.Call()
	return uint64(r1) / 1000
}

// ── Disk (GetDiskFreeSpaceEx on C:\) ─────────────────────────────

func sampleDisk() (total, free uint64) {
	pathPtr, err := windows.UTF16PtrFromString(`C:\`)
	if err != nil {
		return 0, 0
	}
	var freeBytesAvail, totalBytes, totalFreeBytes uint64
	err = windows.GetDiskFreeSpaceEx(pathPtr, &freeBytesAvail, &totalBytes, &totalFreeBytes)
	if err != nil {
		return 0, 0
	}
	return totalBytes, totalFreeBytes
}

// ── Network (netstat -e, delta bytes/sec) ─────────────────────────

type netSnapshot struct {
	recv, sent uint64
	at         time.Time
}

var (
	netMu      sync.Mutex
	lastNetSnap netSnapshot
	lastNetIn  uint64
	lastNetOut uint64
)

func parseNetstat() (recv, sent uint64) {
	out, err := exec.Command("netstat", "-e").Output()
	if err != nil {
		return 0, 0
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "Bytes") {
			continue
		}
		// "Bytes     123456     654321"
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		recv, _ = strconv.ParseUint(fields[1], 10, 64)
		sent, _ = strconv.ParseUint(fields[2], 10, 64)
		return
	}
	return 0, 0
}

func sampleNetwork() (inRate, outRate uint64) {
	recv, sent := parseNetstat()
	now := time.Now()

	netMu.Lock()
	defer netMu.Unlock()

	if lastNetSnap.at.IsZero() {
		lastNetSnap = netSnapshot{recv, sent, now}
		return 0, 0
	}

	dt := now.Sub(lastNetSnap.at).Seconds()
	if dt <= 0 {
		return lastNetIn, lastNetOut
	}

	dRecv := recv - lastNetSnap.recv
	dSent := sent - lastNetSnap.sent
	lastNetIn  = uint64(float64(dRecv) / dt)
	lastNetOut = uint64(float64(dSent) / dt)
	lastNetSnap = netSnapshot{recv, sent, now}
	return lastNetIn, lastNetOut
}
