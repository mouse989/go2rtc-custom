package auth

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// initTestSettings points the package-level settings store at a temp file —
// UpdateSettings silently no-ops (os.WriteFile("", ...) fails before it ever
// touches the in-memory appSettings) if initSettings was never called, which
// it isn't by default in a unit test binary.
func initTestSettings(t *testing.T) {
	t.Helper()
	if err := initSettings(filepath.Join(t.TempDir(), "settings.json")); err != nil {
		t.Fatalf("initSettings: %v", err)
	}
}

func TestBuildDisconnectedPlaceholderIsValidJPEG(t *testing.T) {
	data := buildDisconnectedPlaceholder()
	if len(data) == 0 {
		t.Fatal("expected non-empty JPEG bytes")
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != 320 || b.Dy() != 180 {
		t.Fatalf("expected 320x180, got %dx%d", b.Dx(), b.Dy())
	}
}

func TestBuildDisconnectedPlaceholderIsVisuallyDistinctFromDefault(t *testing.T) {
	// placeholderJPEG (init() in proxy.go) is a flat dark gray; the
	// disconnected placeholder must not be identical, or a viewer can't
	// tell "camera not yet fetched" apart from "camera offline".
	if bytes.Equal(placeholderJPEG, disconnectedPlaceholderJPEG) {
		t.Fatal("disconnected placeholder is identical to the generic cold-miss placeholder")
	}

	img, err := jpeg.Decode(bytes.NewReader(disconnectedPlaceholderJPEG))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Sample a pixel from the ⚠️ icon's amber fill (drawWarningIcon places it
	// at cx=160, topY=8, size=64 — (170, 45) sits inside the triangle body,
	// clear of the "!" glyph and the highlight streak) and confirm it's
	// warm-toned (amber), not the flat dark red-brown background.
	r, g, b, _ := img.At(170, 45).RGBA()
	if !(r > g && g > b) {
		t.Fatalf("expected an amber (r>g>b) pixel inside the warning icon, got r=%d g=%d b=%d", r>>8, g>>8, b>>8)
	}
}

func TestSnapshotStaleThresholdAutoScalesWithInterval(t *testing.T) {
	initTestSettings(t)

	// Default interval (0 → 15s) → floored at the 60s minimum, not 4×15=60
	// coincidentally equal here, so also check a case where flooring
	// actually matters.
	if err := UpdateSettings(AppSettings{SnapshotIntervalSec: 0}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if got := snapshotStaleThreshold(); got != 60*time.Second {
		t.Fatalf("expected 60s for default interval, got %s", got)
	}

	if err := UpdateSettings(AppSettings{SnapshotIntervalSec: 5}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if got := snapshotStaleThreshold(); got != 60*time.Second {
		t.Fatalf("expected the 60s floor for a 5s interval (4×5=20 < 60), got %s", got)
	}

	if err := UpdateSettings(AppSettings{SnapshotIntervalSec: 300}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if got := snapshotStaleThreshold(); got != 1200*time.Second {
		t.Fatalf("expected 4×300=1200s for a 300s interval, got %s", got)
	}
}

func TestSnapshotStaleThresholdExplicitOverride(t *testing.T) {
	initTestSettings(t)

	if err := UpdateSettings(AppSettings{SnapshotIntervalSec: 300, SnapshotStaleThresholdSec: 90}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if got := snapshotStaleThreshold(); got != 90*time.Second {
		t.Fatalf("expected explicit 90s override regardless of interval, got %s", got)
	}
}

func TestDrawWarningIconStaysWithinBounds(t *testing.T) {
	// Regression guard: drawWarningIcon/drawCenteredText must not panic for
	// any reasonable canvas/geometry — exercise it at the actual placement
	// buildDisconnectedPlaceholder uses, plus a corner/oversized case that
	// would push the scaled icon rect outside the canvas if draw.Draw's
	// clipping didn't handle it.
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))

	drawWarningIcon(img, 160, 8, 64)
	drawWarningIcon(img, 0, 0, 400) // centered at corner, oversized — must not panic
	drawCenteredText(img, "CAMERA OFFLINE", 130, color.RGBA{230, 230, 230, 255})
}

func TestProxyFrameHandlerServesDisconnectedPlaceholderForStaleSnapshot(t *testing.T) {
	initTestSettings(t)
	if err := UpdateSettings(AppSettings{SnapshotStaleThresholdSec: 60}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	if err := os.MkdirAll(snapshotDir, 0755); err != nil {
		t.Fatalf("mkdir snapshotDir: %v", err)
	}
	const streamName = "test-stale-snapshot-cam"
	path := SnapshotFilePath(streamName)
	t.Cleanup(func() { _ = os.Remove(path) })

	realJPEG := []byte{0xFF, 0xD8, 0xFF, 0xE0, 'r', 'e', 'a', 'l'}
	if err := writeSnapshotToDisk(streamName, realJPEG); err != nil {
		t.Fatalf("writeSnapshotToDisk: %v", err)
	}

	id := RegisterStreamID(streamName)
	admin := &User{Username: "admin", Role: RoleAdmin}

	doRequest := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/proxy/frame?id="+id, nil)
		req = req.WithContext(context.WithValue(req.Context(), userContextKey, admin))
		w := httptest.NewRecorder()
		proxyFrameHandler(w, req)
		return w
	}

	// Fresh file (just written) → served as-is.
	w := doRequest()
	if got := w.Header().Get("X-Frame-Cache"); got != "HIT" {
		t.Fatalf("expected X-Frame-Cache: HIT for a fresh snapshot, got %q", got)
	}
	if !bytes.Equal(w.Body.Bytes(), realJPEG) {
		t.Fatalf("expected the real snapshot bytes for a fresh file, got %v", w.Body.Bytes())
	}

	// Back-date the file past the 60s threshold → must switch to the
	// disconnected placeholder instead of serving the (now stale) real image.
	staleTime := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(path, staleTime, staleTime); err != nil {
		t.Fatalf("os.Chtimes: %v", err)
	}

	w = doRequest()
	if got := w.Header().Get("X-Frame-Cache"); got != "STALE" {
		t.Fatalf("expected X-Frame-Cache: STALE for a back-dated snapshot, got %q", got)
	}
	if !bytes.Equal(w.Body.Bytes(), disconnectedPlaceholderJPEG) {
		t.Fatal("expected the disconnected placeholder bytes for a stale snapshot, got something else")
	}
	if bytes.Equal(w.Body.Bytes(), realJPEG) {
		t.Fatal("served the stale real snapshot instead of the disconnected placeholder")
	}
}
