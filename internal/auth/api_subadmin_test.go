package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// setupSubAdminTest initializes a fresh user store + JWT secret in a temp
// dir, same pattern as device_binding_test.go's
// TestMiddlewareRequiresVerifiedDeviceForGatedScope.
func setupSubAdminTest(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := initSecret(filepath.Join(dir, ".jwt_secret")); err != nil {
		t.Fatalf("initSecret: %v", err)
	}
	if err := initStore(filepath.Join(dir, "users.json")); err != nil {
		t.Fatalf("initStore: %v", err)
	}
	initSubAdminAudit(filepath.Join(dir, "subadmin_audit"))
}

func doSubAdminRequest(t *testing.T, caller *User, method, path string, body any) *httptest.ResponseRecorder {
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
	req := httptest.NewRequest(method, path, reader)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, caller))
	w := httptest.NewRecorder()
	switch {
	case path == "/api/subadmin/users":
		subAdminUsersHandler(w, req)
	default:
		subAdminUserActionHandler(w, req)
	}
	return w
}

func TestSanitizeSubAdminManagedUsersFiltersInvalidEntries(t *testing.T) {
	setupSubAdminTest(t)

	if err := CreateUser(&User{Username: "viewer1", Role: RoleViewer, Enabled: true}, "pw"); err != nil {
		t.Fatalf("CreateUser viewer1: %v", err)
	}
	if err := CreateUser(&User{Username: "admin2", Role: RoleAdmin, Enabled: true}, "pw"); err != nil {
		t.Fatalf("CreateUser admin2: %v", err)
	}
	if err := CreateUser(&User{Username: "subadmin2", Role: RoleViewer, Enabled: true, Tabs: []string{TabSubAdmin}}, "pw"); err != nil {
		t.Fatalf("CreateUser subadmin2: %v", err)
	}

	got := sanitizeSubAdminManagedUsers(
		[]string{"viewer1", "admin2", "subadmin2", "ghost-does-not-exist", "self"},
		"self",
	)
	if len(got) != 1 || got[0] != "viewer1" {
		t.Fatalf("expected only viewer1 to survive sanitization, got %v", got)
	}
}

func TestSubAdminToggleEnabledRequiresCapabilityFlag(t *testing.T) {
	setupSubAdminTest(t)
	if err := CreateUser(&User{Username: "bob", Role: RoleViewer, Enabled: true}, "pw"); err != nil {
		t.Fatalf("CreateUser bob: %v", err)
	}
	caller := &User{
		Username:             "suba",
		Role:                 RoleViewer,
		Tabs:                 []string{TabSubAdmin},
		SubAdminManagedUsers: []string{"bob"},
		// SubAdminCanToggleEnabled left false on purpose.
	}

	w := doSubAdminRequest(t, caller, http.MethodPut, "/api/subadmin/users/bob/enabled", map[string]bool{"enabled": false})
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without the capability flag, got %d: %s", w.Code, w.Body.String())
	}
	bob, _ := GetUser("bob")
	if !bob.Enabled {
		t.Fatal("bob should still be enabled — the forbidden request must not have applied")
	}
}

func TestSubAdminToggleEnabledRejectsUnmanagedTarget(t *testing.T) {
	setupSubAdminTest(t)
	if err := CreateUser(&User{Username: "carol", Role: RoleViewer, Enabled: true}, "pw"); err != nil {
		t.Fatalf("CreateUser carol: %v", err)
	}
	caller := &User{
		Username: "suba", Role: RoleViewer, Tabs: []string{TabSubAdmin},
		SubAdminCanToggleEnabled: true,
		SubAdminManagedUsers:     []string{"someone-else"}, // NOT carol
	}

	w := doSubAdminRequest(t, caller, http.MethodPut, "/api/subadmin/users/carol/enabled", map[string]bool{"enabled": false})
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for an unmanaged target, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubAdminToggleEnabledRejectsAdminTargetEvenIfListed(t *testing.T) {
	setupSubAdminTest(t)
	if err := CreateUser(&User{Username: "root2", Role: RoleAdmin, Enabled: true}, "pw"); err != nil {
		t.Fatalf("CreateUser root2: %v", err)
	}
	// Simulates a stale/tampered managed-users list naming an admin account —
	// managedTarget must refuse this regardless of what the list says.
	caller := &User{
		Username: "suba", Role: RoleViewer, Tabs: []string{TabSubAdmin},
		SubAdminCanToggleEnabled: true,
		SubAdminManagedUsers:     []string{"root2"},
	}

	w := doSubAdminRequest(t, caller, http.MethodPut, "/api/subadmin/users/root2/enabled", map[string]bool{"enabled": false})
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 — an admin account must never be toggleable here, got %d: %s", w.Code, w.Body.String())
	}
	root2, _ := GetUser("root2")
	if !root2.Enabled {
		t.Fatal("root2 should still be enabled")
	}
}

func TestSubAdminToggleEnabledHappyPathIsAudited(t *testing.T) {
	setupSubAdminTest(t)
	if err := CreateUser(&User{Username: "dave", Role: RoleViewer, Enabled: true}, "pw"); err != nil {
		t.Fatalf("CreateUser dave: %v", err)
	}
	caller := &User{
		Username: "suba", Role: RoleViewer, Tabs: []string{TabSubAdmin},
		SubAdminCanToggleEnabled: true,
		SubAdminManagedUsers:     []string{"dave"},
	}

	w := doSubAdminRequest(t, caller, http.MethodPut, "/api/subadmin/users/dave/enabled", map[string]bool{"enabled": false})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	dave, _ := GetUser("dave")
	if dave.Enabled {
		t.Fatal("expected dave to be disabled")
	}

	entries, err := querySubAdminAudit("", "")
	if err != nil {
		t.Fatalf("querySubAdminAudit: %v", err)
	}
	if len(entries) != 1 || entries[0].SubAdmin != "suba" || entries[0].TargetUser != "dave" || entries[0].Action != "toggle_enabled" {
		t.Fatalf("expected one audit entry for suba->dave toggle_enabled, got %+v", entries)
	}
}

func TestSubAdminResetPasswordNeverLeaksPasswordInAudit(t *testing.T) {
	setupSubAdminTest(t)
	if err := CreateUser(&User{Username: "erin", Role: RoleViewer, Enabled: true}, "pw"); err != nil {
		t.Fatalf("CreateUser erin: %v", err)
	}
	caller := &User{
		Username: "suba", Role: RoleViewer, Tabs: []string{TabSubAdmin},
		SubAdminCanResetPassword: true,
		SubAdminManagedUsers:     []string{"erin"},
	}

	w := doSubAdminRequest(t, caller, http.MethodPost, "/api/subadmin/users/erin/password",
		map[string]any{"password": "S3cur3-Pass!", "force_change": true})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	erin, _ := GetUser("erin")
	if !erin.MustChangePassword {
		t.Fatal("expected MustChangePassword=true after force_change reset")
	}
	if _, ok := Authenticate("erin", "S3cur3-Pass!"); !ok {
		t.Fatal("expected the new password to work")
	}

	entries, _ := querySubAdminAudit("", "")
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	if bytes.Contains([]byte(entries[0].Detail), []byte("S3cur3-Pass!")) {
		t.Fatal("audit detail must never contain the plaintext password")
	}
}
