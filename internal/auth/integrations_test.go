package auth

import (
	"path/filepath"
	"testing"
)

func setupIntegrationsTest(t *testing.T) {
	t.Helper()
	if err := initIntegrations(filepath.Join(t.TempDir(), "integrations.json")); err != nil {
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
