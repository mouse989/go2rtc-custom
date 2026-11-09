package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// A successful save must leave a recoverable rotating backup (.bak.0)
// behind — the safety net TestLoadRecoversFromBackupWhenLiveFileIsZeroed
// below depends on.
func TestSaveLockedLeavesARotatingBackup(t *testing.T) {
	setupIntegrationsTest(t)
	if _, _, err := CreateIntegration("OMNIA", "/api/aievent/omnia/v1/push"); err != nil {
		t.Fatalf("CreateIntegration: %v", err)
	}
	data, err := os.ReadFile(istore.path + ".bak.0")
	if err != nil {
		t.Fatalf("expected a .bak.0 file after a successful save: %v", err)
	}
	if !strings.Contains(string(data), "OMNIA") {
		t.Fatalf("expected the backup to contain the new integration, got: %s", data)
	}
}

// Reproduces the exact corruption reported against a Synology/Btrfs-backed
// data volume: the live file stays the same byte length as before but
// every byte reads back as 0x00 (a storage-layer checksum mismatch with no
// redundant copy to self-heal from — not a truncation or a partial write,
// which TestLoadCorruptedIntegrationsFileBacksUpOriginal already covers).
// load() must recover the integrations from the rotating backup rather
// than start empty, and heal the live file back to valid JSON so the next
// restart doesn't need to recover again.
func TestLoadRecoversFromBackupWhenLiveFileIsZeroed(t *testing.T) {
	setupIntegrationsTest(t)
	in, key, err := CreateIntegration("OMNIA", "/api/aievent/omnia/v1/push")
	if err != nil {
		t.Fatalf("CreateIntegration: %v", err)
	}
	liveInfo, err := os.Stat(istore.path)
	if err != nil {
		t.Fatalf("stat live file: %v", err)
	}
	// Simulate the exact observed corruption: same length, every byte 0x00.
	zeroed := make([]byte, liveInfo.Size())
	if err := os.WriteFile(istore.path, zeroed, 0644); err != nil {
		t.Fatalf("simulate zeroed file: %v", err)
	}

	if err := istore.load(); err != nil {
		t.Fatalf("load() after zeroing = %v, want nil (recovered from backup)", err)
	}
	if _, ok := ValidateIntegrationKey("/api/aievent/omnia/v1/push", key, "1.2.3.4"); !ok {
		t.Fatal("expected the integration created before corruption to still validate after recovery")
	}
	recoveredList := ListIntegrations()
	if len(recoveredList) != 1 || recoveredList[0].ID != in.ID {
		t.Fatalf("ListIntegrations after recovery = %+v, want the one integration created before corruption", recoveredList)
	}

	// The live file must now be healed (valid JSON again), not left zeroed.
	healed, err := os.ReadFile(istore.path)
	if err != nil {
		t.Fatalf("read healed live file: %v", err)
	}
	var sanity []*Integration
	if err := json.Unmarshal(healed, &sanity); err != nil {
		t.Fatalf("live file after recovery is still not valid JSON: %v", err)
	}
	if len(sanity) != 1 {
		t.Fatalf("healed live file has %d entries, want 1", len(sanity))
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

// rotateBackups() used to write the newest backup slot (.bak.0) with a
// plain os.WriteFile — not atomic. A crash mid-write there could corrupt
// .bak.0, which then gets rotated (still corrupt) into .bak.1 and .bak.2 on
// later saves, eventually leaving load()'s recovery with nothing valid to
// fall back to even though the live file itself was always written
// atomically. It must now go through the same temp+rename pattern
// saveLocked() uses for the live file: no stray .tmp file left behind, and
// every backup slot holding valid JSON.
func TestRotateBackupsWritesNewestSlotAtomicallyAndCleansUpTempFile(t *testing.T) {
	setupIntegrationsTest(t)
	if _, _, err := CreateIntegration("OMNIA", "/api/aievent/omnia/v1/push"); err != nil {
		t.Fatalf("CreateIntegration: %v", err)
	}
	// A second save exercises the actual rotation (.bak.0 -> .bak.1), not
	// just the first-ever write into an empty slot.
	if _, _, err := CreateIntegration("FPT VDS", "/api/trafficflow/fpt/v1/push"); err != nil {
		t.Fatalf("CreateIntegration: %v", err)
	}

	entries, err := os.ReadDir(filepath.Dir(istore.path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("expected no leftover .tmp file after backup rotation, found %s", e.Name())
		}
	}

	for _, suffix := range []string{".bak.0", ".bak.1"} {
		data, err := os.ReadFile(istore.path + suffix)
		if err != nil {
			t.Fatalf("read %s: %v", istore.path+suffix, err)
		}
		var list []*Integration
		if err := json.Unmarshal(data, &list); err != nil {
			t.Fatalf("%s is not valid JSON: %v", suffix, err)
		}
	}
}

// ValidateIntegrationKey updates LastUsedAt/IP in memory on every call, but
// must throttle how often that actually gets persisted to disk — a live
// push feed calling this every few seconds would otherwise turn into a
// near-continuous stream of saves, each one another window for a crash to
// land mid-write (see rotateBackups' own fix for what that caused). Two
// validations in quick succession should only actually hit disk once.
func TestValidateIntegrationKeyThrottlesUsageStampPersistence(t *testing.T) {
	setupIntegrationsTest(t)
	_, key, err := CreateIntegration("OMNIA", "/api/aievent/omnia/v1/push")
	if err != nil {
		t.Fatalf("CreateIntegration: %v", err)
	}
	// Force the throttle window to look like it last fired long ago, so
	// the very first validation below is the one allowed through to disk.
	istore.lastUsageSaveAt = time.Time{}

	if _, ok := ValidateIntegrationKey("/api/aievent/omnia/v1/push", key, "1.1.1.1"); !ok {
		t.Fatal("expected the first validation to succeed")
	}
	diskAfterFirst, err := os.ReadFile(istore.path)
	if err != nil {
		t.Fatalf("read after first validation: %v", err)
	}
	if !strings.Contains(string(diskAfterFirst), "1.1.1.1") {
		t.Fatalf("expected the first validation's IP to be persisted, got: %s", diskAfterFirst)
	}

	if _, ok := ValidateIntegrationKey("/api/aievent/omnia/v1/push", key, "2.2.2.2"); !ok {
		t.Fatal("expected the second validation to succeed")
	}
	id := ListIntegrations()[0].ID
	found, ok := GetIntegration(id)
	if !ok || found.LastUsedIP != "2.2.2.2" {
		t.Fatalf("expected in-memory LastUsedIP to update immediately to 2.2.2.2, got %+v", found)
	}
	diskAfterSecond, err := os.ReadFile(istore.path)
	if err != nil {
		t.Fatalf("read after second validation: %v", err)
	}
	if strings.Contains(string(diskAfterSecond), "2.2.2.2") {
		t.Fatal("expected the second validation's IP to be throttled, not yet persisted to disk")
	}
	if !strings.Contains(string(diskAfterSecond), "1.1.1.1") {
		t.Fatal("expected the file to still hold the first validation's already-persisted IP")
	}
}
