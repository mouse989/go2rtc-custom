package auth

// proxy.go — masked-ID proxy endpoints + rate-limited frame cache
//
// Endpoints:
//   GET /api/proxy/streams          – list user's streams with masked IDs
//   GET /api/proxy/frame?id=MASKED  – snapshot (rate-limited, cached)
//   GET /api/proxy/hls?id=MASKED    – HLS playlist
//   GET /api/proxy/mp4?id=MASKED    – MP4 stream
//   WS  /api/proxy/ws?id=MASKED     – WebSocket (WebRTC/MSE/HLS)

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"image/jpeg"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ── Snapshot JPEG disk cache ──────────────────────────────────────
//
// Every camera gets a dedicated goroutine that fetches a JPEG directly from
// the camera's HTTP endpoint every 5 s using the assigned camera type config,
// then saves the result under snapshots/{name}.jpg.
//
// All frames are served as image/jpeg, allowing universal <img> display across
// Chrome, Edge, Firefox, and iOS Safari — no codec dependency in the browser.

const snapshotDir = "snapshots"

// schedulerStats holds the most-recently-computed scheduler parameters for
// display in /api/proxy/snapshot-stats.
var (
	schedulerMu    sync.RWMutex
	schedulerDelayMs int  // ms between camera dispatches in current cycle
	schedulerConc    int  // effective concurrency cap used in current cycle
)

// ── Snapshot health tracking ──────────────────────────────────────

type cameraHealth struct {
	OK        bool
	FailSince time.Time // zero value = healthy
}

var (
	healthMu  sync.RWMutex
	healthMap = map[string]*cameraHealth{}
)

func recordHealth(name string, ok bool) {
	healthMu.Lock()
	defer healthMu.Unlock()
	h := healthMap[name]
	if h == nil {
		h = &cameraHealth{}
		healthMap[name] = h
	}
	if ok {
		h.OK = true
		h.FailSince = time.Time{}
	} else if h.OK || h.FailSince.IsZero() {
		// First failure: record start time.
		h.OK = false
		h.FailSince = time.Now()
	}
}

// ── Snapshot access log ───────────────────────────────────────────

// SnapshotSession records one GET /api/proxy/frame request.
type SnapshotSession struct {
	Time       time.Time `json:"time"`
	RemoteAddr string    `json:"remote_addr"`
	CameraName string    `json:"camera_name"`
	UserAgent  string    `json:"user_agent"`
}

const snapRingSize = 200

var (
	snapLogMu   sync.Mutex
	snapLogBuf  [snapRingSize]SnapshotSession
	snapLogHead int
	snapLogFull bool
)

func recordSnapshotAccess(remoteAddr, cameraName, userAgent string) {
	snapLogMu.Lock()
	snapLogBuf[snapLogHead] = SnapshotSession{
		Time:       time.Now(),
		RemoteAddr: remoteAddr,
		CameraName: cameraName,
		UserAgent:  userAgent,
	}
	snapLogHead++
	if snapLogHead >= snapRingSize {
		snapLogHead = 0
		snapLogFull = true
	}
	snapLogMu.Unlock()
}

// GetSnapshotSessions returns up to snapRingSize entries, most-recent first.
func GetSnapshotSessions() []SnapshotSession {
	snapLogMu.Lock()
	defer snapLogMu.Unlock()
	size := snapLogHead
	if snapLogFull {
		size = snapRingSize
	}
	out := make([]SnapshotSession, size)
	for i := 0; i < size; i++ {
		idx := (snapLogHead - 1 - i + snapRingSize) % snapRingSize
		out[i] = snapLogBuf[idx]
	}
	return out
}

// snapshotInterval returns the configured refresh interval, defaulting to 15 s.
func snapshotInterval() time.Duration {
	if s := GetSettings().SnapshotIntervalSec; s >= 1 {
		return time.Duration(s) * time.Second
	}
	return 15 * time.Second
}

