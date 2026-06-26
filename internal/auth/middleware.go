package auth

import (
	"context"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// ── Presence tracking ──────────────────────────────────────────────
// JWT auth is stateless, so "online users" is approximated by recording
// the last authenticated request per user (username key).

type presenceInfo struct {
	LastSeen   time.Time
	RemoteAddr string // client IP (X-Forwarded-For preferred, else RemoteAddr host)
	UserAgent  string
}

var (
	presenceMu sync.Mutex
	presence   = map[string]presenceInfo{}
)

func touchPresence(r *http.Request, username string) {
	// Prefer the leftmost X-Forwarded-For address (closest to real client).
	ip := r.RemoteAddr
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		ip = strings.TrimSpace(strings.SplitN(fwd, ",", 2)[0])
	} else if h, _, err := net.SplitHostPort(ip); err == nil {
		ip = h
	}
	presenceMu.Lock()
	presence[username] = presenceInfo{
		LastSeen:   time.Now(),
		RemoteAddr: ip,
		UserAgent:  r.Header.Get("User-Agent"),
	}
	presenceMu.Unlock()
}

// ActiveUsers returns the number of distinct users who made an
// authenticated request within the given window (e.g. 5 minutes).
func ActiveUsers(window time.Duration) int {
	cutoff := time.Now().Add(-window)
	presenceMu.Lock()
	defer presenceMu.Unlock()
	n := 0
	for u, info := range presence {
		if info.LastSeen.After(cutoff) {
			n++
		} else if time.Since(info.LastSeen) > 24*time.Hour {
			delete(presence, u) // garbage-collect stale entries
		}
	}
	return n
}

// ActiveUserDetail holds per-user presence info for the activity API.
type ActiveUserDetail struct {
	Username    string `json:"username"`
	Role        string `json:"role"`
	LastSeenSec int64  `json:"last_seen_sec"` // seconds since last request
	RemoteAddr  string `json:"remote_addr"`   // client IP
	UserAgent   string `json:"user_agent"`
}

