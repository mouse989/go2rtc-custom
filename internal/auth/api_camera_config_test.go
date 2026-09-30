package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCameraConfigStreamsHandlerAcceptsRTSPSAndRTSPX(t *testing.T) {
	withMockStreams(t, map[string][]string{
		"plain-rtsp":  {"rtsp://admin:pass@10.0.0.1:554/Streaming/Channels/101"},
		"bosch-rtsps": {"rtsps://admin:pass@10.0.0.2:554/rtsp_tunnel"},
		"unifi-rtspx": {"rtspx://10.0.0.3:7441/fD6ouM72bWoFijxK"},
		"ffmpeg-src":  {"ffmpeg:rtsp://admin:pass@10.0.0.4/live"}, // not a bare rtsp(s|x):// URL — excluded
	})

	admin := &User{Username: "admin", Role: RoleAdmin}
	req := httptest.NewRequest(http.MethodGet, "/api/camera-config/streams", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, admin))
	w := httptest.NewRecorder()

	cameraConfigStreamsHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var out []camStreamInfo
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	byName := map[string]camStreamInfo{}
	for _, ci := range out {
		byName[ci.Name] = ci
	}

	if _, ok := byName["plain-rtsp"]; !ok {
		t.Error("expected plain-rtsp to be included")
	}
	bosch, ok := byName["bosch-rtsps"]
	if !ok {
		t.Fatal("expected bosch-rtsps (rtsps://) to be included, was excluded")
	}
	if bosch.Host != "10.0.0.2" || bosch.Port != "554" || bosch.Username != "admin" || bosch.Password != "pass" {
		t.Errorf("bosch-rtsps parsed incorrectly: %+v", bosch)
	}
	if _, ok := byName["unifi-rtspx"]; !ok {
		t.Error("expected unifi-rtspx (rtspx://) to be included, was excluded")
	}
	if _, ok := byName["ffmpeg-src"]; ok {
		t.Error("expected ffmpeg-wrapped source to be excluded (not a bare rtsp(s|x):// URL)")
	}
}
