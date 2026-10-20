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
//   GET   /api/subadmin/cameras                    — cameras the caller may grant (its own accessible streams)
//   PUT   /api/subadmin/users/{username}/cameras   — {camera_ids: [...]}  (masked IDs, see below)
//
// Every target username is always re-validated server-side against
// managedTarget — the admin.html-side "managed users" picker is a
// convenience, not a trust boundary; a request naming a username outside
// the caller's SubAdminManagedUsers (or an admin/sub-admin account) is
// rejected regardless of what the client sends.
//
// Camera grants are deliberately masked-ID based (like the ordinary,
// non-admin /api/cameras response), not real stream names — a sub admin
// gets exactly the same "real camera names are sensitive" treatment as any
// other non-admin account (see AllowCamNames elsewhere in this package),
// never more, regardless of which managed user is being edited.

import (
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
)

func registerSubAdminHandlers() {
	http.HandleFunc("/api/subadmin/users", subAdminUsersHandler)
	http.HandleFunc("/api/subadmin/users/", subAdminUserActionHandler)
	http.HandleFunc("/api/subadmin/cameras", subAdminCamerasHandler)
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
	Username           string   `json:"username"`
	Enabled            bool     `json:"enabled"`
	MustChangePassword bool     `json:"must_change_password"`
	// CameraIDs are masked IDs for the subset of the target's current
	// Streams that fall within the caller's OWN accessible scope — the only
	// ones this caller could meaningfully toggle via the cameras endpoint
	// below. Cameras the target has outside that scope aren't reported here
	// at all (the caller has no way to act on them, and no need to know
	// what they are).
	CameraIDs []string `json:"camera_ids"`
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

	callerScope := make(map[string]bool)
	for _, name := range AccessibleStreamNames(caller) {
		callerScope[name] = true
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
		camIDs := make([]string, 0, len(u.Streams))
		for _, name := range u.Streams {
			if callerScope[name] {
				camIDs = append(camIDs, StreamID(name))
			}
		}
		sort.Strings(camIDs)
		out = append(out, subAdminManagedUserOut{
			Username:           u.Username,
			Enabled:            u.Enabled,
			MustChangePassword: u.MustChangePassword,
			CameraIDs:          camIDs,
		})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Username) < strings.ToLower(out[j].Username) })
	responseJSON(w, out)
}

// subAdminCamerasHandler  GET /api/subadmin/cameras — the caller's own
// accessible camera list (masked ID + label), i.e. everything they could
// potentially grant to a managed user.
func subAdminCamerasHandler(w http.ResponseWriter, r *http.Request) {
	caller, ok := requireSubAdmin(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	type camOut struct {
		ID    string `json:"id"`
		Label string `json:"label,omitempty"`
	}
	locMu.RLock()
	locByName := make(map[string]*CameraLocation, len(locMap))
	for name, loc := range locMap {
		locByName[name] = loc
	}
	locMu.RUnlock()

	names := AccessibleStreamNames(caller)
	out := make([]camOut, 0, len(names))
	for _, name := range names {
		item := camOut{ID: StreamID(name)}
		if loc := locByName[name]; loc != nil {
			item.Label = loc.Label
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		li, lj := out[i].Label, out[j].Label
		if li == "" {
			li = out[i].ID
		}
		if lj == "" {
			lj = out[j].ID
		}
		return li < lj
	})
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
	case "cameras":
		subAdminGrantCameras(w, r, caller, targetUsername, ip)
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

func subAdminGrantCameras(w http.ResponseWriter, r *http.Request, caller *User, targetUsername, ip string) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !caller.SubAdminCanGrantCameras && caller.Role != RoleAdmin {
		http.Error(w, "forbidden: this sub admin cannot grant camera access", http.StatusForbidden)
		return
	}
	var req struct {
		CameraIDs []string `json:"camera_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	target, ok := managedTarget(w, caller, targetUsername)
	if !ok {
		return
	}

	// The caller's own accessible scope bounds everything below — a camera
	// outside it is simply not resolvable to a name the caller is allowed
	// to touch, regardless of what camera_ids the request names.
	callerScope := make(map[string]bool)
	for _, name := range AccessibleStreamNames(caller) {
		callerScope[name] = true
	}

	desired := make(map[string]bool, len(req.CameraIDs))
	for _, id := range req.CameraIDs {
		if name, found := StreamNameByID(id); found && callerScope[name] {
			desired[name] = true
		}
	}

	// New Streams = (target's current list, minus anything in caller's own
	// scope) UNION (desired, which is already filtered to caller's scope).
	// This only ever adds/removes cameras within the caller's own reach —
	// access the top admin granted directly outside that reach is left
	// completely untouched, added or removed count included in the audit
	// detail below.
	kept := make([]string, 0, len(target.Streams))
	for _, name := range target.Streams {
		if !callerScope[name] {
			kept = append(kept, name)
		}
	}
	var added, removed int
	hadBefore := make(map[string]bool, len(target.Streams))
	for _, name := range target.Streams {
		hadBefore[name] = true
	}
	newStreams := kept
	for name := range desired {
		newStreams = append(newStreams, name)
		if !hadBefore[name] {
			added++
		}
	}
	for name := range callerScope {
		if hadBefore[name] && !desired[name] {
			removed++
		}
	}
	sort.Strings(newStreams)
	target.Streams = newStreams

	if err := UpdateUser(target, ""); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	recordSubAdminAction(caller.Username, "grant_cameras", target.Username,
		"added="+strconv.Itoa(added)+" removed="+strconv.Itoa(removed), ip)
	responseJSON(w, map[string]any{"streams_count": len(target.Streams)})
}
