// Package accesslog records which user viewed which camera stream (and by
// what protocol), for the admin-only "Log" page's camera access history.
// This is separate from go2rtc's own in-memory application log (internal/api
// api/log) — that's server diagnostics, this is a per-view audit trail.
package accesslog

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

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/auth"
)

// Entry is one recorded camera view.
type Entry struct {
	Time   string `json:"time"`   // RFC3339, Asia/Ho_Chi_Minh
	User   string `json:"user"`   // username
	Stream string `json:"stream"` // real stream name (never the masked ID)
	Kind   string `json:"kind"`   // mjpeg, mjpeg-ws, ascii, y4m, mse, mp4, mp4-ws, hls, hls-ws, webrtc, webrtc-http
}

var (
	mu  sync.Mutex
	dir string
)

// A single "view" of a camera in the browser player often triggers more
// than one protocol negotiation in quick succession (e.g. webrtc then a
// fallback to mse), which would otherwise show up as several near-identical
// rows for the same user+camera a few milliseconds apart. coalesceWindow
// batches Record calls for the same (user, stream) pair arriving within it
// into a single log line whose Kind lists every protocol that was tried,
// comma-separated (e.g. "mse,webrtc").
const coalesceWindow = 4 * time.Second

type pendingEntry struct {
	time  time.Time // of the first Record call in this batch
	kinds []string  // insertion order, deduplicated
	seen  map[string]bool
}

var pending = map[string]*pendingEntry{}

// vnLocation anchors the daily file's calendar day (and each entry's
// timestamp) to Vietnam wall-clock time regardless of the server process's
// own timezone — same pattern as internal/incidents/timeslots.go and
// internal/traffic/storage.go.
var vnLocation = loadVNLocation()

func loadVNLocation() *time.Location {
	if loc, err := time.LoadLocation("Asia/Ho_Chi_Minh"); err == nil {
		return loc
	}
	return time.FixedZone("ICT", 7*3600)
}

var fileRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.jsonl$`)

func dayFile(t time.Time) string {
	return filepath.Join(dir, t.In(vnLocation).Format("2006-01-02")+".jsonl")
}

func Init() {
	dir = filepath.Join(filepath.Dir(app.ConfigPath), "access_log")
	_ = os.MkdirAll(dir, 0755)

	http.HandleFunc("/api/access-log", handleQuery)
}

// Record schedules one access-log entry for (user, stream, kind). If another
// call for the same user+stream arrives within coalesceWindow, its kind is
// merged into the same pending entry instead of writing a second line — see
// coalesceWindow's doc comment. Best-effort: failures are silently dropped
// rather than disrupting the stream request that triggered it.
func Record(user, stream, kind string) {
	if user == "" || dir == "" {
		return
	}
	key := user + "|" + stream

	mu.Lock()
	if p, ok := pending[key]; ok {
		if !p.seen[kind] {
			p.seen[kind] = true
			p.kinds = append(p.kinds, kind)
		}
		mu.Unlock()
		return
	}
	p := &pendingEntry{
		time:  time.Now(),
		kinds: []string{kind},
		seen:  map[string]bool{kind: true},
	}
	pending[key] = p
	mu.Unlock()

	time.AfterFunc(coalesceWindow, func() { flush(key) })
}

// flush writes key's pending entry to disk and removes it from the map.
func flush(key string) {
	mu.Lock()
	p, ok := pending[key]
	if ok {
		delete(pending, key)
	}
	mu.Unlock()
	if !ok {
		return
	}

	parts := strings.SplitN(key, "|", 2)
	user, stream := parts[0], ""
	if len(parts) == 2 {
		stream = parts[1]
	}

	data, err := json.Marshal(Entry{
		Time:   p.time.In(vnLocation).Format(time.RFC3339),
		User:   user,
		Stream: stream,
		Kind:   strings.Join(p.kinds, ","),
	})
	if err != nil {
		return
	}
	data = append(data, '\n')

	mu.Lock()
	defer mu.Unlock()
	f, err := os.OpenFile(dayFile(p.time), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(data)
}

// Query returns entries whose calendar day (Asia/Ho_Chi_Minh) falls within
// [from, to] inclusive — either bound may be empty for an open range.
// Both use "2006-01-02" format.
func Query(from, to string) ([]Entry, error) {
	list, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Entry{}, nil
		}
		return nil, err
	}

	var names []string
	for _, e := range list {
		name := e.Name()
		if e.IsDir() || !fileRe.MatchString(name) {
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

	out := []Entry{}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var e Entry
			if json.Unmarshal([]byte(line), &e) == nil {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

// handleQuery GET /api/access-log?from=YYYY-MM-DD&to=YYYY-MM-DD  (admin only)
func handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user.Role != auth.RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	entries, err := Query(q.Get("from"), q.Get("to"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entries)
}
