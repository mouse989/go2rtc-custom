package auth

import (
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Brute-force protection for POST /api/auth/login. Tracked independently by
// username and by client IP, so an attacker can't dodge the per-username
// lockout by spraying many usernames from one IP, nor dodge a per-IP limit
// by distributing guesses across many IPs against one account.
//
// Thresholds are admin-configurable (AppSettings.MaxLoginFailures /
// LoginLockoutMinutes, 0 = these defaults) so a site under heavy scanning
// can tighten them without a rebuild.
const (
	defaultMaxLoginFailures   = 5
	defaultLoginLockoutWindow = 5 * time.Minute
	loginStateMaxAge          = time.Hour // stale entries swept after this long unattempted
)

// maxLoginFailures returns the configured failure threshold before lockout.
func maxLoginFailures() int {
	if n := GetSettings().MaxLoginFailures; n > 0 {
		return n
	}
	return defaultMaxLoginFailures
}

// loginLockoutWindow returns the configured lockout duration.
func loginLockoutWindow() time.Duration {
	if m := GetSettings().LoginLockoutMinutes; m > 0 {
		return time.Duration(m) * time.Minute
	}
	return defaultLoginLockoutWindow
}

type loginAttemptState struct {
	failures    int
	lockedUntil time.Time
	lastAttempt time.Time
}

var (
	loginThrottleMu sync.Mutex
	loginThrottle   = map[string]*loginAttemptState{}
	loginSweepOnce  sync.Once
)

// startLoginThrottleSweeper periodically drops tracking entries nobody has
// touched in a while, so long-running servers exposed to the internet don't
// accumulate one entry per distinct attacker IP/username forever.
func startLoginThrottleSweeper() {
	loginSweepOnce.Do(func() {
		go func() {
			for range time.Tick(15 * time.Minute) {
				now := time.Now()
				loginThrottleMu.Lock()
				for k, s := range loginThrottle {
					if now.Sub(s.lastAttempt) > loginStateMaxAge {
						delete(loginThrottle, k)
					}
				}
				loginThrottleMu.Unlock()
			}
		}()
	})
}

// clientIP extracts the request's remote address, ignoring the port and
// preferring X-Forwarded-For's leftmost hop — same convention as
// touchPresence's IP resolution elsewhere in this package.
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if i := strings.IndexByte(fwd, ','); i > 0 {
			return strings.TrimSpace(fwd[:i])
		}
		return strings.TrimSpace(fwd)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// loginLocked reports whether key (a "user:"/"ip:"-prefixed identity) is
// currently locked out, and for how much longer.
func loginLocked(key string) (locked bool, remaining time.Duration) {
	loginThrottleMu.Lock()
	defer loginThrottleMu.Unlock()
	s := loginThrottle[key]
	if s == nil || s.lockedUntil.IsZero() || !time.Now().Before(s.lockedUntil) {
		return false, 0
	}
	return true, time.Until(s.lockedUntil)
}

// recordLoginFailure reports justLocked = true exactly once per lockout
// episode: loginHandler only ever calls this when the key isn't currently
// locked (it returns early via loginLocked otherwise), so reaching the
// threshold here always means a lockout is being freshly (re-)armed —
// callers use this to raise a security alert without spamming it on every
// blocked attempt during the lockout window.
func recordLoginFailure(key string) (justLocked bool) {
	loginThrottleMu.Lock()
	defer loginThrottleMu.Unlock()
	s := loginThrottle[key]
	if s == nil {
		s = &loginAttemptState{}
		loginThrottle[key] = s
	}
	s.failures++
	s.lastAttempt = time.Now()
	if s.failures >= maxLoginFailures() {
		s.lockedUntil = time.Now().Add(loginLockoutWindow())
		return true
	}
	return false
}

func recordLoginSuccess(key string) {
	loginThrottleMu.Lock()
	defer loginThrottleMu.Unlock()
	delete(loginThrottle, key)
}

// ── Admin monitoring & manual unlock ──────────────────────────────────
//
// LoginLockoutEntry is one tracked identity (a username or an IP) for the
// admin-facing "Khóa đăng nhập" tool: see /api/login-lockouts.
type LoginLockoutEntry struct {
	Kind          string `json:"kind"`            // "user" or "ip"
	Identifier    string `json:"identifier"`      // username (lowercased) or IP
	Failures      int    `json:"failures"`        // consecutive failures so far
	Locked        bool   `json:"locked"`          // currently locked out
	RemainingSec  int    `json:"remaining_sec"`   // seconds left in the lockout, 0 if not locked
	LastAttemptAt string `json:"last_attempt_at"` // RFC3339
}

// splitLoginKey parses a "user:"/"ip:"-prefixed throttle key back into
// (kind, identifier).
func splitLoginKey(key string) (kind, identifier string, ok bool) {
	if k, id, found := strings.Cut(key, ":"); found && (k == "user" || k == "ip") {
		return k, id, true
	}
	return "", "", false
}

// ListLoginLockouts returns every tracked identity that currently has at
// least one recorded failure (locked or not — a near-lockout is useful to
// see coming), newest attempt first.
func ListLoginLockouts() []LoginLockoutEntry {
	loginThrottleMu.Lock()
	defer loginThrottleMu.Unlock()

	now := time.Now()
	out := make([]LoginLockoutEntry, 0, len(loginThrottle))
	for key, s := range loginThrottle {
		kind, id, ok := splitLoginKey(key)
		if !ok || s.failures == 0 {
			continue
		}
		locked := !s.lockedUntil.IsZero() && now.Before(s.lockedUntil)
		remaining := 0
		if locked {
			remaining = int(time.Until(s.lockedUntil).Round(time.Second) / time.Second)
		}
		out = append(out, LoginLockoutEntry{
			Kind:          kind,
			Identifier:    id,
			Failures:      s.failures,
			Locked:        locked,
			RemainingSec:  remaining,
			LastAttemptAt: s.lastAttempt.Format(time.RFC3339),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAttemptAt > out[j].LastAttemptAt })
	return out
}

// UnlockLogin clears the throttle state for kind ("user" or "ip") +
// identifier, so the next login attempt is treated as fresh — used to let
// an admin reopen an account/IP before its lockout timer would naturally
// expire. Reports whether an entry existed to clear.
func UnlockLogin(kind, identifier string) bool {
	var key string
	switch kind {
	case "user":
		key = "user:" + strings.ToLower(identifier)
	case "ip":
		key = "ip:" + identifier
	default:
		return false
	}
	loginThrottleMu.Lock()
	_, existed := loginThrottle[key]
	delete(loginThrottle, key)
	loginThrottleMu.Unlock()
	return existed
}
