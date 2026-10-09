package auth

// device_binding.go — WebAuthn-based "trusted device" registry.
//
// Not all data needs a verified device: which categories are gated is
// chosen per user via User.DeviceBindingScopes (models.go), picked from the
// fixed catalog below. A TrustedDevice starts "pending" right after the
// browser completes a WebAuthn registration ceremony (see webauthn.go) and
// only counts toward access once an admin approves it — mirrors the
// register-then-admin-approves flow already used for other review queues
// in this codebase (e.g. camera type assignment).
//
// Adding a new gated data category later is just one more entry in
// deviceScopeCatalog (key, label, path prefixes) — no architecture change.

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// DeviceScope is one selectable category of sensitive data that can be
// gated behind a verified device.
type DeviceScope struct {
	Key          string   `json:"key"`
	Label        string   `json:"label"`
	PathPrefixes []string `json:"-"` // matched with strings.HasPrefix against r.URL.Path
}

var deviceScopeCatalog = []DeviceScope{
	{
		Key:   "cam_stream",
		Label: "Xem video trực tiếp camera",
		PathPrefixes: []string{
			"/api/webrtc", "/api/ws", "/api/hls", "/api/mjpeg", "/api/mp4",
			"/api/proxy/hls", "/api/proxy/mp4", "/api/proxy/ws",
		},
	},
	{
		Key:          "cam_snapshot",
		Label:        "Xem ảnh snapshot camera",
		PathPrefixes: []string{"/api/frame", "/api/proxy/frame"},
	},
	{
		Key:          "incidents_export",
		Label:        "Nhập/xuất dữ liệu sự cố",
		PathPrefixes: []string{"/api/incidents"},
	},
	{
		Key:          "counting_export",
		Label:        "Xuất dữ liệu đếm xe/trạm",
		PathPrefixes: []string{"/api/counting/export-csv", "/api/counting/data"},
	},
	{
		Key:          "gps_locations",
		Label:        "Toạ độ GPS camera/trạm đếm",
		PathPrefixes: []string{"/api/camera-locations", "/api/counting/stations"},
	},
	{
		Key:          "monitor_devices",
		Label:        "Giám sát thiết bị hạ tầng",
		PathPrefixes: []string{"/api/device-stats"},
	},
	{
		Key:          "ai_event_image",
		Label:        "Xem ảnh sự cố AI (OMNIA)",
		PathPrefixes: []string{"/api/aievent/image"},
	},
}

// DeviceScopeCatalog returns the fixed catalog (for the admin UI / API).
func DeviceScopeCatalog() []DeviceScope { return deviceScopeCatalog }

// ValidDeviceScopeKey reports whether key names a real catalog entry.
func ValidDeviceScopeKey(key string) bool {
	for _, s := range deviceScopeCatalog {
		if s.Key == key {
			return true
		}
	}
	return false
}

// validDeviceScopeKeys filters keys down to those naming a real catalog
// entry, dropping anything else — used when saving a user's
// DeviceBindingScopes from API input so a stale or mistyped key can never
// silently persist as a no-op scope nothing ever matches.
func validDeviceScopeKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if ValidDeviceScopeKey(k) {
			out = append(out, k)
		}
	}
	return out
}

// deviceScopeForPath returns the scope key gating path, or "" if path isn't
// covered by any scope at all (nothing to gate).
func deviceScopeForPath(path string) string {
	for _, s := range deviceScopeCatalog {
		for _, p := range s.PathPrefixes {
			if strings.HasPrefix(path, p) {
				return s.Key
			}
		}
	}
	return ""
}

// ── TrustedDevice store ──────────────────────────────────────────────

const (
	DeviceStatusPending  = "pending"
	DeviceStatusApproved = "approved"
	DeviceStatusRevoked  = "revoked"
)

// TrustedDevice is one WebAuthn credential registered by a user, pending or
// granted admin approval to satisfy device-binding checks. Credential is the
// library's own record type, stored in full (rather than picked apart into
// our own fields) because go-webauthn's docs call every one of its fields —
// including the raw attestation bytes — required for later re-verification
// and replay-counter tracking; see [webauthn.Credential]'s doc comment.
type TrustedDevice struct {
	ID         string              `json:"id"`
	Username   string              `json:"username"`
	Label      string              `json:"label"` // user-supplied, e.g. "Laptop Dell - Windows 11"
	Credential webauthn.Credential `json:"credential"`
	Status     string              `json:"status"` // pending | approved | revoked
	CreatedAt  time.Time           `json:"created_at"`
	ApprovedAt *time.Time          `json:"approved_at,omitempty"`
	ApprovedBy string              `json:"approved_by,omitempty"`
	LastUsedAt *time.Time          `json:"last_used_at,omitempty"`
	LastUsedIP string              `json:"last_used_ip,omitempty"`
}

type deviceBindingStore struct {
	mu      sync.RWMutex
	path    string
	devices []*TrustedDevice
}

var devBindStore *deviceBindingStore

func initDeviceBinding(path string) error {
	devBindStore = &deviceBindingStore{path: path}
	return devBindStore.load()
}

