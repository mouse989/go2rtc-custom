package auth

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

func initTestDeviceBinding(t *testing.T) {
	t.Helper()
	if err := initDeviceBinding(filepath.Join(t.TempDir(), "device_bindings.json")); err != nil {
		t.Fatalf("initDeviceBinding: %v", err)
	}
}

func TestDeviceScopeForPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/api/webrtc", "cam_stream"},
		{"/api/ws", "cam_stream"},
		{"/api/hls/index.m3u8", "cam_stream"},
		{"/api/proxy/hls", "cam_stream"},
		{"/api/proxy/mp4", "cam_stream"},
		{"/api/proxy/frame", "cam_snapshot"},
		{"/api/frame.jpeg", "cam_snapshot"},
		{"/api/incidents/import", "incidents_export"},
		{"/api/counting/export-csv", "counting_export"},
		{"/api/camera-locations", "gps_locations"},
		{"/api/device-stats", "monitor_devices"},
		{"/api/users", ""}, // not a gateable path at all
		{"/api/proxy/ws", "cam_stream"},
	}
	for _, c := range cases {
		if got := deviceScopeForPath(c.path); got != c.want {
			t.Errorf("deviceScopeForPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestValidDeviceScopeKeys(t *testing.T) {
	got := validDeviceScopeKeys([]string{"cam_stream", "bogus-key", "cam_snapshot"})
	want := []string{"cam_stream", "cam_snapshot"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if validDeviceScopeKeys(nil) != nil {
		t.Fatal("expected nil for empty/nil input")
	}
}

func TestUserRequiresDeviceBindingFor(t *testing.T) {
	u := &User{DeviceBindingScopes: []string{"cam_stream", "gps_locations"}}
	if !u.RequiresDeviceBindingFor("cam_stream") {
		t.Fatal("expected cam_stream to be gated")
	}
	if u.RequiresDeviceBindingFor("cam_snapshot") {
		t.Fatal("cam_snapshot was never granted to this user, should not be gated")
	}
}

func TestTrustedDeviceLifecycle(t *testing.T) {
	initTestDeviceBinding(t)

	cred := webauthn.Credential{ID: []byte("cred-1"), PublicKey: []byte("pubkey-bytes")}
	dev, err := addPendingDevice("alice", "Laptop Dell", cred)
	if err != nil {
		t.Fatalf("addPendingDevice: %v", err)
	}
	if dev.Status != DeviceStatusPending {
		t.Fatalf("expected pending, got %s", dev.Status)
	}
	if dev.ID == "" {
		t.Fatal("expected a generated ID")
	}

	// Pending devices must never satisfy the login/assertion credential
	// lookup — that's the whole point of the approval gate.
	if _, ok := findApprovedDeviceByCredentialID(cred.ID); ok {
		t.Fatal("pending device must not be found as approved")
	}
	if userHasApprovedDevice("alice") {
		t.Fatal("alice should have no approved device yet")
	}

	if ok := approveDevice(dev.ID, "admin"); !ok {
		t.Fatal("approveDevice returned false for an existing device")
	}
	found, ok := findApprovedDeviceByCredentialID(cred.ID)
	if !ok {
		t.Fatal("expected to find the now-approved device by credential ID")
	}
	if found.Username != "alice" {
		t.Fatalf("expected username alice, got %s", found.Username)
	}
	if !userHasApprovedDevice("alice") {
		t.Fatal("alice should now have an approved device")
	}

	// touchDeviceUsage persists the advanced Credential (SignCount/Flags)
	// plus last-used metadata — required so replay-counter checks on the
	// next login actually have something to compare against.
	updatedCred := cred
	updatedCred.Authenticator.SignCount = 42
	touchDeviceUsage(dev.ID, updatedCred, "10.0.0.5")
	got, _ := getTrustedDevice(dev.ID)
	if got.Credential.Authenticator.SignCount != 42 {
		t.Fatalf("expected SignCount 42, got %d", got.Credential.Authenticator.SignCount)
	}
	if got.LastUsedIP != "10.0.0.5" || got.LastUsedAt == nil {
		t.Fatalf("expected last-used metadata recorded, got %+v", got)
	}

	// Revoking removes it from the approved-lookup path even though the
	// record itself stays (for audit/history).
	if ok := revokeDevice(dev.ID); !ok {
		t.Fatal("revokeDevice returned false for an existing device")
	}
	if _, ok := findApprovedDeviceByCredentialID(cred.ID); ok {
		t.Fatal("revoked device must not be found as approved")
	}
	if userHasApprovedDevice("alice") {
		t.Fatal("alice should have no approved device after revocation")
	}

	if all := listAllTrustedDevices(); len(all) != 1 {
		t.Fatalf("expected 1 device total, got %d", len(all))
	}
	if mine := listDevicesForUser("alice"); len(mine) != 1 {
		t.Fatalf("expected 1 device for alice, got %d", len(mine))
	}
	if other := listDevicesForUser("bob"); len(other) != 0 {
		t.Fatalf("bob should have no devices, got %d", len(other))
	}
	if approveDevice("no-such-id", "admin") {
		t.Fatal("approving an unknown ID must report false")
	}
	if revokeDevice("no-such-id") {
		t.Fatal("revoking an unknown ID must report false")
	}
}

func TestDeviceVerifiedTokenRoundTrip(t *testing.T) {
	jwtSecret = []byte("test-secret-for-device-verified-token-tests-xx")

	token, err := signDeviceVerifiedToken("alice", time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if !verifyDeviceVerifiedToken(token, "alice") {
		t.Fatal("expected a freshly-signed token to verify")
	}
	if verifyDeviceVerifiedToken(token, "bob") {
		t.Fatal("a token signed for alice must not verify for a different username")
	}
	if verifyDeviceVerifiedToken(token+"tampered", "alice") {
		t.Fatal("a tampered token must not verify")
	}
	if verifyDeviceVerifiedToken("", "alice") {
		t.Fatal("an empty token must not verify")
	}
	if verifyDeviceVerifiedToken("not-a-valid-token", "alice") {
		t.Fatal("a malformed token must not verify")
	}

	expired, err := signDeviceVerifiedToken("alice", -time.Minute)
	if err != nil {
		t.Fatalf("sign expired: %v", err)
	}
	if verifyDeviceVerifiedToken(expired, "alice") {
		t.Fatal("an expired token must not verify")
	}
}

func TestDeviceVerifyValidDuration(t *testing.T) {
	initTestSettings(t)

	if got := deviceVerifyValidDuration(); got != 12*time.Hour {
		t.Fatalf("expected default 12h, got %s", got)
	}
	if err := UpdateSettings(AppSettings{DeviceVerifyValidHours: 3}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if got := deviceVerifyValidDuration(); got != 3*time.Hour {
		t.Fatalf("expected explicit 3h override, got %s", got)
	}
}

// TestMiddlewareRequiresVerifiedDeviceForGatedScope exercises the real
// Middleware() end to end: a viewer whose account has cam_stream in
// DeviceBindingScopes must be refused a cam_stream path (device_stream
// endpoints — see deviceScopeCatalog) until their request also carries a
// valid device-verified cookie, at which point the same request reaches the
// wrapped handler normally.
func TestMiddlewareRequiresVerifiedDeviceForGatedScope(t *testing.T) {
	dir := t.TempDir()
	if err := initSecret(filepath.Join(dir, ".jwt_secret")); err != nil {
		t.Fatalf("initSecret: %v", err)
	}
	if err := initStore(filepath.Join(dir, "users.json")); err != nil {
		t.Fatalf("initStore: %v", err)
	}
	initTestSettings(t)

	u := &User{
		Username:            "carol",
		Role:                RoleViewer,
		Enabled:             true,
		Streams:             []string{"cam1"},
		DeviceBindingScopes: []string{"cam_stream"},
	}
	if err := CreateUser(u, "irrelevant-password"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// CreateUser always forces a first-login password reset, which would
	// block every other path via Middleware's own must-change-password
	// gate — clear it so this test only exercises the device-binding gate.
	existing, _ := GetUser(u.Username)
	existing.MustChangePassword = false
	if err := UpdateUser(existing, ""); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}

	token, err := GenerateToken(existing)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	reached := false
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware(final)

	newReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/webrtc?src=cam1", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		return req
	}

	// No device-verified cookie yet — gated scope must refuse with a
	// machine-readable reason, and the wrapped handler must never run.
	reached = false
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, newReq())
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a verified-device cookie, got %d (body: %s)", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("device_verification_required")) {
		t.Fatalf("expected device_verification_required in body, got %s", w.Body.String())
	}
	if reached {
		t.Fatal("wrapped handler must not run without device verification")
	}

	// With a valid device-verified cookie for the same user, the request
	// reaches the wrapped handler like any other permitted path.
	validToken, err := signDeviceVerifiedToken(u.Username, time.Hour)
	if err != nil {
		t.Fatalf("signDeviceVerifiedToken: %v", err)
	}
	reached = false
	w = httptest.NewRecorder()
	req := newReq()
	req.AddCookie(&http.Cookie{Name: deviceVerifiedCookieName, Value: validToken})
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with a valid verified-device cookie, got %d (body: %s)", w.Code, w.Body.String())
	}
	if !reached {
		t.Fatal("wrapped handler should have run once the device is verified")
	}

	// A device-verified cookie signed for a *different* username must not
	// satisfy carol's gate.
	otherToken, err := signDeviceVerifiedToken("mallory", time.Hour)
	if err != nil {
		t.Fatalf("signDeviceVerifiedToken: %v", err)
	}
	reached = false
	w = httptest.NewRecorder()
	req = newReq()
	req.AddCookie(&http.Cookie{Name: deviceVerifiedCookieName, Value: otherToken})
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a cookie signed for a different user, got %d", w.Code)
	}
	if reached {
		t.Fatal("wrapped handler must not run for a cookie signed for a different user")
	}
}
