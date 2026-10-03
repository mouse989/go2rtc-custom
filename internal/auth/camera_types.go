package auth

// camera_types.go — per-vendor snapshot configuration + direct HTTP fetch
//
// Flow:
//   Admin assigns each stream to a "camera type" (e.g. Hikvision, Dahua).
//   Each type stores the HTTP snapshot path and optional ONVIF flag.
//
//   fetchDirectJPEG() uses the stream's RTSP source URL to extract host +
//   credentials, then fetches JPEG via HTTP — bypassing go2rtc and ffmpeg.
//   For ONVIF types, the snapshot URI is discovered via ONVIF GetSnapshotUri
//   and cached per stream.
//
//   Returns (nil, nil) when no type is assigned → caller falls through to ffmpeg.
//   Returns (nil, err) when type is assigned but fetch failed → caller logs + falls through.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/AlexxIT/go2rtc/pkg/onvif"
)

// CameraType describes how to capture a JPEG snapshot for a vendor/model family.
type CameraType struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	SnapshotPath string `json:"snapshot_path"` // e.g. /ISAPI/Streaming/channels/101/picture
	HTTPPort     int    `json:"http_port"`     // 0 → default 80, or 443 when HTTPS is set
	HTTPS        bool   `json:"https"`         // snapshot endpoint is HTTPS (self-signed certs tolerated — see cameraHTTPClient)
	ONVIF        bool   `json:"onvif"`         // auto-discover snapshot URL via ONVIF
	RTSP         bool   `json:"rtsp"`          // grab frame from go2rtc's RTSP stream via loopback

	// PTZEnabled/PTZDriver configure pan/tilt/zoom control — entirely
	// independent of the snapshot fields above (a camera's snapshot method
	// and its PTZ method are unrelated capabilities; see internal/auth/ptz.go,
	// which never reads SnapshotPath/HTTPS/ONVIF/RTSP). Credentials are not
	// configured here either: both PTZ drivers reuse resolveStreamHostCreds
	// below, the same RTSP-source-URL-derived host/user/pass the snapshot
	// path already uses.
	PTZEnabled bool   `json:"ptz_enabled"`
	PTZDriver  string `json:"ptz_driver,omitempty"` // "axis_vapix" | "onvif"
}

type cameraTypesData struct {
	Types       []*CameraType     `json:"types"`
	Assignments map[string]string `json:"assignments"` // streamName → typeID
}

type cameraTypeStore struct {
	mu   sync.RWMutex
	path string
	data cameraTypesData
}

var ctStore *cameraTypeStore

// onvifSnapshotCache caches the ONVIF-discovered snapshot URI per stream name.
var (
	onvifCacheMu sync.RWMutex
	onvifCache   = map[string]string{}
)

// getStreamSources is wired by streams.Init() via SetStreamSourcesProvider
// to return the configured source URLs (e.g. rtsp://…) for a given stream name.
var getStreamSources func(streamName string) []string

// SetStreamSourcesProvider connects the stream source resolver from the streams
// package without creating an import cycle (auth ← streams ← auth).
func SetStreamSourcesProvider(f func(streamName string) []string) {
	getStreamSources = f
}

func initCameraTypes(path string) error {
	ctStore = &cameraTypeStore{path: path}
	return ctStore.load()
}

func (s *cameraTypeStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		s.data = cameraTypesData{Types: []*CameraType{}, Assignments: map[string]string{}}
		return nil
	}
	if err != nil {
		return err
	}
	if err = json.Unmarshal(data, &s.data); err != nil {
		return err
	}
	if s.data.Assignments == nil {
		s.data.Assignments = map[string]string{}
	}
	if s.data.Types == nil {
		s.data.Types = []*CameraType{}
	}
	return nil
}

func (s *cameraTypeStore) save() error {
	data, err := json.MarshalIndent(&s.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0600)
}

// ── CRUD ─────────────────────────────────────────────────────────

func listCameraTypes() []*CameraType {
	ctStore.mu.RLock()
	defer ctStore.mu.RUnlock()
	out := make([]*CameraType, len(ctStore.data.Types))
	for i, t := range ctStore.data.Types {
		cp := *t
		out[i] = &cp
	}
	return out
}

func upsertCameraType(t *CameraType) error {
	ctStore.mu.Lock()
	defer ctStore.mu.Unlock()
	for i, existing := range ctStore.data.Types {
		if existing.ID == t.ID {
			cp := *t
			ctStore.data.Types[i] = &cp
			return ctStore.save()
		}
	}
	cp := *t
	ctStore.data.Types = append(ctStore.data.Types, &cp)
	return ctStore.save()
}

