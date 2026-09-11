package auth

// Lightweight anomaly detection over login and access-control events.
// Every check here is intentionally simple (no ML, no external service) —
// each one flags one concrete, explainable signal an admin can act on:
//
//   - login_lockout:        brute-force lockout just triggered (username or IP)
//   - new_ip:               a user logged in successfully from an IP never
//                            seen for that account before
//   - concurrent_ip:        a user logged in from a different IP shortly
//                           after their previous login — possible shared or
//                           compromised credentials
//   - off_hours_login:      a login succeeded outside the configured normal
//                           hours (disabled by default; admin opts in via
//                           Settings)
//   - unauthorized_access:  an authenticated user was denied a resource
//                           (camera stream or page/tab) they don't have
//                           permission for — throttled per (user, resource)
//                           so a single blocked feature doesn't spam alerts
//
// Alerts are stored as one JSONL file per calendar day (same pattern as
// login_history.go / internal/accesslog), queryable by an admin-only API and
// shown on the Log page's "Cảnh báo bảo mật" tab.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	AlertSeverityLow    = "low"
	AlertSeverityMedium = "medium"
	AlertSeverityHigh   = "high"
)

// SecurityAlert is one recorded anomaly.
type SecurityAlert struct {
	Time     string `json:"time"` // RFC3339, Asia/Ho_Chi_Minh
	Type     string `json:"type"`
	Severity string `json:"severity"`
	Username string `json:"username,omitempty"`
	IP       string `json:"ip,omitempty"`
	Message  string `json:"message"`
}

var (
	alertsMu  sync.Mutex
	alertsDir string
)

var alertsFileRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.jsonl$`)

func alertsDayFile(t time.Time) string {
	return filepath.Join(alertsDir, t.In(vnLocation).Format("2006-01-02")+".jsonl")
}

func initSecurityAlerts(dir, knownIPsPath string) {
	alertsDir = dir
	_ = os.MkdirAll(dir, 0755)
	loadKnownIPs(knownIPsPath)
}

// raiseAlert appends one alert. Best-effort: failures are silently dropped
// rather than disrupting the request that triggered the check.
func raiseAlert(alertType, severity, username, ip, message string) {
	if alertsDir == "" {
		return
	}
	now := time.Now()
	data, err := json.Marshal(SecurityAlert{
		Time:     now.In(vnLocation).Format(time.RFC3339),
		Type:     alertType,
		Severity: severity,
		Username: username,
		IP:       ip,
		Message:  message,
	})
	if err != nil {
		return
	}
	data = append(data, '\n')

	alertsMu.Lock()
	defer alertsMu.Unlock()
	f, err := os.OpenFile(alertsDayFile(now), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(data)
}

func queryAlerts(from, to string) ([]SecurityAlert, error) {
	list, err := os.ReadDir(alertsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []SecurityAlert{}, nil
		}
		return nil, err
	}

	var names []string
	for _, e := range list {
		name := e.Name()
		if e.IsDir() || !alertsFileRe.MatchString(name) {
			continue
		}
		day := strings.TrimSuffix(name, ".jsonl")
		if from != "" && day < from {
			continue
		}
		if to != "" && day > to {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	out := []SecurityAlert{}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(alertsDir, name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var a SecurityAlert
			if json.Unmarshal([]byte(line), &a) == nil {
				out = append(out, a)
			}
		}
	}
	return out, nil
}

func registerSecurityAlertsHandler() {
	http.HandleFunc("/api/security-alerts", securityAlertsHandler)
}

// securityAlertsHandler GET /api/security-alerts?from=YYYY-MM-DD&to=YYYY-MM-DD  (admin only)
func securityAlertsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, ok := UserFromContext(r.Context())
	if !ok || user.Role != RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	entries, err := queryAlerts(q.Get("from"), q.Get("to"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	responseJSON(w, entries)
}

// ── Known IPs per user (persisted) ──────────────────────────────────────

const knownIPsMaxPerUser = 10

var (
	knownIPsMu   sync.Mutex
	knownIPs     = map[string][]string{}
	knownIPsPath string
)

func loadKnownIPs(path string) {
	knownIPsPath = path
	data, err := os.ReadFile(path)
	if err != nil {
		return // start empty; file created on first save
	}
	_ = json.Unmarshal(data, &knownIPs)
}

func saveKnownIPsLocked() {
	if knownIPsPath == "" {
		return
	}
	data, err := json.MarshalIndent(knownIPs, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(knownIPsPath, data, 0644)
}

// rememberIP reports whether ip was already known for username, and records
// it either way (moving it to "known" for next time).
func rememberIP(username, ip string) (wasKnown bool) {
	if username == "" || ip == "" {
		return true // nothing meaningful to flag
	}
	knownIPsMu.Lock()
	defer knownIPsMu.Unlock()
	ips := knownIPs[username]
	for _, existing := range ips {
		if existing == ip {
			return true
		}
	}
	ips = append(ips, ip)
	if len(ips) > knownIPsMaxPerUser {
		ips = ips[len(ips)-knownIPsMaxPerUser:]
	}
	knownIPs[username] = ips
	saveKnownIPsLocked()
	return false
}

// ── Concurrent-IP tracking (in-memory, short window) ────────────────────

const concurrentIPWindow = 10 * time.Minute

type lastLogin struct {
	IP string
	At time.Time
}

var (
	lastLoginMu sync.Mutex
	lastLogins  = map[string]lastLogin{}
)

// checkConcurrentLogin reports the previous login IP if username logged in
// from a *different* IP within concurrentIPWindow, then records this login
// as the new "last login" for next time.
func checkConcurrentLogin(username, ip string) (prevIP string, flagged bool) {
	lastLoginMu.Lock()
	defer lastLoginMu.Unlock()
	prev, ok := lastLogins[username]
	lastLogins[username] = lastLogin{IP: ip, At: time.Now()}
	if ok && prev.IP != ip && time.Since(prev.At) <= concurrentIPWindow {
		return prev.IP, true
	}
	return "", false
}

// ── Off-hours login check ───────────────────────────────────────────────

// isOffHoursLogin reports whether t (evaluated in Vietnam time) falls
// outside the admin-configured normal login window. Disabled unless the
// admin has opted in via Settings ("login_hour_range_enabled").
func isOffHoursLogin(t time.Time) bool {
	s := GetSettings()
	if !s.LoginHourRangeEnabled {
		return false
	}
	start, end := s.LoginHourStart, s.LoginHourEnd
	if start < 0 || start > 23 || end < 0 || end > 23 || start == end {
		return false // misconfigured — don't false-positive on it
	}
	hour := t.In(vnLocation).Hour()
	if start < end {
		return hour < start || hour >= end
	}
	// Wraps past midnight, e.g. start=22 end=6 → normal window is 22:00–05:59
	return hour >= end && hour < start
}

// ── Entry points, called from api.go / middleware.go ────────────────────

// onLoginLockout is called the moment a brute-force lockout is (re-)armed
// for kind ("user" or "ip") + identifier — never on every blocked attempt
// while it's already locked (recordLoginFailure only returns justLocked
// once per lockout episode), so this can't spam.
func onLoginLockout(kind, identifier string) {
	msg := "Tài khoản \"" + identifier + "\" bị tạm khóa do đăng nhập sai nhiều lần liên tiếp."
	username := ""
	ip := ""
	if kind == "ip" {
		ip = identifier
		msg = "Địa chỉ IP " + identifier + " bị tạm khóa do đăng nhập sai nhiều lần liên tiếp."
	} else {
		username = identifier
	}
	raiseAlert("login_lockout", AlertSeverityHigh, username, ip, msg)
}

// onLoginSuccess runs the post-authentication anomaly checks: new IP,
// concurrent IP, off-hours. Called once per successful login.
func onLoginSuccess(username, ip, userAgent string) {
	now := time.Now()

	if isOffHoursLogin(now) {
		raiseAlert("off_hours_login", AlertSeverityMedium, username, ip,
			"User \""+username+"\" đăng nhập ngoài khung giờ bình thường (lúc "+now.In(vnLocation).Format("15:04 02/01")+").")
	}

	if wasKnown := rememberIP(username, ip); !wasKnown {
		raiseAlert("new_ip", AlertSeverityMedium, username, ip,
			"User \""+username+"\" đăng nhập từ địa chỉ IP mới ("+ip+").")
	}

	if prevIP, flagged := checkConcurrentLogin(username, ip); flagged {
		raiseAlert("concurrent_ip", AlertSeverityHigh, username, ip,
			"User \""+username+"\" đăng nhập từ IP "+ip+" chỉ ít phút sau lần đăng nhập trước từ IP "+prevIP+" — có thể tài khoản bị dùng chung hoặc bị lộ mật khẩu.")
	}
}

// ── Unauthorized-access throttling ───────────────────────────────────────

const unauthorizedAccessThrottle = 5 * time.Minute

var (
	unauthorizedMu   sync.Mutex
	unauthorizedSeen = map[string]time.Time{}
)

// onUnauthorizedAccess is called whenever an authenticated, non-admin user
// is denied a resource (camera stream or page/tab) they lack permission
// for. Throttled per (username, resource) so one blocked UI feature
// polling in a loop doesn't flood the alert log.
func onUnauthorizedAccess(username, ip, resource string) {
	key := username + "|" + resource
	now := time.Now()

	unauthorizedMu.Lock()
	last, seen := unauthorizedSeen[key]
	if seen && now.Sub(last) < unauthorizedAccessThrottle {
		unauthorizedMu.Unlock()
		return
	}
	unauthorizedSeen[key] = now
	unauthorizedMu.Unlock()

	raiseAlert("unauthorized_access", AlertSeverityHigh, username, ip,
		"User \""+username+"\" cố truy cập tài nguyên không được cấp quyền: "+resource+".")
}
