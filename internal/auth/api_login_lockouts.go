package auth

// Admin tool for the brute-force lockout mechanism in login_throttle.go:
// lists every username/IP currently tracked (locked or approaching it) and
// lets an admin manually clear a lockout before its timer would naturally
// expire — e.g. a legitimate user mistyped their password 5 times, or an
// office NAT IP got locked out because of one user's mistakes.

import (
	"encoding/json"
	"net/http"
)

func registerLoginLockoutsHandler() {
	http.HandleFunc("/api/login-lockouts", loginLockoutsHandler)
	http.HandleFunc("/api/login-lockouts/unlock", loginLockoutsUnlockHandler)
}

// loginLockoutsHandler GET /api/login-lockouts  (admin only)
func loginLockoutsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, ok := UserFromContext(r.Context())
	if !ok || user.Role != RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	s := GetSettings()
	responseJSON(w, map[string]any{
		"entries":               ListLoginLockouts(),
		"max_login_failures":    maxLoginFailures(),
		"login_lockout_minutes": int(loginLockoutWindow().Minutes()),
		"configured": map[string]any{
			"max_login_failures":    s.MaxLoginFailures,
			"login_lockout_minutes": s.LoginLockoutMinutes,
		},
	})
}

// loginLockoutsUnlockHandler POST /api/login-lockouts/unlock
// {"kind":"user"|"ip","identifier":"..."}  (admin only)
func loginLockoutsUnlockHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	admin, ok := UserFromContext(r.Context())
	if !ok || admin.Role != RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		Kind       string `json:"kind"`
		Identifier string `json:"identifier"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Kind != "user" && req.Kind != "ip" {
		http.Error(w, `kind must be "user" or "ip"`, http.StatusBadRequest)
		return
	}
	if req.Identifier == "" {
		http.Error(w, "identifier required", http.StatusBadRequest)
		return
	}

	existed := UnlockLogin(req.Kind, req.Identifier)

	// Audit trail: shows up in Logs → Cảnh báo bảo mật so there's a record
	// of who manually reopened an account/IP and when.
	label, ip := "", ""
	if req.Kind == "user" {
		label = req.Identifier
	} else {
		ip = req.Identifier
	}
	raiseAlert("lockout_cleared", AlertSeverityLow, label, ip,
		"Admin \""+admin.Username+"\" đã mở khóa thủ công "+lockoutKindLabelVI(req.Kind)+" \""+req.Identifier+"\".")

	responseJSON(w, map[string]any{"ok": true, "existed": existed})
}

func lockoutKindLabelVI(kind string) string {
	if kind == "ip" {
		return "địa chỉ IP"
	}
	return "tài khoản"
}
