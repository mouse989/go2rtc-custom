package aianalyze

import "testing"

// withTempColorStore mirrors withTempStore in store_test.go, for the
// colour-override package globals.
func withTempColorStore(t *testing.T) {
	t.Helper()
	prevDir, prevOverr := dataDir, classColorOverr
	dataDir = t.TempDir()
	classColorOverr = nil
	t.Cleanup(func() { dataDir, classColorOverr = prevDir, prevOverr })
}

func TestGetClassColorsReturnsDefaultsBeforeAnyOverride(t *testing.T) {
	withTempColorStore(t)
	got := GetClassColors()
	for class, want := range DefaultClassColors {
		if got[class] != want {
			t.Errorf("GetClassColors()[%q] = %q, want default %q", class, got[class], want)
		}
	}
}

func TestSetClassColorsOverridesOnlyGivenClasses(t *testing.T) {
	withTempColorStore(t)
	if err := SetClassColors(map[string]string{"motorcycle": "#123456"}); err != nil {
		t.Fatalf("SetClassColors: %v", err)
	}
	got := GetClassColors()
	if got["motorcycle"] != "#123456" {
		t.Errorf("motorcycle = %q, want overridden #123456", got["motorcycle"])
	}
	if got["car"] != DefaultClassColors["car"] {
		t.Errorf("car = %q, want untouched default %q", got["car"], DefaultClassColors["car"])
	}
}

func TestSetClassColorsRejectsInvalidHex(t *testing.T) {
	withTempColorStore(t)
	if err := SetClassColors(map[string]string{"car": "not-a-color"}); err == nil {
		t.Fatal("expected error for an invalid hex colour")
	}
	if err := SetClassColors(map[string]string{"car": "red"}); err == nil {
		t.Fatal("expected error for a CSS colour name (must be hex)")
	}
}

func TestSetClassColorsAcceptsShortAndLongHex(t *testing.T) {
	withTempColorStore(t)
	if err := SetClassColors(map[string]string{"car": "#fff", "bus": "#aabbcc"}); err != nil {
		t.Fatalf("SetClassColors: %v", err)
	}
	got := GetClassColors()
	if got["car"] != "#fff" || got["bus"] != "#aabbcc" {
		t.Errorf("got car=%q bus=%q, want #fff / #aabbcc", got["car"], got["bus"])
	}
}

func TestSetClassColorsEmptyValueClearsOverride(t *testing.T) {
	withTempColorStore(t)
	if err := SetClassColors(map[string]string{"truck": "#111111"}); err != nil {
		t.Fatalf("SetClassColors: %v", err)
	}
	if err := SetClassColors(map[string]string{"truck": ""}); err != nil {
		t.Fatalf("SetClassColors (clear): %v", err)
	}
	got := GetClassColors()
	if got["truck"] != DefaultClassColors["truck"] {
		t.Errorf("truck = %q after clearing override, want default %q", got["truck"], DefaultClassColors["truck"])
	}
}

func TestClassColorsPersistAcrossReload(t *testing.T) {
	withTempColorStore(t)
	if err := SetClassColors(map[string]string{"person": "#ff9800"}); err != nil {
		t.Fatalf("SetClassColors: %v", err)
	}
	classColorOverr = nil // simulate a fresh process before loadClassColors runs
	loadClassColors()
	got := GetClassColors()
	if got["person"] != "#ff9800" {
		t.Errorf("after reload, person = %q, want persisted #ff9800", got["person"])
	}
}
