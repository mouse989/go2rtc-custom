package auth

// traffic_live.go — server-side proxy for VietMap's "Live Traffic Tile"
// vector-tile API (https://traffic.vietmap.vn/{z}/{x}/{y}?api_key=...).
//
// That API is IP-allowlisted by the vendor to specific registered server
// IPs and gated by its own API key — browsers can never call it directly
// (wrong IP, and the key must never reach client code). This proxy is the
// only thing that calls it: AppSettings.VietmapLiveTrafficAPIKey is read
// server-side only (never serialized back over /api/settings — see
// api_settings.go), the server makes the allow-listed call, and relays the
// raw MVT tile bytes back to the browser behind the normal session auth
// plus the User.AllowTrafficLive permission check (middleware.go).
//
// A small in-memory cache absorbs repeat requests for the same tile from
// multiple viewers (or one viewer's periodic refresh — see map.html's
// _refreshTrafficTiles) within the vendor's documented ~120s update
// interval, so a busy deployment doesn't multiply calls to the upstream
// 1:1 with browser requests. When the local cache goes stale it still
// revalidates against upstream with If-None-Match before re-downloading,
// per the vendor's own bandwidth-saving recommendation.

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	trafficLiveCacheTTL    = 90 * time.Second // vendor: tiles refresh ~120s; stay under that
	trafficLiveCacheMaxAge = 10 * time.Minute // sweep entries untouched longer than this
)

// trafficLiveUpstreamBase is a var (not const) so tests can point it at an
// httptest.Server instead of the real upstream.
var trafficLiveUpstreamBase = "https://traffic.vietmap.vn"

type trafficLiveCacheEntry struct {
	body        []byte
	contentType string
	etag        string
	expiresAt   time.Time
	touchedAt   time.Time
}

var (
	trafficLiveCacheMu   sync.Mutex
	trafficLiveCache     = map[string]*trafficLiveCacheEntry{}
	trafficLiveSweepOnce sync.Once

	trafficLiveHTTPClient = &http.Client{Timeout: 10 * time.Second}
)

// startTrafficLiveCacheSweeper periodically drops tiles nobody has fetched
// in a while, so a server that's been up for a long time viewing many map
// areas doesn't accumulate one cache entry per distinct tile forever.
func startTrafficLiveCacheSweeper() {
	trafficLiveSweepOnce.Do(func() {
		go func() {
			for range time.Tick(5 * time.Minute) {
				cutoff := time.Now().Add(-trafficLiveCacheMaxAge)
				trafficLiveCacheMu.Lock()
				for k, e := range trafficLiveCache {
					if e.touchedAt.Before(cutoff) {
						delete(trafficLiveCache, k)
					}
				}
				trafficLiveCacheMu.Unlock()
			}
		}()
	})
}

func registerTrafficLiveHandler() {
	startTrafficLiveCacheSweeper()
	http.HandleFunc("/api/traffic-live/", trafficLiveTileHandler)
}

// trafficLiveTileHandler GET /api/traffic-live/{z}/{x}/{y}
func trafficLiveTileHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	apiKey := GetSettings().VietmapLiveTrafficAPIKey
	if apiKey == "" {
		http.Error(w, "live traffic is not configured (missing API key in Settings)", http.StatusServiceUnavailable)
		return
	}

	z, x, y, ok := parseTileXYZ(strings.TrimPrefix(r.URL.Path, "/api/traffic-live/"))
	if !ok {
		http.Error(w, "expected /api/traffic-live/{z}/{x}/{y}", http.StatusBadRequest)
		return
	}
	key := fmt.Sprintf("%d/%d/%d", z, x, y)

	entry, fresh := getTrafficLiveCacheEntry(key)
	if fresh {
		serveTrafficLiveCacheEntry(w, r, entry)
		return
	}

	fetched, status, err := fetchTrafficLiveTile(z, x, y, apiKey, entry)
	if err != nil {
		log.Warn().Err(err).Str("tile", key).Msg("[auth] live traffic tile fetch failed")
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	switch status {
	case http.StatusUnauthorized:
		http.Error(w, "live traffic API key rejected by upstream — check Settings", http.StatusBadGateway)
		return
	case http.StatusForbidden:
		http.Error(w, "this server's IP is not allow-listed for live traffic — contact VietMap", http.StatusBadGateway)
		return
	case http.StatusOK:
		// fall through
	default:
		http.Error(w, fmt.Sprintf("upstream returned HTTP %d", status), http.StatusBadGateway)
		return
	}

	putTrafficLiveCacheEntry(key, fetched)
	serveTrafficLiveCacheEntry(w, r, fetched)
}

