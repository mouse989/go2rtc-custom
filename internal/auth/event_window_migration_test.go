package auth

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInitSettingsMigratesLegacyEventWindowFields verifies that a
// settings.json saved before EventWindowMinutes existed (only the two
// now-deprecated per-layer fields set) gets its value carried forward
// instead of silently resetting to the 10-minute default.
func TestInitSettingsMigratesLegacyEventWindowFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"traffic_event_window_minutes":15,"ai_event_window_minutes":20}`), 0644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}
	if err := initSettings(path); err != nil {
		t.Fatalf("initSettings: %v", err)
	}
	if got := GetSettings().EventWindowMinutes; got != 20 {
		t.Errorf("EventWindowMinutes = %d, want 20 (the larger of the two legacy values)", got)
	}
}

// TestInitSettingsDoesNotOverrideExplicitEventWindow verifies a
// settings.json that already has event_window_minutes set is left alone
// even if the (now-unused) legacy fields are also present — an admin's
// post-upgrade save must stick, not keep getting re-migrated.
func TestInitSettingsDoesNotOverrideExplicitEventWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"event_window_minutes":5,"traffic_event_window_minutes":15,"ai_event_window_minutes":20}`), 0644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}
	if err := initSettings(path); err != nil {
		t.Fatalf("initSettings: %v", err)
	}
	if got := GetSettings().EventWindowMinutes; got != 5 {
		t.Errorf("EventWindowMinutes = %d, want 5 (already-explicit value must not be migrated over)", got)
	}
}

// TestInitSettingsNoLegacyFieldsLeavesEventWindowZero verifies a brand-new
// (or already-clean) settings.json with none of these fields set leaves
// EventWindowMinutes at its zero value — the 10-minute default is applied
// downstream by the handlers, not baked in here.
func TestInitSettingsNoLegacyFieldsLeavesEventWindowZero(t *testing.T) {
	// initSettings on a not-yet-created file returns early without
	// touching appSettings (see its own "start empty; file created on
	// first save" comment) — reset the package-level state by hand first
	// so this test doesn't inherit whatever an earlier test in this file
	// left behind.
	settingsMu.Lock()
	appSettings = AppSettings{}
	settingsMu.Unlock()
	initTestSettings(t)
	if got := GetSettings().EventWindowMinutes; got != 0 {
		t.Errorf("EventWindowMinutes = %d, want 0 (no legacy values to migrate)", got)
	}
}
