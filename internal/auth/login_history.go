package auth

// Login history: every login attempt (success or failure) is recorded with
// its username, client IP, user-agent and Vietnam wall-clock time, so an
// admin can review who logged in when and from where — the raw data behind
// both the "Lịch sử đăng nhập" log page and the anomaly checks in
// security_alerts.go.

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

// LoginHistoryEntry is one recorded login attempt.
type LoginHistoryEntry struct {
	Time      string `json:"time"`     // RFC3339, Asia/Ho_Chi_Minh
	Username  string `json:"username"` // as typed — may not exist for failed attempts
	IP        string `json:"ip"`
	UserAgent string `json:"user_agent"`
	Success   bool   `json:"success"`
	Reason    string `json:"reason,omitempty"` // "invalid_credentials" / "locked" — empty on success
}

var (
	loginHistoryMu  sync.Mutex
	loginHistoryDir string
)

// vnLocation anchors daily log files (and their entries' timestamps) to
// Vietnam wall-clock time regardless of the server process's own timezone —
// same pattern as internal/accesslog, internal/incidents/timeslots.go and
// internal/traffic/storage.go. Shared by login_history.go and
// security_alerts.go.
var vnLocation = loadVNLocation()

func loadVNLocation() *time.Location {
	if loc, err := time.LoadLocation("Asia/Ho_Chi_Minh"); err == nil {
		return loc
	}
	return time.FixedZone("ICT", 7*3600)
}

var loginHistoryFileRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.jsonl$`)

func loginHistoryDayFile(t time.Time) string {
	return filepath.Join(loginHistoryDir, t.In(vnLocation).Format("2006-01-02")+".jsonl")
}

func initLoginHistory(dir string) {
	loginHistoryDir = dir
	_ = os.MkdirAll(dir, 0755)
}

// recordLoginHistory appends one login-attempt entry. Best-effort: failures
// are silently dropped rather than disrupting the login request itself.
func recordLoginHistory(username, ip, userAgent string, success bool, reason string) {
	if loginHistoryDir == "" {
		return
	}
	now := time.Now()
	data, err := json.Marshal(LoginHistoryEntry{
		Time:      now.In(vnLocation).Format(time.RFC3339),
		Username:  username,
		IP:        ip,
		UserAgent: userAgent,
		Success:   success,
		Reason:    reason,
	})
	if err != nil {
		return
	}
	data = append(data, '\n')

	loginHistoryMu.Lock()
	defer loginHistoryMu.Unlock()
	f, err := os.OpenFile(loginHistoryDayFile(now), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(data)
}

// queryLoginHistory returns entries whose calendar day (Asia/Ho_Chi_Minh)
// falls within [from, to] inclusive — either bound may be empty for an open
// range. Both use "2006-01-02" format.
func queryLoginHistory(from, to string) ([]LoginHistoryEntry, error) {
	list, err := os.ReadDir(loginHistoryDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []LoginHistoryEntry{}, nil
		}
		return nil, err
	}

	var names []string
	for _, e := range list {
		name := e.Name()
		if e.IsDir() || !loginHistoryFileRe.MatchString(name) {
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

	out := []LoginHistoryEntry{}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(loginHistoryDir, name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var e LoginHistoryEntry
			if json.Unmarshal([]byte(line), &e) == nil {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

func registerLoginHistoryHandler() {
	http.HandleFunc("/api/login-history", loginHistoryHandler)
}

// loginHistoryHandler GET /api/login-history?from=YYYY-MM-DD&to=YYYY-MM-DD  (admin only)
func loginHistoryHandler(w http.ResponseWriter, r *http.Request) {
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
	entries, err := queryLoginHistory(q.Get("from"), q.Get("to"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	responseJSON(w, entries)
}