// snapshotConcurrency returns the configured max concurrent fetches.
// Auto default: interval_sec × 10 (e.g. 15 s × 10 = 150).
func snapshotConcurrency() int {
	s := GetSettings()
	if s.SnapshotConcurrency >= 1 {
		return s.SnapshotConcurrency
	}
	iv := 15
	if s.SnapshotIntervalSec >= 1 {
		iv = s.SnapshotIntervalSec
	}
	n := iv * 10
	if n < 10 {
		n = 10
	}
	if n > 1000 {
		n = 1000
	}
	return n
}

// SnapshotStatsResponse is the payload for GET /api/proxy/snapshot-stats.
type SnapshotStatsResponse struct {
	IntervalSec int                    `json:"interval_sec"`
	Total       int                    `json:"total"`
	OK          int                    `json:"ok"`
	Fail        int                    `json:"fail"`
	Failing     []SnapshotFailingCamera `json:"failing"`
	// Scheduler stats
	DelayMs     int `json:"delay_ms"`     // ms between camera dispatches
	Concurrency int `json:"concurrency"`  // max concurrent fetches
}

type SnapshotFailingCamera struct {
	Name      string    `json:"name"`
	FailSince time.Time `json:"fail_since"`
	FailSec   int64     `json:"fail_sec"`
}

// placeholderJPEG is a small dark-gray JPEG served when no snapshot is yet
// available (camera offline, not yet assigned a type, first-startup cold miss).
var placeholderJPEG []byte

func init() {
	img := image.NewGray(image.Rect(0, 0, 320, 180))
	for i := range img.Pix {
		img.Pix[i] = 28 // dark gray
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 50})
	placeholderJPEG = buf.Bytes()
}

// getStreamNames is set by streams.Init() to avoid a circular import.
var getStreamNames func() []string

func SetStreamNamesProvider(f func() []string) { getStreamNames = f }

// SnapshotFilePath returns the .jpg path for a camera snapshot on disk.
// Non [a-zA-Z0-9_-] bytes are replaced with '_' for filesystem safety.
func SnapshotFilePath(streamName string) string {
	var b strings.Builder
	for _, c := range []byte(streamName) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' {
			b.WriteByte(c)
		} else {
			b.WriteByte('_')
		}
	}
	return filepath.Join(snapshotDir, b.String()+".jpg")
}

