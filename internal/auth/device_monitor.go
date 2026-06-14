package auth

// device_monitor.go — Ping-based connectivity monitoring for network devices
//
// Each device is pinged every getPingIntervalSec() seconds.
// State transitions (connected→disconnected, disconnected→connected) are logged
// to device_events.jsonl as JSONL entries.
//
// Log rules:
//   - First disconnection in an outage period → log "disconnect" with timestamp
//   - First successful ping after an outage  → log "reconnect" with timestamp
//   - No intermediate entries while state is stable
//
// Ping implementation (in priority order):
//  1. Native ICMP via "udp4" socket (Linux, no root if ping_group_range allows)
//  2. Native ICMP via "ip4:icmp" raw socket (Windows admin / Linux CAP_NET_RAW)
//  3. exec.Command("ping") fallback (any OS, no privileges needed)
//
// Native mode: ONE shared socket, all goroutines call WriteTo concurrently.
// No OS processes are spawned → eliminates the process-creation overhead that
// caused false disconnects at high concurrency.

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// devHealth tracks live connectivity state for one device.
type devHealth struct {
	Connected   bool
	FailSince   time.Time // zero = currently connected
	initialized bool      // false = never pinged
}

var (
	devHealthMu  sync.RWMutex
	devHealthMap = map[string]*devHealth{} // deviceID → health
)

// devEvent is one JSONL line in device_events.jsonl.
type devEvent struct {
	Time     time.Time `json:"ts"`
	Event    string    `json:"event"` // "disconnect" | "reconnect"
	DeviceID string    `json:"device_id"`
	Name     string    `json:"name"`
	IP       string    `json:"ip"`
	TypeID   string    `json:"type_id"`
}

var (
	devEventsPath string
	devEventsMu   sync.Mutex
)

func initDeviceMonitor(eventsPath string) {
	devEventsPath = eventsPath
	initNativePinger()
	go runDeviceMonitor()
}

// ── Native in-process ICMP pinger ─────────────────────────────────────────────
// All goroutines share one PacketConn. Replies are matched back to the waiting
// goroutine via (source IP, ICMP ID, sequence number).

type icmpWaiter struct {
	ch  chan bool
	seq int
}

type nativePinger struct {
	conn    *icmp.PacketConn
	isUDP   bool // "udp4" vs "ip4:icmp" — affects address type in WriteTo/ReadFrom
	id      uint16
	seqNext atomic.Uint32
	mu      sync.Mutex
	waiters map[string]*icmpWaiter // canonical IP → active waiter
}

var globalPinger *nativePinger // nil → fall back to exec.Command

func initNativePinger() {
	// Try unprivileged UDP-ICMP first (Linux with ping_group_range configured).
	if conn, err := icmp.ListenPacket("udp4", ""); err == nil {
		p := newNativePinger(conn, true)
		go p.readLoop()
		globalPinger = p
		return
	}
	// Try raw ICMP socket (Windows admin or Linux CAP_NET_RAW).
	if conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0"); err == nil {
		p := newNativePinger(conn, false)
		go p.readLoop()
		globalPinger = p
		return
	}
	// No privileges — will fall back to exec.Command.
}

func newNativePinger(conn *icmp.PacketConn, isUDP bool) *nativePinger {
	return &nativePinger{
		conn:    conn,
		isUDP:   isUDP,
		id:      uint16(os.Getpid() & 0xffff),
		waiters: make(map[string]*icmpWaiter),
	}
}

func (p *nativePinger) readLoop() {
	buf := make([]byte, 1500)
	for {
		n, peer, err := p.conn.ReadFrom(buf)
		if err != nil {
			return
		}
		msg, err := icmp.ParseMessage(1 /* IPv4 ICMP */, buf[:n])
		if err != nil {
			continue
		}
		if msg.Type != ipv4.ICMPTypeEchoReply {
			continue
		}
		echo, ok := msg.Body.(*icmp.Echo)
		if !ok || echo.ID != int(p.id) {
			continue
		}
		var srcIP string
		switch a := peer.(type) {
		case *net.IPAddr:
			srcIP = a.IP.String()
		case *net.UDPAddr:
			srcIP = a.IP.String()
		default:
			continue
		}
		p.mu.Lock()
		w := p.waiters[srcIP]
		if w != nil && w.seq == echo.Seq {
			select {
			case w.ch <- true:
			default:
			}
		}
		p.mu.Unlock()
	}
}

