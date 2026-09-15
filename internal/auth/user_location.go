package auth

// User location capture: when a user's browser grants geolocation
// permission after logging in, app.js's maybeCaptureLoginLocation() posts
// one GPS fix here — tied to that login (not every page visit), best-effort
// and entirely opt-in via the browser's own permission prompt; nothing is
// ever requested or recorded without it. Admins can review the resulting
// history on the Log page's "Vị trí người dùng" tab, filterable by user and
// date range — same daily-JSONL pattern as login_history.go and
// internal/accesslog.

import (
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// UserLocationEntry is one recorded GPS fix.
type UserLocationEntry struct {
	Time     string  `json:"time"` // RFC3339, Asia/Ho_Chi_Minh
	Username string  `json:"username"`
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	Accuracy float64 `json:"accuracy,omitempty"` // meters, 0 = unknown
}

var (
	userLocMu  sync.Mutex
	userLocDir string
)

var userLocFileRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.jsonl$`)

func userLocDayFile(t time.Time) string {
	return filepath.Join(userLocDir, t.In(vnLocation).Format("2006-01-02")+".jsonl")
}

func initUserLocations(dir string) {
	userLocDir = dir
	_ = os.MkdirAll(dir, 0755)
}

// recordUserLocation appends one GPS fix. Best-effort: failures are
// silently dropped rather than disrupting the request that triggered it.
func recordUserLocation(username string, lat, lon, accuracy float64) {
	if userLocDir == "" || username == "" {
		return
	}
	now := time.Now()
	data, err := json.Marshal(UserLocationEntry{
		Time:     now.In(vnLocation).Format(time.RFC3339),
		Username: username,
		Lat:      lat,
		Lon:      lon,
		Accuracy: accuracy,
	})
	if err != nil {
		return
	}
	data = append(data, '\n')

	userLocMu.Lock()
	defer userLocMu.Unlock()
	f, err := os.OpenFile(userLocDayFile(now), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(data)
}

// queryUserLocations returns entries whose calendar day (Asia/Ho_Chi_Minh)
// falls within [from, to] inclusive (either bound may be empty for an open
// range), optionally filtered to one username (case-insensitive exact match).
func queryUserLocations(from, to, user string) ([]UserLocationEntry, error) {
	list, err := os.ReadDir(userLocDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []UserLocationEntry{}, nil
		}
		return nil, err
	}

	var names []string
	for _, e := range list {
		name := e.Name()
		if e.IsDir() || !userLocFileRe.MatchString(name) {
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

	out := []UserLocationEntry{}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(userLocDir, name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var e UserLocationEntry
			if json.Unmarshal([]byte(line), &e) != nil {
				continue
			}
			if user != "" && !strings.EqualFold(e.Username, user) {
				continue
			}
			out = append(out, e)
		}
	}
	return out, nil
}

// ── Throttling ────────────────────────────────────────────────────────
// At most one accepted fix per user within this window, so a misbehaving
// authenticated client can't flood the log by calling the endpoint in a
// loop — the browser only ever calls it once per login anyway.
const userLocationThrottle = 20 * time.Second

var (
	userLocThrottleMu sync.Mutex
	userLocLastPost   = map[string]time.Time{}
)

func registerUserLocationHandler() {
	http.HandleFunc("/api/user-location", userLocationHandler)
}

// userLocationHandler:
//
//	POST /api/user-location  {"lat":..,"lon":..,"accuracy":..}  — any
//	     authenticated user, records their OWN location (username comes
//	     from the auth token, never from the request body).
//	GET  /api/user-location?from=&to=&user=  — admin only.
func userLocationHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		user, ok := UserFromContext(r.Context())
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			Lat      float64 `json:"lat"`
			Lon      float64 `json:"lon"`
			Accuracy float64 `json:"accuracy"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if math.IsNaN(req.Lat) || math.IsNaN(req.Lon) ||
			math.IsInf(req.Lat, 0) || math.IsInf(req.Lon, 0) ||
			req.Lat < -90 || req.Lat > 90 || req.Lon < -180 || req.Lon > 180 {
			http.Error(w, "invalid coordinates", http.StatusBadRequest)
			return
		}

		userLocThrottleMu.Lock()
		last, seen := userLocLastPost[user.Username]
		if seen && time.Since(last) < userLocationThrottle {
			userLocThrottleMu.Unlock()
			w.WriteHeader(http.StatusNoContent) // accepted-but-ignored, client doesn't need to know
			return
		}
		userLocLastPost[user.Username] = time.Now()
		userLocThrottleMu.Unlock()

		recordUserLocation(user.Username, req.Lat, req.Lon, req.Accuracy)
		w.WriteHeader(http.StatusNoContent)

	case http.MethodGet:
		user, ok := UserFromContext(r.Context())
		if !ok || user.Role != RoleAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		q := r.URL.Query()
		entries, err := queryUserLocations(q.Get("from"), q.Get("to"), q.Get("user"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		responseJSON(w, entries)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