func deleteCameraType(id string) error {
	ctStore.mu.Lock()
	defer ctStore.mu.Unlock()
	for i, t := range ctStore.data.Types {
		if t.ID == id {
			ctStore.data.Types = append(ctStore.data.Types[:i], ctStore.data.Types[i+1:]...)
			for k, v := range ctStore.data.Assignments {
				if v == id {
					delete(ctStore.data.Assignments, k)
				}
			}
			return ctStore.save()
		}
	}
	return nil
}

// getCameraTypeAssignments returns the current stream→type map, pruned to
// streams that still exist in go2rtc.yaml. Assignments are never actively
// removed when a stream is deleted or renamed in the config, so without
// this filter a camera fleet that's been reconfigured over time
// accumulates orphaned entries here forever — invisible in the Stream
// Assignments table (which only ever lists current streams), but still
// counted into each Camera Type's "Cameras" badge (renderCtTable sums
// this map by type ID), so the per-type counts can add up to more than
// the actual number of configured cameras. Filtering here — the one place
// both the admin's "Cameras" badges and the "chưa gán" count ultimately
// read from — keeps both numbers honest against the same live stream
// list, and the pruned result the admin's next "Save Assignments" click
// sends back as a PUT self-heals the stored file, no separate cleanup
// step needed.
func getCameraTypeAssignments() map[string]string {
	ctStore.mu.RLock()
	defer ctStore.mu.RUnlock()
	cp := make(map[string]string, len(ctStore.data.Assignments))
	for k, v := range ctStore.data.Assignments {
		cp[k] = v
	}

	if getStreamNames != nil {
		live := make(map[string]bool)
		for _, name := range getStreamNames() {
			live[name] = true
		}
		for k := range cp {
			if !live[k] {
				delete(cp, k)
			}
		}
	}
	return cp
}

func setCameraTypeAssignments(assignments map[string]string) error {
	// Invalidate ONVIF cache for streams whose type changed or was removed.
	ctStore.mu.RLock()
	old := ctStore.data.Assignments
	ctStore.mu.RUnlock()

	onvifCacheMu.Lock()
	for stream, typeID := range assignments {
		if old[stream] != typeID {
			delete(onvifCache, stream)
		}
	}
	for stream := range old {
		if _, exists := assignments[stream]; !exists {
			delete(onvifCache, stream)
		}
	}
	onvifCacheMu.Unlock()

	// Same invalidation for the PTZ ONVIF client cache (ptz_onvif.go) — a
	// reassigned stream may now point at a different camera entirely.
	onvifPTZCacheMu.Lock()
	for stream, typeID := range assignments {
		if old[stream] != typeID {
			delete(onvifPTZCache, stream)
		}
	}
	for stream := range old {
		if _, exists := assignments[stream]; !exists {
			delete(onvifPTZCache, stream)
		}
	}
	onvifPTZCacheMu.Unlock()

	ctStore.mu.Lock()
	ctStore.data.Assignments = assignments
	err := ctStore.save()
	ctStore.mu.Unlock()
	return err
}

// cameraTypeForStream returns a copy of the CameraType assigned to
// streamName, or nil if none is assigned (or the store isn't initialized).
// Shared by the snapshot path below, the PTZ dispatcher (ptz.go), and the
// /api/proxy/streams "ptz" flag (proxy.go).
func cameraTypeForStream(streamName string) *CameraType {
	if ctStore == nil {
		return nil
	}
	ctStore.mu.RLock()
	defer ctStore.mu.RUnlock()
	typeID := ctStore.data.Assignments[streamName]
	for _, t := range ctStore.data.Types {
		if t.ID == typeID {
			cp := *t
			return &cp
		}
	}
	return nil
}

// ── Direct HTTP snapshot ──────────────────────────────────────────

