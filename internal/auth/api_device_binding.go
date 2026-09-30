package auth

// api_device_binding.go — HTTP handlers for the device-binding (WebAuthn)
// feature: self-service enrollment + step-up verification (any authenticated
// user, for their own account) and admin review (approve/revoke).

import (
	"net/http"
	"strings"
)

func registerDeviceBindingHandler() {
	http.HandleFunc("/api/auth/webauthn/register/begin", webauthnRegisterBeginHandler)
	http.HandleFunc("/api/auth/webauthn/register/finish", webauthnRegisterFinishHandler)
	http.HandleFunc("/api/auth/webauthn/verify/begin", webauthnVerifyBeginHandler)
	http.HandleFunc("/api/auth/webauthn/verify/finish", webauthnVerifyFinishHandler)
	http.HandleFunc("/api/device-scopes", deviceScopesHandler)
	http.HandleFunc("/api/device-bindings", deviceBindingsHandler)
	http.HandleFunc("/api/device-bindings/", deviceBindingActionHandler)
}

// webauthnRegisterBeginHandler POST /api/auth/webauthn/register/begin
// Any authenticated user enrolls a device for themselves.
func webauthnRegisterBeginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	creation, err := beginDeviceRegistration(user.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	responseJSON(w, creation)
}

// webauthnRegisterFinishHandler POST /api/auth/webauthn/register/finish?label=...
// Body is the raw PublicKeyCredential.toJSON() result from
// navigator.credentials.create(). Stores the verified credential as a
// pending TrustedDevice — it grants nothing until an admin approves it.
func webauthnRegisterFinishHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	cred, err := finishDeviceRegistration(user.Username, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	label := strings.TrimSpace(r.URL.Query().Get("label"))
	if label == "" {
		label = "Thiết bị chưa đặt tên"
	}
	dev, err := addPendingDevice(user.Username, label, *cred)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	responseJSON(w, deviceBindingSummary(dev))
}

// webauthnVerifyBeginHandler POST /api/auth/webauthn/verify/begin
// Step-up: starts an assertion ceremony against the caller's own approved
// devices only.
func webauthnVerifyBeginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	assertion, err := beginDeviceVerification(user.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	responseJSON(w, assertion)
}

// webauthnVerifyFinishHandler POST /api/auth/webauthn/verify/finish
// Body is the raw PublicKeyCredential.toJSON() result from
// navigator.credentials.get(). On success sets the short-lived
// device-verified cookie that satisfies every gated scope for this user
// (see middleware.go's requestHasVerifiedDevice).
func webauthnVerifyFinishHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if _, err := finishDeviceVerification(user.Username, clientIP(r), r); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	validFor := deviceVerifyValidDuration()
	token, err := signDeviceVerifiedToken(user.Username, validFor)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     deviceVerifiedCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isRequestTLS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(validFor.Seconds()),
	})
	w.WriteHeader(http.StatusNoContent)
}

// deviceScopesHandler GET /api/device-scopes — the fixed catalog, for the
// admin UI's per-scope checkboxes on the user edit form.
func deviceScopesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := UserFromContext(r.Context()); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	type scopeOut struct {
		Key   string `json:"key"`
		Label string `json:"label"`
	}
	catalog := DeviceScopeCatalog()
	out := make([]scopeOut, len(catalog))
	for i, s := range catalog {
		out[i] = scopeOut{Key: s.Key, Label: s.Label}
	}
	responseJSON(w, out)
}

// deviceBindingOut is the API-safe view of a TrustedDevice — never exposes
// the raw WebAuthn Credential (public key / attestation bytes).
type deviceBindingOut struct {
	ID         string  `json:"id"`
	Username   string  `json:"username"`
	Label      string  `json:"label"`
	Status     string  `json:"status"`
	CreatedAt  string  `json:"created_at"`
	ApprovedAt *string `json:"approved_at,omitempty"`
	ApprovedBy string  `json:"approved_by,omitempty"`
	LastUsedAt *string `json:"last_used_at,omitempty"`
	LastUsedIP string  `json:"last_used_ip,omitempty"`
}

func deviceBindingSummary(d *TrustedDevice) deviceBindingOut {
	out := deviceBindingOut{
		ID:         d.ID,
		Username:   d.Username,
		Label:      d.Label,
		Status:     d.Status,
		CreatedAt:  d.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		ApprovedBy: d.ApprovedBy,
		LastUsedIP: d.LastUsedIP,
	}
	if d.ApprovedAt != nil {
		s := d.ApprovedAt.Format("2006-01-02T15:04:05Z07:00")
		out.ApprovedAt = &s
	}
	if d.LastUsedAt != nil {
		s := d.LastUsedAt.Format("2006-01-02T15:04:05Z07:00")
		out.LastUsedAt = &s
	}
	return out
}

// deviceBindingsHandler GET /api/device-bindings[?all=1]
// Default: the caller's own devices. Admins may pass ?all=1 to list every
// user's devices (the pending-approval queue).
func deviceBindingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var devices []*TrustedDevice
	if r.URL.Query().Get("all") == "1" {
		if user.Role != RoleAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		devices = listAllTrustedDevices()
	} else {
		devices = listDevicesForUser(user.Username)
	}
	out := make([]deviceBindingOut, len(devices))
	for i, d := range devices {
		out[i] = deviceBindingSummary(d)
	}
	responseJSON(w, out)
}

// deviceBindingActionHandler POST /api/device-bindings/{id}/approve|revoke  (admin only)
func deviceBindingActionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	caller, ok := UserFromContext(r.Context())
	if !ok || caller.Role != RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/device-bindings/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" {
		http.Error(w, "expected /api/device-bindings/{id}/approve or /revoke", http.StatusBadRequest)
		return
	}
	id, action := parts[0], parts[1]
	if _, found := getTrustedDevice(id); !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	switch action {
	case "approve":
		approveDevice(id, caller.Username)
	case "revoke":
		revokeDevice(id)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	dev, _ := getTrustedDevice(id)
	responseJSON(w, deviceBindingSummary(dev))
}
