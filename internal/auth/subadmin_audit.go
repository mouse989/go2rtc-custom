package auth

// subadmin_audit.go — a durable, admin-only audit trail of every write a
// sub admin makes through /api/subadmin/* (see api_subadmin.go). Delegating
// enable/disable, password-reset and camera-grant capability to a non-admin
// account was only acceptable on the condition that the top admin can
// always see exactly what was done with it — this is that record. Same
// day-bucketed JSONL storage pattern as login_history.go: small, append-
// only, no database needed, and trivially greppable on disk if ever needed
// outside the UI.

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

// SubAdminAuditEntry is one recorded sub-admin action.
type SubAdminAuditEntry struct {
	Time       string `json:"time"` // RFC3339, Asia/Ho_Chi_Minh
	SubAdmin   string `json:"sub_admin"`
	Action     string `json:"action"` // "toggle_enabled" | "reset_password" | "grant_cameras"
	TargetUser string `json:"target_user"`
	// Detail is a human-readable summary of what changed — NEVER the
	// plaintext password itself, even for "reset_password" (see
	// api_subadmin.go's resetPassword handler, which only ever passes
	// something like "force_change=true" here).
	Detail string `json:"detail"`
	IP     string `json:"ip"`
}

var (
	subAdminAuditMu  sync.Mutex
	subAdminAuditDir string
)

var subAdminAuditFileRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.jsonl$`)

func subAdminAuditDayFile(t time.Time) string {
	return filepath.Join(subAdminAuditDir, t.In(vnLocation).Format("2006-01-02")+".jsonl")
}

func initSubAdminAudit(dir string) {
	subAdminAuditDir = dir
	_ = os.MkdirAll(dir, 0755)
}

// recordSubAdminAction appends one audit entry. Best-effort: a failure here
// must never block or roll back the action it's recording — same convention
// as recordLoginHistory and trafficflow's logPushAttempt.
func recordSubAdminAction(subAdmin, action, targetUser, detail, ip string) {
	if subAdminAuditDir == "" {
		return
	}
	now := time.Now()
	data, err := json.Marshal(SubAdminAuditEntry{
		Time:       now.In(vnLocation).Format(time.RFC3339),
		SubAdmin:   subAdmin,
		Action:     action,
		TargetUser: targetUser,
		Detail:     detail,
		IP:         ip,
	})
	if err != nil {
		return
	}
	data = append(data, '\n')

	subAdminAuditMu.Lock()
	defer subAdminAuditMu.Unlock()
	f, err := os.OpenFile(subAdminAuditDayFile(now), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(data)
}

// querySubAdminAudit returns entries whose calendar day (Asia/Ho_Chi_Minh)
// falls within [from, to] inclusive — either bound may be empty for an open
// range. Both use "2006-01-02" format. Newest first.
func querySubAdminAudit(from, to string) ([]SubAdminAuditEntry, error) {
	list, err := os.ReadDir(subAdminAuditDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []SubAdminAuditEntry{}, nil
		}
		return nil, err
	}

	var names []string
	for _, e := range list {
		name := e.Name()
		if e.IsDir() || !subAdminAuditFileRe.MatchString(name) {
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

	out := []SubAdminAuditEntry{}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(subAdminAuditDir, name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var e SubAdminAuditEntry
			if json.Unmarshal([]byte(line), &e) == nil {
				out = append(out, e)
			}
		}
	}
	// Newest first — the natural reading order for an audit review.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	return out, nil
}

func registerSubAdminAuditHandler() {
	http.HandleFunc("/api/subadmin-audit", subAdminAuditHandler)
}

// subAdminAuditHandler GET /api/subadmin-audit?from=YYYY-MM-DD&to=YYYY-MM-DD  (admin only)
func subAdminAuditHandler(w http.ResponseWriter, r *http.Request) {
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
	entries, err := querySubAdminAudit(q.Get("from"), q.Get("to"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	responseJSON(w, entries)
}
