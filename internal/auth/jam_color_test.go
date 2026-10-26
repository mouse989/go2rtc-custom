package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func doJamColorRequest(t *testing.T, method string, body any, admin bool) *httptest.ResponseRecorder {
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
	req := httptest.NewRequest(method, "/api/jam-color-cfg", reader)
	role := RoleViewer
	if admin {
		role = RoleAdmin
	}
	u := &User{Username: "u", Role: role}
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, u))
	w := httptest.NewRecorder()
	jamColorCfgHandler(w, req)
	return w
}

func TestJamColorDefaultStopsAreValid(t *testing.T) {
	if err := validateJamColorStops(jamColorDefaultStops); err != nil {
		t.Fatalf("built-in default stops failed validation: %v", err)
	}
}

func TestJamColorCfgGetReturnsDefaultWhenUnset(t *testing.T) {
	initTestSettings(t)
	w := doJamColorRequest(t, http.MethodGet, nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var out jamColorCfgOut
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !out.IsDefault {
		t.Error("expected IsDefault=true before any customization")
	}
	if len(out.Stops) != len(jamColorDefaultStops) {
		t.Errorf("expected %d default stops, got %d", len(jamColorDefaultStops), len(out.Stops))
	}
}

func TestJamColorCfgPutRequiresAdmin(t *testing.T) {
	initTestSettings(t)
	stops := []JamColorStop{{0, "#000000"}, {10, "#ffffff"}}
	w := doJamColorRequest(t, http.MethodPut, stops, false)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a non-admin PUT", w.Code)
	}
}

func TestJamColorCfgPutSavesAndGetReflectsIt(t *testing.T) {
	initTestSettings(t)
	stops := []JamColorStop{{0, "#000000"}, {5, "#888888"}, {10, "#ffffff"}}
	w := doJamColorRequest(t, http.MethodPut, stops, true)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200: %s", w.Code, w.Body.String())
	}

	w2 := doJamColorRequest(t, http.MethodGet, nil, false)
	var out jamColorCfgOut
	if err := json.Unmarshal(w2.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.IsDefault {
		t.Error("expected IsDefault=false after a custom save")
	}
	if len(out.Stops) != 3 || out.Stops[1].Color != "#888888" {
		t.Errorf("unexpected stops after save: %+v", out.Stops)
	}
}

func TestJamColorCfgPutRejectsUnsortedOrOutOfRangeOrBadColor(t *testing.T) {
	initTestSettings(t)
	cases := []struct {
		name  string
		stops []JamColorStop
	}{
		{"too few stops", []JamColorStop{{0, "#000000"}}},
		{"value out of range", []JamColorStop{{-1, "#000000"}, {10, "#ffffff"}}},
		{"value over 10", []JamColorStop{{0, "#000000"}, {11, "#ffffff"}}},
		{"duplicate value", []JamColorStop{{5, "#000000"}, {5, "#ffffff"}}},
		{"bad color", []JamColorStop{{0, "red"}, {10, "#ffffff"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := doJamColorRequest(t, http.MethodPut, c.stops, true)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 for %s", w.Code, c.name)
			}
		})
	}
}

func TestJamColorCfgPutSortsOutOfOrderStops(t *testing.T) {
	initTestSettings(t)
	// Sent out of order — the handler should sort before validating/saving
	// rather than rejecting purely on input order.
	stops := []JamColorStop{{10, "#ffffff"}, {0, "#000000"}, {5, "#888888"}}
	w := doJamColorRequest(t, http.MethodPut, stops, true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var out jamColorCfgOut
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for i := 1; i < len(out.Stops); i++ {
		if out.Stops[i].Value <= out.Stops[i-1].Value {
			t.Fatalf("stops not sorted ascending: %+v", out.Stops)
		}
	}
}

func TestJamColorCfgDeleteRequiresAdminAndResetsToDefault(t *testing.T) {
	initTestSettings(t)
	stops := []JamColorStop{{0, "#000000"}, {10, "#ffffff"}}
	doJamColorRequest(t, http.MethodPut, stops, true)

	wDenied := doJamColorRequest(t, http.MethodDelete, nil, false)
	if wDenied.Code != http.StatusForbidden {
		t.Fatalf("DELETE by non-admin status = %d, want 403", wDenied.Code)
	}

	wOK := doJamColorRequest(t, http.MethodDelete, nil, true)
	if wOK.Code != http.StatusOK {
		t.Fatalf("DELETE by admin status = %d, want 200: %s", wOK.Code, wOK.Body.String())
	}

	w2 := doJamColorRequest(t, http.MethodGet, nil, false)
	var out jamColorCfgOut
	if err := json.Unmarshal(w2.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !out.IsDefault || len(out.Stops) != len(jamColorDefaultStops) {
		t.Errorf("expected reset to default after DELETE, got %+v", out)
	}
}

func TestEffectiveJamColorStopsFallsBackToDefault(t *testing.T) {
	initTestSettings(t)
	stops := EffectiveJamColorStops()
	if len(stops) != len(jamColorDefaultStops) {
		t.Fatalf("expected default stops when unset, got %d stops", len(stops))
	}
}
