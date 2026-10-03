package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCameraHostForStream(t *testing.T) {
	withMockStreams(t, map[string][]string{
		"rtsp-cam":     {"rtsp://admin:pass@10.0.1.5:554/Streaming/1"},
		"http-cam":     {"http://10.0.1.6:8080/snapshot.jpg"},
		"no-port-cam":  {"rtsp://10.0.1.7/live"},
		"no-scheme":    {"10.0.1.8:554"},
		"empty-source": {},
	})

	cases := []struct {
		name string
		want string
	}{
		{"rtsp-cam", "10.0.1.5"},
		{"http-cam", "10.0.1.6"},
		{"no-port-cam", "10.0.1.7"},
		{"no-scheme", ""},    // no "://" — can't tell host from the rest
		{"empty-source", ""}, // no configured source at all
		{"unknown-stream", ""},
	}
	for _, c := range cases {
		if got := cameraHostForStream(c.name); got != c.want {
			t.Errorf("cameraHostForStream(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCameraHostForStreamNoProvider(t *testing.T) {
	saved := getStreamSources
	getStreamSources = nil
	t.Cleanup(func() { getStreamSources = saved })

	if got := cameraHostForStream("anything"); got != "" {
		t.Fatalf("expected \"\" when getStreamSources is nil, got %q", got)
	}
}

// withStubPing replaces doPing for the duration of the test with a
// deterministic function over a fixed ip→reachable map, so tests never hit
// real ICMP/exec.Command("ping").
func withStubPing(t *testing.T, reachable map[string]bool) {
	t.Helper()
	saved := doPing
	doPing = func(ip string, timeoutMs int) bool { return reachable[ip] }
	t.Cleanup(func() { doPing = saved })
}

// resetHealthMap clears the shared cameraHealth map so snapshot-ping tests
// don't see entries left behind by other tests/handlers in this package.
func resetHealthMap(t *testing.T) {
	t.Helper()
	healthMu.Lock()
	saved := healthMap
	healthMap = map[string]*cameraHealth{}
	healthMu.Unlock()
	t.Cleanup(func() {
		healthMu.Lock()
		healthMap = saved
		healthMu.Unlock()
	})
}

func doSnapshotPingRequest(t *testing.T) []SnapshotPingResult {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/proxy/snapshot-ping", nil)
	admin := &User{Username: "admin", Role: RoleAdmin}
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, admin))
	w := httptest.NewRecorder()
	proxySnapshotPingHandler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	var results []SnapshotPingResult
	if err := json.Unmarshal(w.Body.Bytes(), &results); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return results
}

func TestSnapshotPingHandlerDistinguishesReachableFromDown(t *testing.T) {
	resetHealthMap(t)
	withMockStreams(t, map[string][]string{
		"cam-reachable":   {"rtsp://10.0.1.5:554/live"},
		"cam-unreachable": {"rtsp://10.0.1.6:554/live"},
		"cam-no-ip":       {"not-a-url-with-a-host"},
		"cam-healthy":     {"rtsp://10.0.1.9:554/live"},
	})
	withStubPing(t, map[string]bool{
		"10.0.1.5": true,  // camera answers ping — snapshot failure is NOT a network outage
		"10.0.1.6": false, // camera doesn't answer — genuinely disconnected
	})

	now := time.Now()
	healthMu.Lock()
	healthMap["cam-reachable"] = &cameraHealth{OK: false, FailSince: now.Add(-time.Minute)}
	healthMap["cam-unreachable"] = &cameraHealth{OK: false, FailSince: now.Add(-2 * time.Minute)}
	healthMap["cam-no-ip"] = &cameraHealth{OK: false, FailSince: now.Add(-time.Minute)}
	healthMap["cam-healthy"] = &cameraHealth{OK: true} // currently fine — must not be pinged/included
	healthMu.Unlock()

	results := doSnapshotPingRequest(t)
	byName := map[string]SnapshotPingResult{}
	for _, r := range results {
		byName[r.Name] = r
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results (only failing cameras), got %d: %+v", len(results), results)
	}
	if r, ok := byName["cam-healthy"]; ok {
		t.Fatalf("a currently-healthy camera must not be pinged/returned, got %+v", r)
	}

	reachable, ok := byName["cam-reachable"]
	if !ok || !reachable.PingOK || reachable.IP != "10.0.1.5" {
		t.Fatalf("expected cam-reachable to report ping_ok=true ip=10.0.1.5, got %+v (found=%v)", reachable, ok)
	}
	unreachable, ok := byName["cam-unreachable"]
	if !ok || unreachable.PingOK || unreachable.IP != "10.0.1.6" {
		t.Fatalf("expected cam-unreachable to report ping_ok=false ip=10.0.1.6, got %+v (found=%v)", unreachable, ok)
	}
	noIP, ok := byName["cam-no-ip"]
	if !ok || noIP.Error == "" || noIP.IP != "" {
		t.Fatalf("expected cam-no-ip to report an error and no IP, got %+v (found=%v)", noIP, ok)
	}
}

func TestSnapshotPingHandlerOrdersLongestDownFirstWhenCapped(t *testing.T) {
	resetHealthMap(t)
	sources := map[string][]string{}
	healthMu.Lock()
	now := time.Now()
	// More failing cameras than the cap, each a different age, so a
	// correct implementation keeps only the oldest failures.
	for i := 0; i < snapshotPingMaxCount+5; i++ {
		name := "cam" + string(rune('A'+i))
		sources[name] = []string{"rtsp://10.0.2." + string(rune('0'+i%10)) + ":554/live"}
		healthMap[name] = &cameraHealth{OK: false, FailSince: now.Add(-time.Duration(i) * time.Second)}
	}
	healthMu.Unlock()
	withMockStreams(t, sources)
	withStubPing(t, nil) // everything reports unreachable; shape, not content, is under test here

	results := doSnapshotPingRequest(t)
	if len(results) != snapshotPingMaxCount {
		t.Fatalf("expected the response capped at %d, got %d", snapshotPingMaxCount, len(results))
	}
}

func TestSnapshotPingHandlerForbidsNonMonitorUser(t *testing.T) {
	resetHealthMap(t)
	req := httptest.NewRequest(http.MethodGet, "/api/proxy/snapshot-ping", nil)
	viewer := &User{Username: "viewer", Role: RoleViewer, Tabs: []string{}}
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, viewer))
	w := httptest.NewRecorder()
	proxySnapshotPingHandler(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a user without the Monitor tab, got %d", w.Code)
	}
}
