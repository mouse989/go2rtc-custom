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
	HTTPPort     int    `json:"http_port"`      // 0 → default 80
	ONVIF        bool   `json:"onvif"`          // auto-discover snapshot URL via ONVIF
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

func getCameraTypeAssignments() map[string]string {
	ctStore.mu.RLock()
	defer ctStore.mu.RUnlock()
	cp := make(map[string]string, len(ctStore.data.Assignments))
	for k, v := range ctStore.data.Assignments {
		cp[k] = v
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

	ctStore.mu.Lock()
	ctStore.data.Assignments = assignments
	err := ctStore.save()
	ctStore.mu.Unlock()
	return err
}

// ── Direct HTTP snapshot ──────────────────────────────────────────

// fetchDirectJPEG tries to fetch a JPEG directly from the camera's HTTP endpoint
// using the camera type assigned to this stream. Returns (nil, nil) when no type
// is assigned so the caller can fall through to the ffmpeg path transparently.
func fetchDirectJPEG(ctx context.Context, streamName string) ([]byte, error) {
	if ctStore == nil {
		return nil, nil
	}

	// Resolve the assigned camera type.
	ctStore.mu.RLock()
	typeID := ctStore.data.Assignments[streamName]
	var ct *CameraType
	for _, t := range ctStore.data.Types {
		if t.ID == typeID {
			cp := *t
			ct = &cp
			break
		}
	}
	ctStore.mu.RUnlock()

	if ct == nil {
		return nil, nil // no type assigned → transparent fallthrough
	}

	if getStreamSources == nil {
		return nil, fmt.Errorf("stream source provider not available")
	}
	sources := getStreamSources(streamName)
	if len(sources) == 0 {
		return nil, fmt.Errorf("no configured source for stream %q", streamName)
	}

	// Use first source (typically rtsp://user:pass@host:port/path).
	srcURL := sources[0]
	if !strings.Contains(srcURL, "://") {
		return nil, fmt.Errorf("source URL has no scheme: %s", srcURL)
	}

	u, err := url.Parse(srcURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("cannot parse source URL %q: %v", srcURL, err)
	}

	host, _, _ := net.SplitHostPort(u.Host)
	if host == "" {
		host = u.Host
	}
	creds := u.User

	port := ct.HTTPPort
	if port <= 0 {
		port = 80
	}

	if ct.ONVIF {
		snapshotURL, err := getONVIFSnapshotURI(ctx, streamName, host, port, creds)
		if err != nil {
			return nil, fmt.Errorf("ONVIF discovery for %q: %w", streamName, err)
		}
		// Credentials are already embedded in snapshotURL by getONVIFSnapshotURI.
		return fetchHTTPJPEG(ctx, snapshotURL, nil)
	}

	snapshotURL := fmt.Sprintf("http://%s:%d%s", host, port, ct.SnapshotPath)
	return fetchHTTPJPEG(ctx, snapshotURL, creds)
}

// fetchHTTPJPEG GETs a URL and validates that the response is JPEG.
func fetchHTTPJPEG(ctx context.Context, rawURL string, creds *url.Userinfo) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	if creds != nil {
		pass, _ := creds.Password()
		req.SetBasicAuth(creds.Username(), pass)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, rawURL)
	}
	data, err := io.ReadAll(resp.Body)
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
