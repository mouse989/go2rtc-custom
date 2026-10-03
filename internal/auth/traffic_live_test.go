package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseTileXYZ(t *testing.T) {
	cases := []struct {
		path                string
		wantZ, wantX, wantY int
		wantOK              bool
	}{
		{"14/13048/7699", 14, 13048, 7699, true},
		{"14/13048/7699/", 14, 13048, 7699, true}, // trailing slash tolerated
		{"0/0/0", 0, 0, 0, true},
		{"14/13048", 0, 0, 0, false},        // too few segments
		{"14/13048/7699/9", 0, 0, 0, false}, // too many segments
		{"a/b/c", 0, 0, 0, false},           // non-numeric
		{"-1/0/0", 0, 0, 0, false},          // negative not allowed
		{"", 0, 0, 0, false},
	}
	for _, c := range cases {
		z, x, y, ok := parseTileXYZ(c.path)
		if ok != c.wantOK {
			t.Errorf("parseTileXYZ(%q) ok = %v, want %v", c.path, ok, c.wantOK)
			continue
		}
		if ok && (z != c.wantZ || x != c.wantX || y != c.wantY) {
			t.Errorf("parseTileXYZ(%q) = %d/%d/%d, want %d/%d/%d", c.path, z, x, y, c.wantZ, c.wantX, c.wantY)
		}
	}
}

func TestParseMaxAge(t *testing.T) {
	cases := []struct {
		cc     string
		want   time.Duration
		wantOK bool
	}{
		{"max-age=120", 120 * time.Second, true},
		{"public, max-age=60, stale-while-revalidate=30", 60 * time.Second, true},
		{"no-store", 0, false},
		{"", 0, false},
		{"max-age=notanumber", 0, false},
	}
	for _, c := range cases {
		got, ok := parseMaxAge(c.cc)
		if ok != c.wantOK || (ok && got != c.want) {
			t.Errorf("parseMaxAge(%q) = %v,%v want %v,%v", c.cc, got, ok, c.want, c.wantOK)
		}
	}
}

// withTrafficLiveUpstream points trafficLiveUpstreamBase at an
// httptest.Server for the duration of the test, restoring it afterward, and
// also resets the shared cache map so tests don't see each other's entries.
func withTrafficLiveUpstream(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	orig := trafficLiveUpstreamBase
	trafficLiveUpstreamBase = srv.URL
	t.Cleanup(func() { trafficLiveUpstreamBase = orig })

	trafficLiveCacheMu.Lock()
	trafficLiveCache = map[string]*trafficLiveCacheEntry{}
	trafficLiveCacheMu.Unlock()
}

func TestTrafficLiveTileHandlerNotConfigured(t *testing.T) {
	initTestSettings(t)

	req := httptest.NewRequest(http.MethodGet, "/api/traffic-live/14/13048/7699", nil)
	w := httptest.NewRecorder()
	trafficLiveTileHandler(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 with no API key configured, got %d", w.Code)
	}
}

func TestTrafficLiveTileHandlerBadPath(t *testing.T) {
	initTestSettings(t)
	if err := UpdateSettings(AppSettings{VietmapLiveTrafficAPIKey: "test-key"}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/traffic-live/not-a-tile", nil)
	w := httptest.NewRecorder()
	trafficLiveTileHandler(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a malformed tile path, got %d", w.Code)
	}
}

func TestTrafficLiveTileHandlerRelaysUpstreamTile(t *testing.T) {
	initTestSettings(t)
	if err := UpdateSettings(AppSettings{VietmapLiveTrafficAPIKey: "test-key"}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	var gotAPIKey string
	var hits int
	withTrafficLiveUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		gotAPIKey = r.URL.Query().Get("api_key")
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Cache-Control", "max-age=120")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fake-mvt-bytes"))
	})

	req := httptest.NewRequest(http.MethodGet, "/api/traffic-live/14/13048/7699", nil)
	w := httptest.NewRecorder()
	trafficLiveTileHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	if w.Body.String() != "fake-mvt-bytes" {
		t.Fatalf("expected tile bytes relayed verbatim, got %q", w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/x-protobuf" {
		t.Fatalf("expected Content-Type relayed, got %q", ct)
	}
	if gotAPIKey != "test-key" {
		t.Fatalf("expected server-side api_key passed upstream, got %q", gotAPIKey)
	}
	if hits != 1 {
		t.Fatalf("expected exactly 1 upstream hit, got %d", hits)
	}

	// Second request within the cache TTL must be served from cache, not
	// hit the upstream again.
	req2 := httptest.NewRequest(http.MethodGet, "/api/traffic-live/14/13048/7699", nil)
	w2 := httptest.NewRecorder()
	trafficLiveTileHandler(w2, req2)
	if w2.Code != http.StatusOK || w2.Body.String() != "fake-mvt-bytes" {
		t.Fatalf("expected cached 200 with the same bytes, got %d %q", w2.Code, w2.Body.String())
	}
	if hits != 1 {
		t.Fatalf("expected the cache to absorb the second request (still 1 upstream hit), got %d", hits)
	}
}