func (p *nativePinger) ping(ip string, timeoutMs int) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	canonical := parsed.String()
	seq := int(p.seqNext.Add(1) & 0xffff)

	w := &icmpWaiter{ch: make(chan bool, 1), seq: seq}
	p.mu.Lock()
	p.waiters[canonical] = w
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		if p.waiters[canonical] == w {
			delete(p.waiters, canonical)
		}
		p.mu.Unlock()
	}()

	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{ID: int(p.id), Seq: seq, Data: []byte("go2rtc")},
	}
	b, err := msg.Marshal(nil)
	if err != nil {
		return false
	}

	var dst net.Addr
	if p.isUDP {
		dst = &net.UDPAddr{IP: parsed.To4()}
	} else {
		dst = &net.IPAddr{IP: parsed.To4()}
	}
	if _, err := p.conn.WriteTo(b, dst); err != nil {
		return false
	}

	select {
	case <-w.ch:
		return true
	case <-time.After(time.Duration(timeoutMs) * time.Millisecond):
		return false
	}
}

// ── Scan loop ─────────────────────────────────────────────────────────────────

func runDeviceMonitor() {
	for devStore == nil {
		time.Sleep(time.Second)
	}
	for {
		interval := time.Duration(getPingIntervalSec()) * time.Second
		pingAllDevices()
		time.Sleep(interval)
	}
}

// pingInProgress prevents overlapping scans: if a full sweep takes longer than
// the configured interval, skip the next tick rather than running concurrently.
var pingInProgress sync.Mutex

func pingAllDevices() {
	if !pingInProgress.TryLock() {
		return
	}
	defer pingInProgress.Unlock()

	devices := listDevices()
	if len(devices) == 0 {
		return
	}

	// Native mode: goroutines only block on network I/O through one shared socket
	// — high concurrency is safe. Exec mode: each goroutine spawns an OS process,
	// so we cap it tightly to avoid thrashing the process table.
	concurrency := 500
	if globalPinger == nil {
		concurrency = 80
	}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, d := range devices {
		wg.Add(1)
		sem <- struct{}{}
		go func(dev *Device) {
			defer func() { <-sem; wg.Done() }()
			ok := doPing(dev.IP, 1000)
			if !ok {
				// 300 ms pause lets transient congestion / switch rate-limits settle
				// before the confirmation ping, reducing false disconnects.
				time.Sleep(300 * time.Millisecond)
				ok = doPing(dev.IP, 1200)
			}
			applyDevHealth(dev, ok)
		}(d)
	}
	wg.Wait()
}

// doPing pings a single IP. Uses the native in-process ICMP pinger when
// available; falls back to exec.Command("ping") otherwise.
func doPing(ip string, timeoutMs int) bool {
	if globalPinger != nil {
		return globalPinger.ping(ip, timeoutMs)
	}
	return doPingExec(ip, timeoutMs)
}

func doPingExec(ip string, timeoutMs int) bool {
	timeoutSec := timeoutMs / 1000
	if timeoutSec < 1 {
		timeoutSec = 1
	}
	// Extra 500 ms so the OS context doesn't race the ping timeout.
	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(timeoutMs+500)*time.Millisecond)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "ping", "-n", "1",
			"-w", strconv.Itoa(timeoutMs), ip)
	} else {
		cmd = exec.CommandContext(ctx, "ping", "-c", "1",
			"-W", strconv.Itoa(timeoutSec), ip)
	}
	return cmd.Run() == nil
}

