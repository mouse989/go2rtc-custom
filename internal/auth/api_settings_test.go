package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// doSettingsRequest calls settingsHandler directly as an admin, the same
// pattern snapshot_stale_test.go uses to exercise a handler without a full
// Middleware()/JWT round trip.
func doSettingsRequest(t *testing.T, method string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, "/api/settings", reader)
	admin := &User{Username: "admin", Role: RoleAdmin}
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, admin))
	w := httptest.NewRecorder()
	settingsHandler(w, req)
	return w
}

// TestSettingsHandlerNeverLeaksLiveTrafficKey verifies the whole point of
// the redaction in api_settings.go: the real VietMap Live Traffic API key
// (IP-allow-listed to this server only — see traffic_live.go) must never
// appear in a GET or POST response body, under any circumstance, including
// right after an admin sets it themselves.
func TestSettingsHandlerNeverLeaksLiveTrafficKey(t *testing.T) {
	initTestSettings(t)
	if err := UpdateSettings(AppSettings{VietmapLiveTrafficAPIKey: "super-secret-key"}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	w := doSettingsRequest(t, http.MethodGet, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/settings: expected 200, got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "super-secret-key") {
		t.Fatalf("GET /api/settings leaked the live traffic API key: %s", w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got["vietmap_live_traffic_api_key"] != "" {
		t.Fatalf("expected the key field to be blanked, got %v", got["vietmap_live_traffic_api_key"])
	}
	if got["vietmap_live_traffic_configured"] != true {
		t.Fatalf("expected vietmap_live_traffic_configured=true, got %v", got["vietmap_live_traffic_configured"])
	}

	// Saving other settings without including the key field at all must
	// leave the existing key untouched server-side (not visible in the
	// response either way, but GetSettings() proves it server-side).
	w2 := doSettingsRequest(t, http.MethodPost, map[string]any{"map_search_radius_km": 2.5})
	if w2.Code != http.StatusOK {
		t.Fatalf("POST (no key field): expected 200, got %d (body: %s)", w2.Code, w2.Body.String())
	}
	if strings.Contains(w2.Body.String(), "super-secret-key") {
		t.Fatalf("POST response leaked the live traffic API key: %s", w2.Body.String())
	}
	if got := GetSettings().VietmapLiveTrafficAPIKey; got != "super-secret-key" {
		t.Fatalf("expected the key to survive an unrelated save, got %q", got)
	}

	// Explicitly sending a new key updates it.
	w3 := doSettingsRequest(t, http.MethodPost, map[string]any{"vietmap_live_traffic_api_key": "rotated-key"})
	if w3.Code != http.StatusOK {
		t.Fatalf("POST (new key): expected 200, got %d", w3.Code)
	}
	if strings.Contains(w3.Body.String(), "rotated-key") {
		t.Fatalf("POST response leaked the newly-set key: %s", w3.Body.String())
	}
	if got := GetSettings().VietmapLiveTrafficAPIKey; got != "rotated-key" {
		t.Fatalf("expected the key to be updated to the new value, got %q", got)
	}

	// Explicitly sending an empty string clears it (the admin.html "Xoá"
	// button's path).
	w4 := doSettingsRequest(t, http.MethodPost, map[string]any{"vietmap_live_traffic_api_key": ""})
	if w4.Code != http.StatusOK {
		t.Fatalf("POST (clear key): expected 200, got %d", w4.Code)
	}
	if got := GetSettings().VietmapLiveTrafficAPIKey; got != "" {
		t.Fatalf("expected the key to be cleared, got %q", got)
	}
}
