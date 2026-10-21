package auth

// api_subadmin.go — the delegated "sub admin" console's backend: a scoped
// subset of user management handed to a non-admin account over its own
// assigned group (User.SubAdminManagedUsers), gated per action by the
// specific SubAdminCan* flag the top admin granted for that account — never
// full RoleAdmin. See models.go's "Sub Admin delegation" doc comment for
// the overall design, and subadmin_audit.go for the write-up every action
// here leaves for the top admin to review.
//
//   GET   /api/subadmin/users                     — list the caller's managed users
//   PUT   /api/subadmin/users/{username}/enabled   — {enabled: bool}
//   POST  /api/subadmin/users/{username}/password  — {password, force_change}
//
// Camera access is deliberately NOT delegable here — that's a separate,
// more involved mechanism elsewhere (per-camera grants AND "địa bàn"/region
// assignment together), out of scope for this console.
//
// Every target username is always re-validated server-side against
// managedTarget — the admin.html-side "managed users" picker is a
// convenience, not a trust boundary; a request naming a username outside
// the caller's SubAdminManagedUsers (or an admin/sub-admin account) is
// rejected regardless of what the client sends.

import (
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strings"
)

func registerSubAdminHandlers() {
	http.HandleFunc("/api/subadmin/users", subAdminUsersHandler)
	http.HandleFunc("/api/subadmin/users/", subAdminUserActionHandler)
}

// requireSubAdmin returns the caller if authenticated and either a real
// delegated sub admin (TabSubAdmin in EffectiveTabs) or an admin — admins
// bypass every other permission check in this codebase (userCanAccessPath's
// own Role==RoleAdmin shortcut already lets them reach these paths at all),
// so this console behaves the same way rather than 403ing an admin who
// opens it, consistent with every other admin-only or tab-gated area here.
func requireSubAdmin(w http.ResponseWriter, r *http.Request) (*User, bool) {
	u, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	if u.Role == RoleAdmin || slices.Contains(u.EffectiveTabs(), TabSubAdmin) {
		return u, true
	}
	http.Error(w, "forbidden", http.StatusForbidden)
	return nil, false
}

// managedTarget resolves targetUsername and verifies it's one caller may
// act on: it must exist, be a plain viewer (never admin or another sub
// admin's own account — a sub admin can only ever act on an ordinary
// managed viewer, no matter what SubAdminManagedUsers says), and actually
// be listed in caller's SubAdminManagedUsers. Writes the HTTP error itself
// on failure, same convention as requireAdmin-style helpers elsewhere.
func managedTarget(w http.ResponseWriter, caller *User, targetUsername string) (*User, bool) {
	target, found := GetUser(targetUsername)
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return nil, false
	}
	if target.Role != RoleViewer || slices.Contains(target.EffectiveTabs(), TabSubAdmin) {
		http.Error(w, "forbidden: not a manageable account", http.StatusForbidden)
		return nil, false
	}
	// Admins bypass requireSubAdmin's own tab check above but are NOT
	// granted an implicit "manage everyone" here — SubAdminManagedUsers is
	// still the actual boundary even for an admin using this console (an
	// admin wanting unrestricted user management already has admin.html).
	if !slices.Contains(caller.SubAdminManagedUsers, targetUsername) {
		http.Error(w, "forbidden: not in your managed group", http.StatusForbidden)
		return nil, false
	}
	return target, true
}

// sanitizeSubAdminManagedUsers filters a requested SubAdminManagedUsers list
// down to usernames that actually exist, are plain viewers, aren't
// themselves a sub admin, and aren't selfUsername — called from
// usersHandler's create/update paths so this boundary is enforced at the
// moment an admin sets it, not just re-checked (though it still is, in
// managedTarget) every time it's used.
func sanitizeSubAdminManagedUsers(requested []string, selfUsername string) []string {
	out := make([]string, 0, len(requested))
	seen := make(map[string]bool, len(requested))
	for _, username := range requested {
		if username == "" || username == selfUsername || seen[username] {
			continue
		}
		u, found := GetUser(username)
		if !found || u.Role != RoleViewer || slices.Contains(u.EffectiveTabs(), TabSubAdmin) {
			continue
		}
		seen[username] = true
		out = append(out, username)
	}
	sort.Strings(out)
	return out
}