func applyDevHealth(dev *Device, connected bool) {
	devHealthMu.Lock()
	h := devHealthMap[dev.ID]
	if h == nil {
		h = &devHealth{}
		devHealthMap[dev.ID] = h
	}
	wasConnected := h.Connected
	wasInit := h.initialized
	h.initialized = true
	h.Connected = connected
	if connected {
		h.FailSince = time.Time{}
	} else if !wasInit || wasConnected {
		// Transition to disconnected: record start of outage.
		h.FailSince = time.Now()
	}
	devHealthMu.Unlock()

	// Log only on transitions.
	if !wasInit {
		if !connected {
			appendDevEvent(dev, "disconnect")
		}
		return
	}
	if wasConnected && !connected {
		appendDevEvent(dev, "disconnect")
	} else if !wasConnected && connected {
		appendDevEvent(dev, "reconnect")
	}
}

func appendDevEvent(dev *Device, event string) {
	e := devEvent{
		Time:     time.Now(),
		Event:    event,
		DeviceID: dev.ID,
		Name:     dev.Name,
		IP:       dev.IP,
		TypeID:   dev.TypeID,
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	devEventsMu.Lock()
	defer devEventsMu.Unlock()
	f, err := os.OpenFile(devEventsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}

// ── Stats ─────────────────────────────────────────────────────────────────────

// DeviceStatsResponse is the payload for GET /api/device-stats.
type DeviceStatsResponse struct {
	PingIntervalSec int              `json:"ping_interval_sec"`
	Types           []DeviceTypeStat `json:"types"`
	Disconnected    []DeviceFailInfo `json:"disconnected"`
}

type DeviceTypeStat struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Total        int    `json:"total"`
	Connected    int    `json:"connected"`
	Disconnected int    `json:"disconnected"`
}

type DeviceFailInfo struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	IP        string    `json:"ip"`
	TypeID    string    `json:"type_id"`
	TypeName  string    `json:"type_name"`
	FailSince time.Time `json:"fail_since"`
	FailSec   int64     `json:"fail_sec"`
}

func getDeviceStats() DeviceStatsResponse {
	types := listDeviceTypes()
	devices := listDevices()
	now := time.Now()

	typeNames := map[string]string{}
	for _, t := range types {
		typeNames[t.ID] = t.Name
	}

	typeTotals := map[string]int{}
	typeConnected := map[string]int{}
	for _, d := range devices {
		typeTotals[d.TypeID]++
	}

	devHealthMu.RLock()
	var failing []DeviceFailInfo
	for _, d := range devices {
		h := devHealthMap[d.ID]
		if h == nil || !h.initialized {
			continue
		}
		if h.Connected {
			typeConnected[d.TypeID]++
		} else {
			failing = append(failing, DeviceFailInfo{
				ID:        d.ID,
				Name:      d.Name,
				IP:        d.IP,
				TypeID:    d.TypeID,
				TypeName:  typeNames[d.TypeID],
				FailSince: h.FailSince,
				FailSec:   int64(now.Sub(h.FailSince).Seconds()),
			})
		}
	}
	devHealthMu.RUnlock()

	// Default sort: newest disconnect first (smallest FailSec at top).
	for i := 1; i < len(failing); i++ {
		for j := i; j > 0 && failing[j].FailSec < failing[j-1].FailSec; j-- {
			failing[j], failing[j-1] = failing[j-1], failing[j]
		}
	}
	if failing == nil {
		failing = []DeviceFailInfo{}
	}

	typeStats := make([]DeviceTypeStat, 0, len(types))
	for _, t := range types {
		total := typeTotals[t.ID]
		conn := typeConnected[t.ID]
		typeStats = append(typeStats, DeviceTypeStat{
			ID:           t.ID,
			Name:         t.Name,
			Total:        total,
			Connected:    conn,
			Disconnected: total - conn,
		})
	}

	return DeviceStatsResponse{
		PingIntervalSec: getPingIntervalSec(),
		Types:           typeStats,
		Disconnected:    failing,
	}
}