func TestTrafficLiveTileHandlerClientConditionalGet(t *testing.T) {
	initTestSettings(t)
	if err := UpdateSettings(AppSettings{VietmapLiveTrafficAPIKey: "test-key"}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	withTrafficLiveUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fake-mvt-bytes"))
	})

	// Prime the cache.
	req := httptest.NewRequest(http.MethodGet, "/api/traffic-live/14/13048/7699", nil)
	w := httptest.NewRecorder()
	trafficLiveTileHandler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("priming request: expected 200, got %d", w.Code)
	}

	// A client request carrying the matching If-None-Match gets 304, no body.
	req2 := httptest.NewRequest(http.MethodGet, "/api/traffic-live/14/13048/7699", nil)
	req2.Header.Set("If-None-Match", `"v1"`)
	w2 := httptest.NewRecorder()
	trafficLiveTileHandler(w2, req2)
	if w2.Code != http.StatusNotModified {
		t.Fatalf("expected 304 for a matching If-None-Match, got %d", w2.Code)
	}
	if w2.Body.Len() != 0 {
		t.Fatalf("expected an empty body on 304, got %q", w2.Body.String())
	}
}

func TestTrafficLiveTileHandlerRevalidatesStaleEntryWith304(t *testing.T) {
	initTestSettings(t)
	if err := UpdateSettings(AppSettings{VietmapLiveTrafficAPIKey: "test-key"}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	var gotIfNoneMatch string
	var hits int
	withTrafficLiveUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		gotIfNoneMatch = r.Header.Get("If-None-Match")
		if gotIfNoneMatch == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fake-mvt-bytes"))
	})

	key := "14/13048/7699"
	// Seed an already-expired cache entry directly so the handler treats it
	// as stale and revalidates, without waiting out the real TTL.
	putTrafficLiveCacheEntry(key, &trafficLiveCacheEntry{
		body:        []byte("fake-mvt-bytes"),
		contentType: "application/x-protobuf",
		etag:        `"v1"`,
		expiresAt:   time.Now().Add(-time.Second),
		touchedAt:   time.Now().Add(-time.Minute),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/traffic-live/"+key, nil)
	w := httptest.NewRecorder()
	trafficLiveTileHandler(w, req)

	if hits != 1 {
		t.Fatalf("expected exactly 1 upstream revalidation request, got %d", hits)
	}
	if gotIfNoneMatch != `"v1"` {
		t.Fatalf("expected the stale entry's ETag sent as If-None-Match, got %q", gotIfNoneMatch)
	}
	if w.Code != http.StatusOK || w.Body.String() != "fake-mvt-bytes" {
		t.Fatalf("expected the old cached body served after a 304 revalidation, got %d %q", w.Code, w.Body.String())
	}
}

func TestTrafficLiveTileHandlerUpstreamAuthErrors(t *testing.T) {
	initTestSettings(t)
	if err := UpdateSettings(AppSettings{VietmapLiveTrafficAPIKey: "wrong-key"}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	cases := []struct {
		upstreamStatus int
	}{
		{http.StatusUnauthorized},
		{http.StatusForbidden},
		{http.StatusInternalServerError},
	}
	for _, c := range cases {
		withTrafficLiveUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.upstreamStatus)
		})
		req := httptest.NewRequest(http.MethodGet, "/api/traffic-live/14/13048/7699", nil)
		w := httptest.NewRecorder()
		trafficLiveTileHandler(w, req)
		if w.Code != http.StatusBadGateway {
			t.Errorf("upstream %d: expected the proxy to report 502, got %d", c.upstreamStatus, w.Code)
		}
	}
}

func TestUserCanAccessPathTrafficLive(t *testing.T) {
	gated := &User{Role: RoleViewer, AllowTrafficLive: true}
	if !userCanAccessPath(gated, "/api/traffic-live/14/13048/7699") {
		t.Fatal("a viewer with AllowTrafficLive should reach /api/traffic-live/*")
	}
	ungated := &User{Role: RoleViewer, AllowTrafficLive: false}
	if userCanAccessPath(ungated, "/api/traffic-live/14/13048/7699") {
		t.Fatal("a viewer without AllowTrafficLive must not reach /api/traffic-live/*")
	}
}