// writeSnapshotToDisk atomically saves JPEG data via temp→rename.
func writeSnapshotToDisk(streamName string, data []byte) error {
	path := SnapshotFilePath(streamName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// openSnapshot opens a .jpg file for serving. Returns error if missing or empty.
func openSnapshot(path string) (*os.File, os.FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 {
		f.Close()
		return nil, nil, fmt.Errorf("empty snapshot")
	}
	return f, fi, nil
}

// serveFromDisk writes a .jpg snapshot as image/jpeg with cache-control.
func serveFromDisk(w http.ResponseWriter, r *http.Request, f *os.File, fi os.FileInfo) {
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// fetchCameraFrameAsJPEG fetches a JPEG directly from the camera's HTTP endpoint
// using the camera type assigned in the admin UI. Returns an error when no type
// is assigned or the fetch fails; the caller writes placeholderJPEG in that case.
func fetchCameraFrameAsJPEG(ctx context.Context, streamName string) ([]byte, error) {
	data, err := fetchDirectJPEG(ctx, streamName)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("no camera type assigned for stream %q", streamName)
	}
	return data, nil
}

// ── Registration ──────────────────────────────────────────────────

// rtspListenPort holds the RTSP server port, set by rtsp.Init() via SetRTSPListenPort.
var rtspListenPort = "8554"

// SetRTSPListenPort is called from internal/rtsp during Init to publish the
// configured RTSP port so the proxy can build correct rtsp:// URLs.
func SetRTSPListenPort(port string) {
	if port != "" {
		rtspListenPort = port
	}
}

func registerProxyHandlers() {
	os.MkdirAll(snapshotDir, 0755) // ensure dir exists before any handler or worker runs
	http.HandleFunc("/api/proxy/streams",        proxyStreamsHandler)
	http.HandleFunc("/api/proxy/frame",          proxyFrameHandler)
	http.HandleFunc("/api/proxy/snapshot-stats", proxySnapshotStatsHandler)
	http.HandleFunc("/api/proxy/hls",            proxyHLSHandler)
	http.HandleFunc("/api/proxy/hls/",           proxyHLSSegmentHandler)
	http.HandleFunc("/api/proxy/mp4",            proxyPassHandler("/api/mp4", "src"))
	http.HandleFunc("/api/proxy/ws",             proxyWSHandler)
	http.HandleFunc("/api/proxy/rtsp-url",       proxyRTSPURLHandler)
	go startSnapshotWorker()
}

func proxySnapshotStatsHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok || (user.Role != RoleAdmin && !HasTab(r.Context(), TabMonitor)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	now := time.Now()
	iv := snapshotInterval()

	schedulerMu.RLock()
	delayMs := schedulerDelayMs
	conc := schedulerConc
	schedulerMu.RUnlock()

	healthMu.RLock()
	resp := SnapshotStatsResponse{
		IntervalSec: int(iv.Seconds()),
		Total:       len(healthMap),
		DelayMs:     delayMs,
		Concurrency: conc,
	}
	for name, h := range healthMap {
		if h.OK {
			resp.OK++
		} else {
			resp.Fail++
			resp.Failing = append(resp.Failing, SnapshotFailingCamera{
				Name:      name,
				FailSince: h.FailSince,
				FailSec:   int64(now.Sub(h.FailSince).Seconds()),
			})
		}
	}
	healthMu.RUnlock()

	if resp.Failing == nil {
		resp.Failing = []SnapshotFailingCamera{}
	}

	// Sort failing cameras by duration descending (longest disconnected first).
	for i := 1; i < len(resp.Failing); i++ {
		for j := i; j > 0 && resp.Failing[j].FailSec > resp.Failing[j-1].FailSec; j-- {
			resp.Failing[j], resp.Failing[j-1] = resp.Failing[j-1], resp.Failing[j]
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// startSnapshotWorker starts the global staggered snapshot scheduler.
func startSnapshotWorker() {
	for getStreamNames == nil {
		time.Sleep(time.Second)
	}
	go snapshotScheduler()
}

// snapshotScheduler is a single goroutine that cycles through all cameras,
// dispatching one fetch goroutine per camera with an even delay between each.
//
//   delay = interval / n_cameras
//   e.g. 2000 cameras × 15 s → 1 fetch every 7.5 ms
//
// This guarantees flat bandwidth with no spikes at interval boundaries.
// The camera list is re-read every cycle, so add/remove is picked up automatically.
// snapshotConcurrency() caps actual concurrent HTTP connections per cycle.
func snapshotScheduler() {
	for {
		names := getStreamNames()
		n := len(names)
		if n == 0 {
			time.Sleep(5 * time.Second)
			continue
		}

		iv := snapshotInterval()
		conc := snapshotConcurrency()
		delay := iv / time.Duration(n)
		if delay < time.Millisecond {
			delay = time.Millisecond // floor at 1 ms to avoid busy-loop
		}
		delayMs := int(delay.Milliseconds())

		schedulerMu.Lock()
		schedulerDelayMs = delayMs
		schedulerConc = conc
		schedulerMu.Unlock()

		sem := make(chan struct{}, conc)
		var wg sync.WaitGroup

		for _, name := range names {
			name := name
			sem <- struct{}{} // blocks when conc goroutines are already in-flight
			wg.Add(1)
			go func() {
				defer func() {
					<-sem
					wg.Done()
				}()

				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				data, err := fetchCameraFrameAsJPEG(ctx, name)
				cancel()

				if err == nil {
					writeSnapshotToDisk(name, data)
					recordHealth(name, true)
				} else {
					recordHealth(name, false)
					log.Debug().Err(err).Str("stream", name).Msg("[snapshot] fetch failed")
					if _, statErr := os.Stat(SnapshotFilePath(name)); os.IsNotExist(statErr) {
						writeSnapshotToDisk(name, placeholderJPEG)
					}
				}
			}()
			time.Sleep(delay)
		}
		wg.Wait() // wait for in-flight fetches before starting the next cycle
	}
}

// ── /api/proxy/streams ────────────────────────────────────────────

func proxyStreamsHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	streamIDMu.RLock()
	all := make(map[string]string, len(streamIDMap))
	for id, name := range streamIDMap {
		all[id] = name
	}
	streamIDMu.RUnlock()

	type streamInfo struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		WsURL    string `json:"ws_url"`
		HlsURL   string `json:"hls_url"`
		FrameURL string `json:"frame_url"`
		Mp4URL   string `json:"mp4_url"`
		RtspURL  string `json:"rtsp_url"`
	}

	wsScheme := "ws"
	if r.TLS != nil {
		wsScheme = "wss"
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}

	// Extract hostname (without port) for building RTSP URL
	hostname := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		hostname = h
	}

	token := extractToken(r)
	var list []streamInfo
	for id, name := range all {
		if user.Role != RoleAdmin && !contains(user.Streams, name) {
			continue
		}
		si := streamInfo{
			ID:       id,
			Name:     name,
			WsURL:    fmt.Sprintf("%s://%s/api/proxy/ws?id=%s&token=%s", wsScheme, r.Host, id, url.QueryEscape(token)),
			HlsURL:   fmt.Sprintf("%s://%s/api/proxy/hls?id=%s&token=%s", scheme, r.Host, id, url.QueryEscape(token)),
			FrameURL: fmt.Sprintf("%s://%s/api/proxy/frame?id=%s&token=%s", scheme, r.Host, id, url.QueryEscape(token)),
			Mp4URL:   fmt.Sprintf("%s://%s/api/proxy/mp4?id=%s&token=%s", scheme, r.Host, id, url.QueryEscape(token)),
			RtspURL:  fmt.Sprintf("rtsp://%s:%s/%s", hostname, rtspListenPort, id),
		}
		list = append(list, si)
	}
	if list == nil {
		list = []streamInfo{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(list)
}

// ── /api/proxy/frame ─────────────────────────────────────────────
//
// Serves a JPEG snapshot from the disk cache as image/jpeg.
// The per-camera worker pre-builds every snapshot before the first user
// request arrives, so this handler almost always returns a fast HIT.
// On a true cold-miss (worker hasn't run yet for this camera), it serves
// the placeholder JPEG immediately — the worker will overwrite it shortly.
//
// Query params:
//   id    = masked stream ID (required)
//   token = JWT token (required if no Authorization header)

func proxyFrameHandler(w http.ResponseWriter, r *http.Request) {
	maskedID := r.URL.Query().Get("id")
	if maskedID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	streamName, ok := StreamNameByID(maskedID)
	if !ok {
		http.Error(w, "stream not found", http.StatusNotFound)
		return
	}
	if !CanAccessStream(r.Context(), streamName) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Record snapshot access for the activity monitor.
	ua := r.Header.Get("User-Agent")
	addr := r.RemoteAddr
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		addr = strings.SplitN(fwd, ",", 2)[0]
	}
	recordSnapshotAccess(addr, streamName, ua)

	// Fast path: serve from disk (worker keeps this fresh every 5 s).
	path := SnapshotFilePath(streamName)
	if f, fi, err := openSnapshot(path); err == nil {
		defer f.Close()
		w.Header().Set("X-Frame-Cache", "HIT")
		serveFromDisk(w, r, f, fi)
		return
	}

	// Cold miss: worker hasn't fetched this camera yet.
	// Serve placeholder immediately so the <img> tag doesn't hang;
	// the worker will write the real JPEG within the next few seconds.
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Cache", "MISS")
	http.ServeContent(w, r, "", time.Now(), bytes.NewReader(placeholderJPEG))
}

// ── /api/proxy/hls  (HLS with m3u8 URL rewriting) ───────────────
//
// The root handler fetches the master playlist from go2rtc and rewrites
// all relative segment/sub-playlist URLs to go through our proxy, keeping
// the masked camera ID in every URL so the segment handler can authenticate.
//
// Without rewriting, the browser would resolve segment relative URLs to
// /api/proxy/segment.ts (wrong path) or hit go2rtc directly (bypasses auth).

func proxyHLSHandler(w http.ResponseWriter, r *http.Request) {
	maskedID := r.URL.Query().Get("id")
	if maskedID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	streamName, ok := StreamNameByID(maskedID)
	if !ok {
		http.Error(w, "stream not found", http.StatusNotFound)
		return
	}
	if !CanAccessStream(r.Context(), streamName) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	token := extractToken(r)

	loopURL := fmt.Sprintf("http://%s/api/hls/index.m3u8?src=%s&token=%s",
		loopbackHost(), url.QueryEscape(streamName), url.QueryEscape(token))

	req, _ := http.NewRequestWithContext(r.Context(), "GET", loopURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "HLS unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		http.Error(w, fmt.Sprintf("HLS upstream %d", resp.StatusCode), resp.StatusCode)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "read error", http.StatusBadGateway)
		return
	}

	rewritten := rewriteM3U8(string(body), maskedID, token)
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(rewritten))
}

// proxyHLSSegmentHandler handles sub-playlists and .ts/.mp4 segments.
// The browser only reaches here because rewriteM3U8 placed the absolute
// /api/proxy/hls/SEGMENT?id=MASKED&token=TOKEN URLs into the m3u8.
func proxyHLSSegmentHandler(w http.ResponseWriter, r *http.Request) {
	maskedID := r.URL.Query().Get("id")
	if maskedID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	streamName, ok := StreamNameByID(maskedID)
	if !ok {
		http.Error(w, "stream not found", http.StatusNotFound)
		return
	}
	if !CanAccessStream(r.Context(), streamName) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	token := extractToken(r)

	// Sub-path after "/api/proxy/hls" e.g. "/index0.m3u8" or "/video0/0000.ts"
	subPath := strings.TrimPrefix(r.URL.Path, "/api/proxy/hls")
	if subPath == "" {
		subPath = "/index.m3u8"
	}

	loopURL := fmt.Sprintf("http://%s/api/hls%s?src=%s&token=%s",
		loopbackHost(), subPath, url.QueryEscape(streamName), url.QueryEscape(token))

	req, _ := http.NewRequestWithContext(r.Context(), "GET", loopURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "segment unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	// If this is a sub-playlist (m3u8), also rewrite its URLs
	if strings.Contains(ct, "mpegurl") || strings.HasSuffix(subPath, ".m3u8") {
		body, _ := io.ReadAll(resp.Body)
		rewritten := rewriteM3U8(string(body), maskedID, token)
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(rewritten))
		return
	}

	// Binary segment — pipe through with original headers
	if ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// rewriteM3U8 converts relative URLs in an m3u8 playlist to absolute
// /api/proxy/hls/PATH?id=MASKED&token=TOKEN URLs.
func rewriteM3U8(content, maskedID, token string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimRight(line, "\r \t")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Parse to separate path from any existing query params (e.g. ?src=NAME)
		u, err := url.Parse(trimmed)
		if err != nil || u.Path == "" {
			continue
		}
		segPath := strings.TrimPrefix(u.Path, "./")
		lines[i] = fmt.Sprintf("/api/proxy/hls/%s?id=%s&token=%s",
			segPath, url.QueryEscape(maskedID), url.QueryEscape(token))
	}
	return strings.Join(lines, "\n")
}

// ── /api/proxy/mp4 ───────────────────────────────────────────────
//
// proxyPassHandler reverse-proxies to a real go2rtc endpoint.
// Fixes vs old version:
//   • Token forwarded in query param so loopback auth works.
//   • Authorization header kept (in case middleware also checks it).
//   • User-Agent stripped so Chrome detection doesn't apply.

func proxyPassHandler(realPath, srcParam string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		maskedID := r.URL.Query().Get("id")
		if maskedID == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		streamName, ok := StreamNameByID(maskedID)
		if !ok {
			http.Error(w, "stream not found", http.StatusNotFound)
			return
		}
		if !CanAccessStream(r.Context(), streamName) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		token := extractToken(r)
		target := &url.URL{
			Scheme:   "http",
			Host:     loopbackHost(),
			Path:     realPath,
			RawQuery: srcParam + "=" + url.QueryEscape(streamName) + "&token=" + url.QueryEscape(token),
		}
		// Forward extra query params (e.g. additional mp4 flags)
		q := r.URL.Query()
		q.Del("id"); q.Del("token")
		if extra := q.Encode(); extra != "" {
			target.RawQuery += "&" + extra
		}

		proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: target.Host})
		req := r.Clone(r.Context())
		req.URL = target
		req.Host = target.Host
		req.Header.Del("User-Agent") // prevent Chrome UA from triggering go2rtc checks
		proxy.ServeHTTP(w, req)
	}
}

