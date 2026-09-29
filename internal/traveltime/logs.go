package traveltime

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// LogEntry records one travel-time measurement for a route.
type LogEntry struct {
	Timestamp   string  `json:"timestamp"`
	RouteID     string  `json:"routeId"`
	RouteName   string  `json:"routeName"`
	Origin      string  `json:"origin"`
	Destination string  `json:"destination"`
	Waypoints   string  `json:"waypoints,omitempty"`
	LengthM     int     `json:"lengthM"`
	DurationSec int     `json:"durationSec"`
	BaseSec     int     `json:"baseSec"`
	DelaySec    int     `json:"delaySec"`
	TTI         float64 `json:"tti"`
}

var (
	logsMu  sync.Mutex
	logsDir string

	// todayCache mirrors today's log file in memory so the dashboard summary
	// API (polled every 60s, see www/dashboard.html) doesn't re-read and
	// re-JSON-parse the whole day's file on every single call — with many
	// routes on a short collection interval that file grows all day, so the
	// same query gets slower and slower as the day goes on, with nothing
	// ever erroring (a slow disk scan isn't a failure) to explain why.
	// appendLogs keeps this in sync as entries are written; getLogs serves
	// "today" straight from it. Historical dates still hit disk — they're
	// only requested on demand, not polled.
	cacheMu   sync.RWMutex
	cacheDate string
	cacheData []LogEntry
)

var logFileRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.jsonl$`)

func initLogs(dir string) {
	logsDir = dir
	_ = os.MkdirAll(dir, 0755)
}

func loc() *time.Location {
	l, _ := time.LoadLocation("Asia/Ho_Chi_Minh")
	if l == nil {
		// Fallback: Vietnam is UTC+7 (no tzdata on this OS)
		return time.FixedZone("ICT", 7*3600)
	}
	return l
}

func todayFile() string {
	return filepath.Join(logsDir, time.Now().In(loc()).Format("2006-01-02")+".jsonl")
}

func dayFile(date string) string {
	return filepath.Join(logsDir, date+".jsonl")
}

// appendLogs appends entries to today's daily JSONL log file.
func appendLogs(entries []LogEntry) error {
	logsMu.Lock()
	defer logsMu.Unlock()
	path := todayFile()
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}

	today := time.Now().In(loc()).Format("2006-01-02")
	cacheMu.Lock()
	if cacheDate == today {
		cacheData = append(cacheData, entries...)
	} else {
		// Stale/unseeded cache (day rollover, or this is the first append
		// since process start): reload from disk rather than assuming
		// today's file was empty before this write — it may already hold
		// entries from earlier today. path was just written to above and
		// we're still holding logsMu, so this reread is consistent.
		loaded, _ := readLogFile(path)
		cacheDate = today
		cacheData = loaded
	}
	cacheMu.Unlock()

	return nil
}

// readLogFile scans and JSON-parses one JSONL log file from disk. Callers
// must hold logsMu.
func readLogFile(path string) ([]LogEntry, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return []LogEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []LogEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e LogEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	if scanner.Err() != nil {
		return nil, scanner.Err()
	}
	return entries, nil
}

// getLogs returns log entries for the given date (format "2006-01-02").
// If date is empty, today's entries are returned.
// limit ≤ 0 returns all entries.
func getLogs(date string, limit int) ([]LogEntry, error) {
	today := time.Now().In(loc()).Format("2006-01-02")

	if date == "" || date == today {
		entries, err := getTodayFromCache(today)
		if err != nil {
			return nil, err
		}
		return applyLimit(entries, limit), nil
	}

	// Validate to prevent path traversal.
	if !logFileRe.MatchString(date + ".jsonl") {
		return []LogEntry{}, nil
	}

	logsMu.Lock()
	defer logsMu.Unlock()
	entries, err := readLogFile(dayFile(date))
	if err != nil {
		return nil, err
	}
	return applyLimit(entries, limit), nil
}

// getTodayFromCache serves today's entries from the in-memory cache that
// appendLogs keeps current, seeding it from disk once if this is the first
// call since startup or since the date rolled over. Always returns a fresh
// copy — the cache slice itself must never escape this function, since
// appendLogs mutates it concurrently.
func getTodayFromCache(today string) ([]LogEntry, error) {
	cacheMu.RLock()
	valid := cacheDate == today
	var data []LogEntry
	if valid {
		data = cacheData
	}
	cacheMu.RUnlock()

	if !valid {
		logsMu.Lock()
		loaded, err := readLogFile(todayFile())
		logsMu.Unlock()
		if err != nil {
			return nil, err
		}

		cacheMu.Lock()
		// Re-check: another goroutine may have seeded (or appended to) the
		// cache while we were reading the file without holding cacheMu.
		if cacheDate != today {
			cacheDate = today
			cacheData = loaded
		}
		data = cacheData
		cacheMu.Unlock()
	}

	out := make([]LogEntry, len(data))
	copy(out, data)
	return out, nil
}

func applyLimit(entries []LogEntry, limit int) []LogEntry {
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	if entries == nil {
		entries = []LogEntry{}
	}
	return entries
}

// listLogDates returns available log dates (newest first).
func listLogDates() ([]string, error) {
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	var dates []string
	for _, e := range entries {
		if !e.IsDir() && logFileRe.MatchString(e.Name()) {
			dates = append(dates, strings.TrimSuffix(e.Name(), ".jsonl"))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dates)))
	return dates, nil
}