// parseTileXYZ parses "z/x/y" (optionally with a trailing slash) into three
// non-negative integers. Validating the shape here — rather than passing
// the raw path segments through to the upstream URL — is what keeps an
// unexpected path from being interpolated into that URL at all.
func parseTileXYZ(path string) (z, x, y int, ok bool) {
	path = strings.TrimSuffix(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	var err error
	if z, err = strconv.Atoi(parts[0]); err != nil || z < 0 {
		return 0, 0, 0, false
	}
	if x, err = strconv.Atoi(parts[1]); err != nil || x < 0 {
		return 0, 0, 0, false
	}
	if y, err = strconv.Atoi(parts[2]); err != nil || y < 0 {
		return 0, 0, 0, false
	}
	return z, x, y, true
}

// getTrafficLiveCacheEntry returns the cached entry for key (if any) and
// whether it's still within its TTL. A present-but-stale entry is still
// returned (not nil) so the caller can use its ETag to revalidate instead
// of re-downloading from scratch.
func getTrafficLiveCacheEntry(key string) (*trafficLiveCacheEntry, bool) {
	trafficLiveCacheMu.Lock()
	defer trafficLiveCacheMu.Unlock()
	e, ok := trafficLiveCache[key]
	if !ok {
		return nil, false
	}
	fresh := time.Now().Before(e.expiresAt)
	if fresh {
		e.touchedAt = time.Now()
	}
	return e, fresh
}

func putTrafficLiveCacheEntry(key string, e *trafficLiveCacheEntry) {
	trafficLiveCacheMu.Lock()
	defer trafficLiveCacheMu.Unlock()
	trafficLiveCache[key] = e
}

// fetchTrafficLiveTile calls the upstream API once, sending an
// If-None-Match from prev (if any) so an unchanged tile costs a cheap 304
// instead of a full re-download. Returns the raw upstream status code for
// non-200/304 responses so the caller can tell a bad API key (401) apart
// from an un-allow-listed IP (403).
func fetchTrafficLiveTile(z, x, y int, apiKey string, prev *trafficLiveCacheEntry) (*trafficLiveCacheEntry, int, error) {
	url := fmt.Sprintf("%s/%d/%d/%d?api_key=%s", trafficLiveUpstreamBase, z, x, y, apiKey)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	if prev != nil && prev.etag != "" {
		req.Header.Set("If-None-Match", prev.etag)
	}

	resp, err := trafficLiveHTTPClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified && prev != nil {
		now := time.Now()
		refreshed := *prev
		refreshed.expiresAt = now.Add(trafficLiveCacheTTL)
		refreshed.touchedAt = now
		return &refreshed, http.StatusOK, nil
	}

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, resp.StatusCode, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20)) // 5MB: generous ceiling for one MVT tile
	if err != nil {
		return nil, 0, err
	}

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/x-protobuf"
	}

	now := time.Now()
	ttl := trafficLiveCacheTTL
	if cc := resp.Header.Get("Cache-Control"); cc != "" {
		if d, ok := parseMaxAge(cc); ok && d > 0 && d < ttl {
			ttl = d
		}
	}

	return &trafficLiveCacheEntry{
		body:        body,
		contentType: ct,
		etag:        resp.Header.Get("ETag"),
		expiresAt:   now.Add(ttl),
		touchedAt:   now,
	}, http.StatusOK, nil
}

// parseMaxAge extracts max-age=N from a Cache-Control header value.
func parseMaxAge(cacheControl string) (time.Duration, bool) {
	for _, part := range strings.Split(cacheControl, ",") {
		part = strings.TrimSpace(part)
		if v, found := strings.CutPrefix(part, "max-age="); found {
			if n, err := strconv.Atoi(v); err == nil {
				return time.Duration(n) * time.Second, true
			}
		}
	}
	return 0, false
}

func serveTrafficLiveCacheEntry(w http.ResponseWriter, r *http.Request, e *trafficLiveCacheEntry) {
	if e.etag != "" {
		w.Header().Set("ETag", e.etag)
		if inm := r.Header.Get("If-None-Match"); inm != "" && inm == e.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	remaining := time.Until(e.expiresAt)
	if remaining < 0 {
		remaining = 0
	}
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(remaining.Seconds())))
	w.Header().Set("Content-Type", e.contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(e.body)
}