// ── /api/proxy/ws (WebSocket) ─────────────────────────────────────
//
// go2rtc's WebSocket upgrader runs CheckOrigin which compares the
// Origin header against the Host header.  When we proxy from
// 125.234.114.126:21984 → 127.0.0.1:1984 the browser's Origin
// ("http://125.234.114.126:21984") doesn't match the loopback host,
// so go2rtc rejects with "request origin not allowed".
// Fix: replace Origin with the loopback origin before forwarding.

func proxyWSHandler(w http.ResponseWriter, r *http.Request) {
	maskedID := r.URL.Query().Get("id")
	if maskedID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	streamName, ok := StreamNameByID(maskedID)
	if !ok {
		http.Error(w, "stream not found", http.StatusNotFound)
		return
	}
	if !CanAccessStream(r.Context(), streamName) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	token := extractToken(r)
	lh := loopbackHost()
	target := &url.URL{
		Scheme:   "http",
		Host:     lh,
		Path:     "/api/ws",
		RawQuery: "src=" + url.QueryEscape(streamName) + "&token=" + url.QueryEscape(token),
	}

	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: lh})
	req := r.Clone(r.Context())
	req.URL = target
	req.Host = lh
	// Override Origin so go2rtc's CheckOrigin sees host == origin
	req.Header.Set("Origin", "http://"+lh)
	proxy.ServeHTTP(w, req)
}