type subAdminManagedUserOut struct {
	Username           string `json:"username"`
	Enabled            bool   `json:"enabled"`
	MustChangePassword bool   `json:"must_change_password"`
}

// subAdminUsersHandler  GET /api/subadmin/users
func subAdminUsersHandler(w http.ResponseWriter, r *http.Request) {
	caller, ok := requireSubAdmin(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	out := make([]subAdminManagedUserOut, 0, len(caller.SubAdminManagedUsers))
	for _, username := range caller.SubAdminManagedUsers {
		u, found := GetUser(username)
		// Defensive: a managed username may have been deleted, promoted to
		// admin, or made a sub admin itself since this list was set — skip
		// it silently rather than erroring the whole list for one stale
		// entry (the admin.html picker self-corrects this on next save).
		if !found || u.Role != RoleViewer || slices.Contains(u.EffectiveTabs(), TabSubAdmin) {
			continue
		}
		out = append(out, subAdminManagedUserOut{
			Username:           u.Username,
			Enabled:            u.Enabled,
			MustChangePassword: u.MustChangePassword,
		})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Username) < strings.ToLower(out[j].Username) })
	responseJSON(w, out)
}

// subAdminUserActionHandler handles every /api/subadmin/users/{username}/{action} path.
func subAdminUserActionHandler(w http.ResponseWriter, r *http.Request) {
	caller, ok := requireSubAdmin(w, r)
	if !ok {
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, "/api/subadmin/users/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.Error(w, "expected /api/subadmin/users/{username}/{action}", http.StatusBadRequest)
		return
	}
	targetUsername, action := parts[0], parts[1]
	ip := clientIP(r)

	switch action {
	case "enabled":
		subAdminToggleEnabled(w, r, caller, targetUsername, ip)
	case "password":
		subAdminResetPassword(w, r, caller, targetUsername, ip)
	default:
		http.Error(w, "unknown action", http.StatusNotFound)
	}
}

func subAdminToggleEnabled(w http.ResponseWriter, r *http.Request, caller *User, targetUsername, ip string) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !caller.SubAdminCanToggleEnabled && caller.Role != RoleAdmin {
		http.Error(w, "forbidden: this sub admin cannot enable/disable accounts", http.StatusForbidden)
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	target, ok := managedTarget(w, caller, targetUsername)
	if !ok {
		return
	}
	target.Enabled = req.Enabled
	if err := UpdateUser(target, ""); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	detail := "enabled=false"
	if req.Enabled {
		detail = "enabled=true"
	}
	recordSubAdminAction(caller.Username, "toggle_enabled", target.Username, detail, ip)
	responseJSON(w, map[string]bool{"enabled": target.Enabled})
}

func subAdminResetPassword(w http.ResponseWriter, r *http.Request, caller *User, targetUsername, ip string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !caller.SubAdminCanResetPassword && caller.Role != RoleAdmin {
		http.Error(w, "forbidden: this sub admin cannot reset passwords", http.StatusForbidden)
		return
	}
	var req struct {
		Password    string `json:"password"`
		ForceChange bool   `json:"force_change"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := validatePassword(req.Password); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	target, ok := managedTarget(w, caller, targetUsername)
	if !ok {
		return
	}
	target.MustChangePassword = req.ForceChange
	if err := UpdateUser(target, req.Password); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Never record the password itself — only that a reset happened and
	// whether it also forced a change at next login.
	detail := "force_change=false"
	if req.ForceChange {
		detail = "force_change=true"
	}
	recordSubAdminAction(caller.Username, "reset_password", target.Username, detail, ip)
	responseJSON(w, map[string]string{"status": "ok"})
}