// fetchDirectJPEG tries to fetch a JPEG directly from the camera's HTTP endpoint
// using the camera type assigned to this stream. Returns (nil, nil) when no type
// is assigned so the caller can fall through to the ffmpeg path transparently.
func fetchDirectJPEG(ctx context.Context, streamName string) ([]byte, error) {
	ct := cameraTypeForStream(streamName)
	if ct == nil {
		return nil, nil // no type assigned → transparent fallthrough
	}

	// RTSP-only type: grab a keyframe from go2rtc's internal loopback API.
	// go2rtc must already have the stream active (it always does for configured streams).
	// keepalive=1 trades reconnecting every snapshot cycle for holding the
	// RTSP connection open between polls — which means receiving the
	// camera's full continuous stream the whole time, not just a brief
	// per-poll burst, so it's opt-in (see AppSettings.SnapshotRTSPKeepAlive
	// and internal/mjpeg/keepalive.go) rather than always on.
	if ct.RTSP {
		snapshotURL := fmt.Sprintf("http://%s/api/frame.jpeg?src=%s",
			loopbackHost(), url.QueryEscape(streamName))
		if GetSettings().SnapshotRTSPKeepAlive {
			snapshotURL += "&keepalive=1"
		}
		return fetchLoopbackJPEG(ctx, snapshotURL)
	}

	host, creds, err := resolveStreamHostCreds(streamName)
	if err != nil {
		return nil, err
	}

	port := ct.HTTPPort
	if port <= 0 {
		if ct.HTTPS {
			port = 443
		} else {
			port = 80
		}
	}

	if ct.ONVIF {
		// ONVIF device discovery itself is always plain HTTP here — cameras
		// exposing ONVIF over HTTPS are rare and not what ct.HTTPS is for
		// (that's the direct snapshot-path fetch below).
		snapshotURL, err := getONVIFSnapshotURI(ctx, streamName, host, port, creds)
		if err != nil {
			return nil, fmt.Errorf("ONVIF discovery for %q: %w", streamName, err)
		}
		// Credentials are already embedded in snapshotURL by getONVIFSnapshotURI.
		return fetchHTTPJPEG(ctx, snapshotURL, nil)
	}

	scheme := "http"
	if ct.HTTPS {
		scheme = "https"
	}
	snapshotURL := fmt.Sprintf("%s://%s:%d%s", scheme, host, port, ct.SnapshotPath)
	return fetchHTTPJPEG(ctx, snapshotURL, creds)
}

// resolveStreamHostCreds extracts the host (without port) and embedded
// credentials from streamName's first configured source URL (typically
// rtsp://user:pass@host:port/path) — shared by the snapshot fetch above and
// by internal/auth/ptz.go, since both assume the camera uses the same
// account for RTSP, HTTP snapshot, and PTZ control (confirmed true for the
// Bosch/Axis cameras this is built for; see ptz.go's doc comment).
func resolveStreamHostCreds(streamName string) (host string, creds *url.Userinfo, err error) {
	if getStreamSources == nil {
		return "", nil, fmt.Errorf("stream source provider not available")
	}
	sources := getStreamSources(streamName)
	if len(sources) == 0 {
		return "", nil, fmt.Errorf("no configured source for stream %q", streamName)
	}

	// Use first source (typically rtsp://user:pass@host:port/path).
	srcURL := sources[0]
	if !strings.Contains(srcURL, "://") {
		return "", nil, fmt.Errorf("source URL has no scheme: %s", srcURL)
	}

	u, err := url.Parse(srcURL)
	if err != nil || u.Host == "" {
		return "", nil, fmt.Errorf("cannot parse source URL %q: %v", srcURL, err)
	}

	host = u.Host
	if h, _, splitErr := net.SplitHostPort(u.Host); splitErr == nil {
		host = h
	}
	return host, u.User, nil
}

// fetchHTTPWithDigestRetry GETs rawURL, authenticating with Basic first (so
// the common case stays a single round trip); a camera endpoint that only
// accepts Digest answers that with 401 + WWW-Authenticate: Digest, and this
// retries once with a computed Digest response, reusing the same
// buildDigestAuth this package already has for the camera bulk-config
// feature (api_camera_config.go). creds may be nil (URL with no
// embedded/assigned credentials, or an ONVIF-discovered snapshot URI that
// embeds them in the URL itself); the Digest retry then falls back to
// whatever userinfo is embedded in rawURL, since Digest can't be satisfied
// by Go's automatic "URL userinfo → Basic auth" behavior the way Basic can.
// Shared by fetchHTTPJPEG (below) and ptz_axis.go's VAPIX driver — both talk
// to the same kind of camera HTTP endpoint, just validate the response body
// differently (JPEG magic bytes vs. VAPIX's plain-text "OK").
func fetchHTTPWithDigestRetry(ctx context.Context, rawURL string, creds *url.Userinfo) ([]byte, error) {
	resp, err := doHTTPGet(ctx, rawURL, creds, "")
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()

		if !strings.HasPrefix(challenge, "Digest ") {
			return nil, fmt.Errorf("HTTP 401 from %s", rawURL)
		}

		digestCreds := creds
		if digestCreds == nil {
			if u, err := url.Parse(rawURL); err == nil {
				digestCreds = u.User
			}
		}
		if digestCreds == nil {
			return nil, fmt.Errorf("HTTP 401 (Digest) from %s: no credentials to answer with", rawURL)
		}
		pass, _ := digestCreds.Password()

		reqURI := rawURL
		if u, err := url.Parse(rawURL); err == nil {
			reqURI = u.RequestURI()
		}
		authHeader := buildDigestAuth(challenge, http.MethodGet, reqURI, digestCreds.Username(), pass)

		resp, err = doHTTPGet(ctx, rawURL, nil, authHeader)
		if err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, rawURL)
	}
	return io.ReadAll(resp.Body)
}