// ── /api/proxy/rtsp-url ──────────────────────────────────────────
//
// Returns the RTSP URL for a given masked camera ID.
// The RTSP stream is served by go2rtc's built-in RTSP server using the
// masked ID as the stream path — the real stream name is never exposed.
//
// Query params:
//   id    = masked camera ID (required)
//   token = JWT token (required if no Authorization header)
//
// Response JSON:
//   stream_id   = the masked camera ID (same as ?id)
//   rtsp_url    = rtsp://HOST:PORT/STREAM_ID   (use if RTSP auth disabled)
//   rtsp_port   = RTSP server port (default 8554)
//   note        = usage hint

func proxyRTSPURLHandler(w http.ResponseWriter, r *http.Request) {
	maskedID := r.URL.Query().Get("id")
	if maskedID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	streamName, ok := StreamNameByID(maskedID)
	if !ok {
		http.Error(w, "stream not found", http.StatusNotFound)
		return
	}
	if !CanAccessStream(r.Context(), streamName) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Derive host without port from the incoming request
	hostname := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		hostname = h
	}

	rtspURL := fmt.Sprintf("rtsp://%s:%s/%s", hostname, rtspListenPort, maskedID)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"stream_id": maskedID,
		"rtsp_url":  rtspURL,
		"rtsp_port": rtspListenPort,
		"note":      "If RTSP authentication is configured in go2rtc, add credentials: rtsp://user:pass@HOST:PORT/STREAM_ID",
	})
}

// ── Helpers ───────────────────────────────────────────────────────

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// listenAddr is the configured API listen address (e.g. ":1984").
var listenAddr = ":1984"

// SetListenAddr is called from api.Init with the configured listen address.
func SetListenAddr(addr string) {
	listenAddr = addr
}

// loopbackHost returns "127.0.0.1:PORT" for loopback connections.
func loopbackHost() string {
	if len(listenAddr) > 0 && listenAddr[0] == ':' {
		return "127.0.0.1" + listenAddr
	}
	return listenAddr
}
