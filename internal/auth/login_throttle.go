package auth

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Brute-force protection for POST /api/auth/login. Tracked independently by
// username and by client IP, so an attacker can't dodge the per-username
// lockout by spraying many usernames from one IP, nor dodge a per-IP limit
// by distributing guesses across many IPs against one account.
const (
	maxLoginFailures   = 5
	loginLockoutWindow = 5 * time.Minute
	loginStateMaxAge   = time.Hour // stale entries swept after this long unattempted
)

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

func recordLoginFailure(key string) {
	loginThrottleMu.Lock()
	defer loginThrottleMu.Unlock()
	s := loginThrottle[key]
	if s == nil {
		s = &loginAttemptState{}
		loginThrottle[key] = s
	}
	s.failures++
	s.lastAttempt = time.Now()
	if s.failures >= maxLoginFailures {
		s.lockedUntil = time.Now().Add(loginLockoutWindow)
	}
}

func recordLoginSuccess(key string) {
	loginThrottleMu.Lock()
	defer loginThrottleMu.Unlock()
	delete(loginThrottle, key)
}
