package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupIntegrationsTest(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	// KeyEnc's encryption is derived from jwtSecretRef (see integrationEncKey) —
	// explicitly initialized here rather than relying on some other test in
	// this package having already called initSecret first.
	if err := initSecret(filepath.Join(dir, ".jwt_secret")); err != nil {
		t.Fatalf("initSecret: %v", err)
	}
	if err := initIntegrations(filepath.Join(dir, "integrations.json")); err != nil {
		t.Fatalf("initIntegrations: %v", err)
	}
}

func TestCreateIntegrationReturnsUsableKey(t *testing.T) {
	setupIntegrationsTest(t)
	in, key, err := CreateIntegration("OMNIA", "/api/aievent/omnia/v1/push")
	if err != nil {
		t.Fatalf("CreateIntegration: %v", err)
	}
	if key == "" {
		t.Fatal("expected a non-empty plaintext key")
	}
	found, ok := ValidateIntegrationKey("/api/aievent/omnia/v1/push", key, "1.2.3.4")
	if !ok {
		t.Fatal("expected the freshly created key to validate")
	}
	if found.ID != in.ID {
		t.Errorf("expected ValidateIntegrationKey to resolve to %s, got %s", in.ID, found.ID)
	}
}

func TestIntegrationStoresOnlyTheHashNeverTheKey(t *testing.T) {
	setupIntegrationsTest(t)
	in, key, err := CreateIntegration("OMNIA", "/api/aievent/omnia/v1/push")
	if err != nil {
		t.Fatal(err)
	}
	if in.KeyHash == key {
		t.Fatal("KeyHash must never equal the plaintext key")
	}
	if in.KeyHash == "" {
		t.Fatal("expected a non-empty KeyHash")
	}
}

func TestValidateIntegrationKeyRejectsWrongKey(t *testing.T) {
	setupIntegrationsTest(t)
	if _, _, err := CreateIntegration("OMNIA", "/api/aievent/omnia/v1/push"); err != nil {
		t.Fatal(err)
	}
	if _, ok := ValidateIntegrationKey("/api/aievent/omnia/v1/push", "totally-wrong-key", "1.2.3.4"); ok {
		t.Error("expected an incorrect key to be rejected")
	}
}

func TestValidateIntegrationKeyRejectsWrongPath(t *testing.T) {
	setupIntegrationsTest(t)
	_, key, err := CreateIntegration("OMNIA", "/api/aievent/omnia/v1/push")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ValidateIntegrationKey("/api/incidents", key, "1.2.3.4"); ok {
		t.Error("expected a key to be rejected for an endpoint outside its AllowedPath")
	}
}