func (s *deviceBindingStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		s.devices = []*TrustedDevice{}
		return nil
	}
	if err != nil {
		return err
	}
	var onDisk struct {
		Devices []*TrustedDevice `json:"devices"`
	}
	if err := json.Unmarshal(data, &onDisk); err != nil {
		return err
	}
	if onDisk.Devices == nil {
		onDisk.Devices = []*TrustedDevice{}
	}
	s.devices = onDisk.Devices
	return nil
}

func (s *deviceBindingStore) saveLocked() error {
	data, err := json.MarshalIndent(struct {
		Devices []*TrustedDevice `json:"devices"`
	}{s.devices}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0600)
}

// addPendingDevice stores a new TrustedDevice for username/label wrapping
// cred (Status forced to "pending", CreatedAt set to now).
func addPendingDevice(username, label string, cred webauthn.Credential) (*TrustedDevice, error) {
	d := &TrustedDevice{
		ID:         genShortID(),
		Username:   username,
		Label:      label,
		Credential: cred,
		Status:     DeviceStatusPending,
		CreatedAt:  time.Now(),
	}
	devBindStore.mu.Lock()
	defer devBindStore.mu.Unlock()
	devBindStore.devices = append(devBindStore.devices, d)
	if err := devBindStore.saveLocked(); err != nil {
		return nil, err
	}
	return d, nil
}

// listAllTrustedDevices returns every device (any status), newest first.
func listAllTrustedDevices() []*TrustedDevice {
	if devBindStore == nil {
		return []*TrustedDevice{}
	}
	devBindStore.mu.RLock()
	defer devBindStore.mu.RUnlock()
	out := make([]*TrustedDevice, len(devBindStore.devices))
	for i, d := range devBindStore.devices {
		cp := *d
		out[len(devBindStore.devices)-1-i] = &cp // newest first
	}
	return out
}

// listDevicesForUser returns every device belonging to username (any status).
func listDevicesForUser(username string) []*TrustedDevice {
	if devBindStore == nil {
		return []*TrustedDevice{}
	}
	devBindStore.mu.RLock()
	defer devBindStore.mu.RUnlock()
	var out []*TrustedDevice
	for _, d := range devBindStore.devices {
		if d.Username == username {
			cp := *d
			out = append(out, &cp)
		}
	}
	if out == nil {
		out = []*TrustedDevice{}
	}
	return out
}

// getTrustedDevice looks up one device by ID.
func getTrustedDevice(id string) (*TrustedDevice, bool) {
	if devBindStore == nil {
		return nil, false
	}
	devBindStore.mu.RLock()
	defer devBindStore.mu.RUnlock()
	for _, d := range devBindStore.devices {
		if d.ID == id {
			cp := *d
			return &cp, true
		}
	}
	return nil, false
}

// findApprovedDeviceByCredentialID looks up an approved device by its
// WebAuthn credential ID — used during the login/assertion ceremony.
func findApprovedDeviceByCredentialID(credentialID []byte) (*TrustedDevice, bool) {
	if devBindStore == nil {
		return nil, false
	}
	devBindStore.mu.RLock()
	defer devBindStore.mu.RUnlock()
	for _, d := range devBindStore.devices {
		if d.Status == DeviceStatusApproved && bytes.Equal(d.Credential.ID, credentialID) {
			cp := *d
			return &cp, true
		}
	}
	return nil, false
}

// approveDevice marks a pending device approved. Returns false if id wasn't
// found or wasn't pending.
func approveDevice(id, approvedBy string) bool {
	devBindStore.mu.Lock()
	defer devBindStore.mu.Unlock()
	for _, d := range devBindStore.devices {
		if d.ID == id {
			d.Status = DeviceStatusApproved
			now := time.Now()
			d.ApprovedAt = &now
			d.ApprovedBy = approvedBy
			_ = devBindStore.saveLocked()
			return true
		}
	}
	return false
}

// revokeDevice marks a device revoked (from any prior status). Returns
// false if id wasn't found.
func revokeDevice(id string) bool {
	devBindStore.mu.Lock()
	defer devBindStore.mu.Unlock()
	for _, d := range devBindStore.devices {
		if d.ID == id {
			d.Status = DeviceStatusRevoked
			_ = devBindStore.saveLocked()
			return true
		}
	}
	return false
}

// touchDeviceUsage records a successful verification: persists the updated
// Credential (its SignCount/Flags advance on every assertion — see
// [webauthn.WebAuthn.ValidateLogin]) and last-used metadata.
func touchDeviceUsage(id string, cred webauthn.Credential, ip string) {
	devBindStore.mu.Lock()
	defer devBindStore.mu.Unlock()
	for _, d := range devBindStore.devices {
		if d.ID == id {
			d.Credential = cred
			now := time.Now()
			d.LastUsedAt = &now
			d.LastUsedIP = ip
			_ = devBindStore.saveLocked()
			return
		}
	}
}

// userHasApprovedDevice reports whether username has at least one approved,
// non-revoked device — used to decide whether the step-up prompt should
// offer "verify" (an approved device exists) or point at self-enrollment.
func userHasApprovedDevice(username string) bool {
	for _, d := range listDevicesForUser(username) {
		if d.Status == DeviceStatusApproved {
			return true
		}
	}
	return false
}
