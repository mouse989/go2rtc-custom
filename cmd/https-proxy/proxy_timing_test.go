package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTimingTransportLogsOnlySlowRequests(t *testing.T) {
	old := slowRequestThreshold
	slowRequestThreshold = 30 * time.Millisecond
	defer func() { slowRequestThreshold = old }()

	logMu.Lock()
	logRing = nil
	logMu.Unlock()

	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer fast.Close()

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(slowRequestThreshold + 40*time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()

	rt := &timingTransport{rt: http.DefaultTransport, site: "test.example"}

	fastReq, _ := http.NewRequest(http.MethodGet, fast.URL, nil)
	resp, err := rt.RoundTrip(fastReq)
	if err != nil {
		t.Fatalf("fast request: %v", err)
	}
	resp.Body.Close()

	slowReq, _ := http.NewRequest(http.MethodGet, slow.URL, nil)
	resp, err = rt.RoundTrip(slowReq)
	if err != nil {
		t.Fatalf("slow request: %v", err)
	}
	resp.Body.Close()

	lines := recentLog()
	var slowLines int
	for _, l := range lines {
		if strings.Contains(l, "[slow-backend]") {
			slowLines++
			if !strings.Contains(l, "test.example") || !strings.Contains(l, "status=200") {
				t.Fatalf("slow-backend log line missing expected fields: %s", l)
			}
		}
	}
	if slowLines != 1 {
		t.Fatalf("expected exactly 1 [slow-backend] log line (fast request must not log), got %d: %v", slowLines, lines)
	}
}

func TestSinceIfSetReturnsZeroForZeroTime(t *testing.T) {
	if d := sinceIfSet(time.Time{}); d != 0 {
		t.Fatalf("expected 0 for zero time, got %s", d)
	}
	if d := sinceIfSet(time.Now().Add(-10 * time.Millisecond)); d < 10*time.Millisecond {
		t.Fatalf("expected at least 10ms, got %s", d)
	}
}
