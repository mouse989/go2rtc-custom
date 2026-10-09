package auth

// integration_throttle.go — brute-force protection for inbound integration
// pushes (e.g. POST /api/aievent/omnia/v1/push), tracked by client IP only —
// unlike login_throttle.go there's no "username" concept for a machine
// caller, and these are separate concerns kept in separate state rather than
// reusing loginThrottle's map (which is keyed/labeled for the login flow's
// "user:"/"ip:" admin lockout view).

import (
	"net/http"
	"sync"
	"time"
)

const (
	maxIntegrationAuthFailures = 10
	integrationLockoutWindow   = 15 * time.Minute
	integrationThrottleMaxAge  = 2 * time.Hour
)

type integrationAttemptState struct {
	failures    int
	lockedUntil time.Time
	lastAttempt time.Time
}

var (
	integrationThrottleMu sync.Mutex
	integrationThrottle   = map[string]*integrationAttemptState{} // by IP
	integrationSweepOnce  sync.Once
)

func startIntegrationThrottleSweeper() {
	integrationSweepOnce.Do(func() {
		go func() {
			for range time.Tick(15 * time.Minute) {
				now := time.Now()
				integrationThrottleMu.Lock()
				for k, s := range integrationThrottle {
					if now.Sub(s.lastAttempt) > integrationThrottleMaxAge {
						delete(integrationThrottle, k)
					}
				}
				integrationThrottleMu.Unlock()
			}
		}()
	})
}

// integrationAuthLocked reports whether ip is currently locked out of
// integration auth, and for how much longer.
func integrationAuthLocked(ip string) (locked bool, remaining time.Duration) {
	integrationThrottleMu.Lock()
	defer integrationThrottleMu.Unlock()
	s := integrationThrottle[ip]
	if s == nil || s.lockedUntil.IsZero() || !time.Now().Before(s.lockedUntil) {
		return false, 0
	}
	return true, time.Until(s.lockedUntil)
}

// recordIntegrationAuthFailure reports justLocked = true exactly once per
// lockout episode (same convention as recordLoginFailure) — callers use
// this to raise a security alert without spamming it on every blocked
// attempt during the lockout window itself.
func recordIntegrationAuthFailure(ip string) (justLocked bool) {
	integrationThrottleMu.Lock()
	defer integrationThrottleMu.Unlock()
	s := integrationThrottle[ip]
	if s == nil {
		s = &integrationAttemptState{}
		integrationThrottle[ip] = s
	}
	s.failures++
	s.lastAttempt = time.Now()
	if s.failures >= maxIntegrationAuthFailures {
		s.lockedUntil = time.Now().Add(integrationLockoutWindow)
		return true
	}
	return false
}

func recordIntegrationAuthSuccess(ip string) {
	integrationThrottleMu.Lock()
	defer integrationThrottleMu.Unlock()
	delete(integrationThrottle, ip)
}

// ── Exported wrappers ──────────────────────────────────────────────────
// internal/aievent's push handler lives in a different package and needs
// these three plus the security-alert hook below; everything else here
// stays package-private.

// IntegrationAuthLocked is integrationAuthLocked's exported form.
func IntegrationAuthLocked(ip string) (locked bool, remaining time.Duration) {
	return integrationAuthLocked(ip)
}

// RecordIntegrationAuthFailure is recordIntegrationAuthFailure's exported
// form.
func RecordIntegrationAuthFailure(ip string) (justLocked bool) {
	return recordIntegrationAuthFailure(ip)
}

// RecordIntegrationAuthSuccess is recordIntegrationAuthSuccess's exported
// form.
func RecordIntegrationAuthSuccess(ip string) {
	recordIntegrationAuthSuccess(ip)
}

// RaiseIntegrationAuthFailedAlert records a security alert (visible on the
// Log page's "Cảnh báo bảo mật" tab, same store as login/access anomalies —
// see security_alerts.go) for an IP that just got locked out of integration
// auth — called once per lockout episode, not on every blocked attempt
// during the lockout window (mirrors onLoginLockout's own convention).
func RaiseIntegrationAuthFailedAlert(ip string) {
	raiseAlert("integration_auth_failed", AlertSeverityMedium, "", ip,
		"Repeated invalid API key attempts on an external-integration push endpoint from "+ip)
}

// ClientIP is clientIP's exported form, for internal/aievent's push handler.
func ClientIP(r *http.Request) string { return clientIP(r) }