func TestValidateIntegrationKeyRejectsDisabledIntegration(t *testing.T) {
	setupIntegrationsTest(t)
	in, key, err := CreateIntegration("OMNIA", "/api/aievent/omnia/v1/push")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateIntegration(in.ID, in.Name, in.AllowedPath, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := ValidateIntegrationKey("/api/aievent/omnia/v1/push", key, "1.2.3.4"); ok {
		t.Error("expected a disabled integration's key to be rejected")
	}
}

func TestRotateIntegrationKeyInvalidatesOldKey(t *testing.T) {
	setupIntegrationsTest(t)
	in, oldKey, err := CreateIntegration("OMNIA", "/api/aievent/omnia/v1/push")
	if err != nil {
		t.Fatal(err)
	}
	newKey, err := RotateIntegrationKey(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if newKey == oldKey {
		t.Fatal("expected the rotated key to differ from the original")
	}
	if _, ok := ValidateIntegrationKey("/api/aievent/omnia/v1/push", oldKey, "1.2.3.4"); ok {
		t.Error("expected the old key to stop working after rotation")
	}
	if _, ok := ValidateIntegrationKey("/api/aievent/omnia/v1/push", newKey, "1.2.3.4"); !ok {
		t.Error("expected the newly rotated key to work")
	}
}

func TestDeleteIntegrationInvalidatesKey(t *testing.T) {
	setupIntegrationsTest(t)
	in, key, err := CreateIntegration("OMNIA", "/api/aievent/omnia/v1/push")
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteIntegration(in.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := ValidateIntegrationKey("/api/aievent/omnia/v1/push", key, "1.2.3.4"); ok {
		t.Error("expected a deleted integration's key to be rejected")
	}
}

func TestCreateIntegrationRejectsNonAPIPath(t *testing.T) {
	setupIntegrationsTest(t)
	if _, _, err := CreateIntegration("Bad", "/not-an-api-path"); err == nil {
		t.Error("expected an error for an allowed_path outside /api/")
	}
}

func TestCreateIntegrationRejectsEmptyName(t *testing.T) {
	setupIntegrationsTest(t)
	if _, _, err := CreateIntegration("", "/api/aievent/omnia/v1/push"); err == nil {
		t.Error("expected an error for an empty name")
	}
}

func TestListIntegrationsOmitsNothingButIsSortedByName(t *testing.T) {
	setupIntegrationsTest(t)
	CreateIntegration("Zebra", "/api/aievent/zebra/push")
	CreateIntegration("Alpha", "/api/aievent/alpha/push")
	list := ListIntegrations()
	if len(list) != 2 {
		t.Fatalf("expected 2 integrations, got %d", len(list))
	}
	if list[0].Name != "Alpha" || list[1].Name != "Zebra" {
		t.Errorf("expected alphabetical order, got %s, %s", list[0].Name, list[1].Name)
	}
}

func TestRevealIntegrationKeyReturnsTheSameKeyCreateGave(t *testing.T) {
	setupIntegrationsTest(t)
	in, key, err := CreateIntegration("OMNIA", "/api/aievent/omnia/v1/push")
	if err != nil {
		t.Fatal(err)
	}
	revealed, err := RevealIntegrationKey(in.ID)
	if err != nil {
		t.Fatalf("RevealIntegrationKey: %v", err)
	}
	if revealed != key {
		t.Errorf("expected the revealed key to match the originally issued one, got %q want %q", revealed, key)
	}
}

func TestRevealIntegrationKeyReflectsRotation(t *testing.T) {
	setupIntegrationsTest(t)
	in, _, err := CreateIntegration("OMNIA", "/api/aievent/omnia/v1/push")
	if err != nil {
		t.Fatal(err)
	}
	newKey, err := RotateIntegrationKey(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	revealed, err := RevealIntegrationKey(in.ID)
	if err != nil {
		t.Fatalf("RevealIntegrationKey: %v", err)
	}
	if revealed != newKey {
		t.Errorf("expected the revealed key to be the rotated one, got %q want %q", revealed, newKey)
	}
}

func TestIntegrationNeverStoresThePlaintextKeyItself(t *testing.T) {
	setupIntegrationsTest(t)
	in, key, err := CreateIntegration("OMNIA", "/api/aievent/omnia/v1/push")
	if err != nil {
		t.Fatal(err)
	}
	if in.KeyEnc == key {
		t.Fatal("KeyEnc must never equal the plaintext key — it must be encrypted")
	}
	if in.KeyEnc == "" {
		t.Fatal("expected a non-empty KeyEnc")
	}
}

func TestRevealIntegrationKeyErrorsForMissingIntegration(t *testing.T) {
	setupIntegrationsTest(t)
	if _, err := RevealIntegrationKey("does-not-exist"); err != errIntegrationNotFound {
		t.Errorf("expected errIntegrationNotFound, got %v", err)
	}
}

func TestRevealIntegrationKeyErrorsWhenKeyEncMissing(t *testing.T) {
	setupIntegrationsTest(t)
	in, _, err := CreateIntegration("OMNIA", "/api/aievent/omnia/v1/push")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an integration persisted before KeyEnc existed.
	istore.mu.Lock()
	istore.integrations[in.ID].KeyEnc = ""
	istore.mu.Unlock()
	if _, err := RevealIntegrationKey(in.ID); err != errIntegrationKeyUnavailable {
		t.Errorf("expected errIntegrationKeyUnavailable, got %v", err)
	}
}

// A corrupted integrations.json (e.g. truncated by a crash mid-write, back
// when saveLocked() wrote the file directly instead of via a temp+rename)
// must never be silently discarded: load() should back up the unreadable
// bytes next to the original path rather than let the first subsequent
// save overwrite them with an empty list.
func TestLoadCorruptedIntegrationsFileBacksUpOriginal(t *testing.T) {
	dir := t.TempDir()
	if err := initSecret(filepath.Join(dir, ".jwt_secret")); err != nil {
		t.Fatalf("initSecret: %v", err)
	}
	path := filepath.Join(dir, "integrations.json")
	const corrupt = `[{"id":"abc","name":"truncated mid-wri`
	if err := os.WriteFile(path, []byte(corrupt), 0644); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}

	err := initIntegrations(path)
	if err == nil {
		t.Fatal("expected initIntegrations to surface the parse error, got nil")
	}
	if len(ListIntegrations()) != 0 {
		t.Fatalf("expected an empty in-memory store after a corrupt load, got %d entries", len(ListIntegrations()))
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var backup string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "integrations.json.corrupt-") {
			backup = e.Name()
		}
	}
	if backup == "" {
		t.Fatalf("expected a integrations.json.corrupt-* backup file, directory has: %v", entries)
	}
	got, err := os.ReadFile(filepath.Join(dir, backup))
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(got) != corrupt {
		t.Fatalf("backup content = %q, want %q", got, corrupt)
	}
}

// saveLocked must write via a temp file + rename, never the live path
// directly, so a crash mid-write can't leave integrations.json truncated —
// see load()'s own test above for what that used to cause.
func TestSaveLockedWritesAtomicallyAndCleansUpTempFile(t *testing.T) {
	setupIntegrationsTest(t)
	if _, _, err := CreateIntegration("OMNIA", "/api/aievent/omnia/v1/push"); err != nil {
		t.Fatalf("CreateIntegration: %v", err)
	}

	entries, err := os.ReadDir(filepath.Dir(istore.path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("expected no leftover .tmp file after a successful save, found %s", e.Name())
		}
	}

	data, err := os.ReadFile(istore.path)
	if err != nil {
		t.Fatalf("read %s: %v", istore.path, err)
	}
	if !strings.Contains(string(data), "OMNIA") {
		t.Fatalf("expected the saved file to contain the new integration, got: %s", data)
	}
}