// fetchHTTPJPEG GETs a URL and validates that the response is JPEG.
func fetchHTTPJPEG(ctx context.Context, rawURL string, creds *url.Userinfo) ([]byte, error) {
	data, err := fetchHTTPWithDigestRetry(ctx, rawURL, creds)
	if err != nil {
		return nil, err
	}
	if len(data) < 2 {
		return nil, fmt.Errorf("response too small (%d bytes)", len(data))
	}
	// JPEG SOI marker.
	if data[0] != 0xFF || data[1] != 0xD8 {
		return nil, fmt.Errorf("response is not JPEG (got %02X%02X)", data[0], data[1])
	}
	return data, nil
}

// cameraHTTPClient fetches camera HTTP(S) snapshot endpoints (both the
// direct ct.SnapshotPath fetch and ONVIF-discovered URIs). IP cameras that
// serve their web/snapshot interface over HTTPS almost universally use a
// self-signed certificate — no public CA can issue one for a private LAN
// IP — so certificate verification is skipped here, the same trade-off
// cmd/https-proxy makes per-site via its own InsecureSkipVerify option.
// Skipping verification is a no-op for the (still far more common) plain
// HTTP snapshot fetches this client also handles.
var cameraHTTPClient = &http.Client{
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	},
}

// doHTTPGet issues one GET, setting Basic auth from creds when given, or the
// literal Authorization header value in authHeader when given (mutually
// exclusive — authHeader is used for the computed Digest retry).
func doHTTPGet(ctx context.Context, rawURL string, creds *url.Userinfo, authHeader string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	} else if creds != nil {
		pass, _ := creds.Password()
		req.SetBasicAuth(creds.Username(), pass)
	}
	return cameraHTTPClient.Do(req)
}

// fetchLoopbackJPEG fetches a JPEG from a loopback go2rtc API endpoint,
// bypassing auth middleware by adding the X-Internal header.
func fetchLoopbackJPEG(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Internal", "counting")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from loopback %s", resp.StatusCode, rawURL)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if len(data) < 2 {
		return nil, fmt.Errorf("loopback response too small (%d bytes)", len(data))
	}
	if data[0] != 0xFF || data[1] != 0xD8 {
		return nil, fmt.Errorf("loopback response is not JPEG (got %02X%02X)", data[0], data[1])
	}
	return data, nil
}

// getONVIFSnapshotURI discovers the snapshot URL via ONVIF protocol and caches it.
// It uses pkg/onvif.NewClient which handles WS-Security auth internally.
func getONVIFSnapshotURI(ctx context.Context, streamName, host string, httpPort int, creds *url.Userinfo) (string, error) {
	onvifCacheMu.RLock()
	cached, ok := onvifCache[streamName]
	onvifCacheMu.RUnlock()
	if ok {
		return cached, nil
	}

	// Build ONVIF base URL: http://user:pass@host:port/?subtype=0&snapshot
	// NewClient uses this to derive device service URL and auth credentials.
	var rawURL string
	if creds != nil {
		rawURL = fmt.Sprintf("http://%s@%s:%d/?subtype=0&snapshot", creds.String(), host, httpPort)
	} else {
		rawURL = fmt.Sprintf("http://%s:%d/?subtype=0&snapshot", host, httpPort)
	}

	type result struct {
		uri string
		err error
	}
	ch := make(chan result, 1)

	go func() {
		client, err := onvif.NewClient(rawURL)
		if err != nil {
			ch <- result{err: fmt.Errorf("connect: %w", err)}
			return
		}
		uri, err := client.GetURI()
		if err != nil {
			ch <- result{err: fmt.Errorf("GetURI: %w", err)}
			return
		}
		ch <- result{uri: uri}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			return "", r.err
		}
		onvifCacheMu.Lock()
		onvifCache[streamName] = r.uri
		onvifCacheMu.Unlock()
		return r.uri, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