// ActiveUserDetails returns details for all users seen within window.
func ActiveUserDetails(window time.Duration) []ActiveUserDetail {
	cutoff := time.Now().Add(-window)
	now := time.Now()
	presenceMu.Lock()
	defer presenceMu.Unlock()
	var out []ActiveUserDetail
	for username, info := range presence {
		if !info.LastSeen.After(cutoff) {
			continue
		}
		role := RoleViewer
		if u, ok := GetUser(username); ok {
			role = u.Role
		}
		out = append(out, ActiveUserDetail{
			Username:    username,
			Role:        role,
			LastSeenSec: int64(now.Sub(info.LastSeen).Seconds()),
			RemoteAddr:  info.RemoteAddr,
			UserAgent:   info.UserAgent,
		})
	}
	// sort: most recently active first
	for i := 0; i < len(out)-1; i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].LastSeenSec < out[i].LastSeenSec {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// Middleware wraps an http.Handler with JWT auth + permission checks.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow public paths (login, logout) and static files without auth
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// Allow internal loopback requests from the counting module
		if r.Header.Get("X-Internal") == "counting" && isLoopback(r.RemoteAddr) {
			next.ServeHTTP(w, r)
			return
		}

		token := extractToken(r)
		if token == "" {
			respondUnauthorized(w, r)
			return
		}

		claims, err := ParseToken(token)
		if err != nil {
			respondUnauthorized(w, r)
			return
		}

		user, ok := GetUser(claims.Username)
		if !ok || !user.Enabled {
			respondUnauthorized(w, r)
			return
		}

		// Check path permission
		if !userCanAccessPath(user, r.URL.Path) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		touchPresence(r, user.Username)

		ctx := context.WithValue(r.Context(), userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserFromContext retrieves the authenticated user from a request context.
func UserFromContext(ctx context.Context) (*User, bool) {
	u, ok := ctx.Value(userContextKey).(*User)
	return u, ok
}

// CanAccessStream reports whether the user in ctx may view streamName.
func CanAccessStream(ctx context.Context, streamName string) bool {
	u, ok := UserFromContext(ctx)
	if !ok {
		return false
	}
	if u.Role == RoleAdmin {
		return true
	}
	// Empty stream list for viewer = no access
	return slices.Contains(u.Streams, streamName)
}

// HasTab reports whether the user in ctx has the given tab permission.
func HasTab(ctx context.Context, tab string) bool {
	u, ok := UserFromContext(ctx)
	if !ok {
		return false
	}
	return slices.Contains(u.EffectiveTabs(), tab)
}

// tabExtraPaths maps tab keys to API paths accessible only to users with that tab.
// These are checked in addition to viewerDefaultPaths.
var tabExtraPaths = map[string][]string{
	TabMonitor:   {"/api/system/stats"},
	TabDashboard: {"/api/dashboard", "/api/heatmap-cfg"},
	TabLog:       {"/api/log"},
	TabConfig:    {"/api/config", "/api/restart"},
	TabCounting:  {"/api/counting"},
}

func extractToken(r *http.Request) string {
	// 1. Authorization: Bearer <token>  (regular HTTP requests)
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	// 2. Cookie (set by login handler)
	if cookie, err := r.Cookie("go2rtc_token"); err == nil {
		return cookie.Value
	}
	// 3. ?token= query param  ← used by WebSocket, MJPEG <img>, HLS <video> etc.
	//    WebSocket JS cannot set Authorization headers, so token must be in URL.
	return r.URL.Query().Get("token")
}

func userCanAccessPath(u *User, path string) bool {
	if u.Role == RoleAdmin {
		return true
	}
	// Use user-specific allow list if defined, otherwise use role defaults
	allowed := u.AllowPaths
	if len(allowed) == 0 {
		allowed = viewerDefaultPaths
	}
	for _, p := range allowed {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	// Check tab-specific extra paths
	for _, tab := range u.EffectiveTabs() {
		for _, p := range tabExtraPaths[tab] {
			if strings.HasPrefix(path, p) {
				return true
			}
		}
	}
	// Station data access for map users with station permissions.
	// These paths are accessible without the "counting" tab — only map tab
	// + allow_view_stations or allow_config_stations is required.
	if u.AllowViewStations || u.AllowConfigStations {
		for _, p := range []string{"/api/counting/stations", "/api/counting/station-types", "/api/counting/summary", "/api/counting/rolling15", "/api/counting/data", "/api/counting/export-csv"} {
			if strings.HasPrefix(path, p) {
				return true
			}
		}
	}
	if u.AllowConfigStations {
		if strings.HasPrefix(path, "/api/counting/cameras") {
			return true
		}
	}
	// Monitor sub-permissions
	if slices.Contains(u.EffectiveTabs(), TabMonitor) {
		if u.AllowMonitorStreaming {
			if strings.HasPrefix(path, "/api/system/activity") {
				return true
			}
		}
		if u.AllowMonitorSnapshot {
			if strings.HasPrefix(path, "/api/proxy/snapshot-stats") {
				return true
			}
		}
		if u.AllowMonitorDevices {
			if strings.HasPrefix(path, "/api/device-stats") {
				return true
			}
		}
		if u.AllowMonitorWorkers {
			if strings.HasPrefix(path, "/api/workers/status") {
				return true
			}
		}
	}
	return false
}

func isLoopback(remoteAddr string) bool {
	h := remoteAddr
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		h = host
	}
	return h == "127.0.0.1" || h == "::1"
}

func isPublicPath(path string) bool {
	// Explicit public API endpoints (no auth required)
	public := []string{
		"/api/auth/login",
		"/api/auth/logout",
	}
	for _, p := range public {
		if path == p {
			return true
		}
	}
	// Everything that is NOT under /api/ is a static file — served without auth.
	// The Go binary embeds www/*.html, www/*.js, www/*.css etc.
	return !strings.HasPrefix(path, "/api/")
}

// respondUnauthorized returns 401. For WebSocket upgrade requests it also
// closes the connection cleanly (browsers don't retry WS on 401 without help).
func respondUnauthorized(w http.ResponseWriter, r *http.Request) {
	// WebSocket upgrade requests need a plain-text 401 so the browser JS
	// receives a proper close event and can redirect to /login.html.
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "401 Unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
}

