package auth

import (
	"context"
	"path/filepath"
	"testing"
)

func setupPTZCameraType(t *testing.T, streamName string, ct *CameraType) {
	t.Helper()
	if err := initCameraTypes(filepath.Join(t.TempDir(), "camera_types.json")); err != nil {
		t.Fatalf("initCameraTypes: %v", err)
	}
	if err := upsertCameraType(ct); err != nil {
		t.Fatalf("upsertCameraType: %v", err)
	}
	ctStore.data.Assignments = map[string]string{streamName: ct.ID}
	withMockStreams(t, map[string][]string{
		streamName: {"rtsp://admin:pass@10.0.0.9:554/Streaming/Channels/101"},
	})
}

func TestPTZEnabledForStream(t *testing.T) {
	setupPTZCameraType(t, "cam-bosch", &CameraType{
		ID: "bosch", Name: "Bosch", PTZEnabled: true, PTZDriver: ptzDriverONVIF,
	})

	if !PTZEnabledForStream("cam-bosch") {
		t.Fatal("expected PTZEnabledForStream to be true for a camera type with PTZEnabled=true and a driver set")
	}
	if PTZEnabledForStream("cam-unknown") {
		t.Fatal("expected PTZEnabledForStream to be false for a stream with no camera type assigned")
	}
}

func TestPTZEnabledForStreamFalseWithoutDriver(t *testing.T) {
	// PTZEnabled=true but PTZDriver left empty — shouldn't happen via the
	// admin UI (driver is required once the checkbox is on) but a stored
	// file could still be hand-edited into this state, and it must stay
	// inert rather than dispatching to a non-existent driver.
	setupPTZCameraType(t, "cam-partial", &CameraType{
		ID: "partial", Name: "Partial", PTZEnabled: true, PTZDriver: "",
	})
	if PTZEnabledForStream("cam-partial") {
		t.Fatal("expected PTZEnabledForStream to be false when PTZDriver is empty, even if PTZEnabled is true")
	}
}

func TestPTZEnabledForStreamFalseWhenDisabled(t *testing.T) {
	setupPTZCameraType(t, "cam-fixed", &CameraType{
		ID: "fixed", Name: "Fixed camera", PTZEnabled: false,
	})
	if PTZEnabledForStream("cam-fixed") {
		t.Fatal("expected PTZEnabledForStream to be false when PTZEnabled is false")
	}
}

func TestPtzAllowed(t *testing.T) {
	setupPTZCameraType(t, "cam-ptz", &CameraType{
		ID: "axisdome", Name: "Axis Dome", PTZEnabled: true, PTZDriver: ptzDriverAxisVAPIX,
	})

	admin := &User{Role: RoleAdmin}
	if !ptzAllowed(admin, "cam-ptz") {
		t.Fatal("expected admin to always be allowed PTZ on a PTZ-enabled stream")
	}

	viewerWithPermission := &User{Role: RoleViewer, AllowPTZ: true, Streams: []string{"cam-ptz"}}
	if !ptzAllowed(viewerWithPermission, "cam-ptz") {
		t.Fatal("expected a viewer with AllowPTZ and stream access to be allowed")
	}

	viewerNoPermission := &User{Role: RoleViewer, AllowPTZ: false, Streams: []string{"cam-ptz"}}
	if ptzAllowed(viewerNoPermission, "cam-ptz") {
		t.Fatal("expected a viewer without AllowPTZ to be denied even though they can see the stream")
	}

	viewerNoStreamAccess := &User{Role: RoleViewer, AllowPTZ: true, Streams: []string{"some-other-cam"}}
	if ptzAllowed(viewerNoStreamAccess, "cam-ptz") {
		t.Fatal("expected a viewer with AllowPTZ but no access to this specific stream to be denied")
	}

	// A camera with no PTZ configured at all must deny even an
	// AllowPTZ+stream-access viewer — PTZEnabledForStream is still checked.
	setupPTZCameraType(t, "cam-fixed", &CameraType{ID: "fixed", Name: "Fixed", PTZEnabled: false})
	viewerFixedCam := &User{Role: RoleViewer, AllowPTZ: true, Streams: []string{"cam-fixed"}}
	if ptzAllowed(viewerFixedCam, "cam-fixed") {
		t.Fatal("expected denial for a non-PTZ camera even with AllowPTZ and stream access")
	}
}

func TestSendPTZHomeRejectsNonPTZCameras(t *testing.T) {
	setupPTZCameraType(t, "cam-fixed", &CameraType{ID: "fixed", Name: "Fixed", PTZEnabled: false})
	if err := SendPTZHome(context.Background(), "cam-fixed"); err != errPTZNotEnabled {
		t.Fatalf("expected errPTZNotEnabled for a non-PTZ camera, got %v", err)
	}
	if err := SendPTZHome(context.Background(), "cam-unknown"); err != errPTZNotEnabled {
		t.Fatalf("expected errPTZNotEnabled for an unassigned stream, got %v", err)
	}
}

func TestSendPTZFocusRejectsNonPTZCameras(t *testing.T) {
	setupPTZCameraType(t, "cam-fixed", &CameraType{ID: "fixed", Name: "Fixed", PTZEnabled: false})
	if err := SendPTZFocus(context.Background(), "cam-fixed", 1); err != errPTZNotEnabled {
		t.Fatalf("expected errPTZNotEnabled for a non-PTZ camera, got %v", err)
	}
	if err := SendPTZFocus(context.Background(), "cam-unknown", -1); err != errPTZNotEnabled {
		t.Fatalf("expected errPTZNotEnabled for an unassigned stream, got %v", err)
	}
}

func TestSendPTZFocusStopRejectsNonPTZCameras(t *testing.T) {
	setupPTZCameraType(t, "cam-fixed", &CameraType{ID: "fixed", Name: "Fixed", PTZEnabled: false})
	if err := SendPTZFocusStop(context.Background(), "cam-fixed"); err != errPTZNotEnabled {
		t.Fatalf("expected errPTZNotEnabled for a non-PTZ camera, got %v", err)
	}
	if err := SendPTZFocusStop(context.Background(), "cam-unknown"); err != errPTZNotEnabled {
		t.Fatalf("expected errPTZNotEnabled for an unassigned stream, got %v", err)
	}
}

func TestSendPTZSaveHomeRejectsNonPTZCameras(t *testing.T) {
	setupPTZCameraType(t, "cam-fixed", &CameraType{ID: "fixed", Name: "Fixed", PTZEnabled: false})
	if err := SendPTZSaveHome(context.Background(), "cam-fixed"); err != errPTZNotEnabled {
		t.Fatalf("expected errPTZNotEnabled for a non-PTZ camera, got %v", err)
	}
	if err := SendPTZSaveHome(context.Background(), "cam-unknown"); err != errPTZNotEnabled {
		t.Fatalf("expected errPTZNotEnabled for an unassigned stream, got %v", err)
	}
}

func TestVapixSpeedClamping(t *testing.T) {
	cases := []struct {
		in   float64
		want int
	}{
		{0, 0},
		{1, 100},
		{-1, -100},
		{0.5, 50},
		{-0.5, -50},
		{2, 100}, // out-of-range input clamps rather than overflowing VAPIX's -100..100
		{-2, -100},
	}
	for _, c := range cases {
		if got := vapixSpeed(c.in); got != c.want {
			t.Errorf("vapixSpeed(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestClampPTZ(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{0, 0},
		{1, 1},
		{-1, -1},
		{1.5, 1},
		{-1.5, -1},
	}
	for _, c := range cases {
		if got := clampPTZ(c.in); got != c.want {
			t.Errorf("clampPTZ(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
