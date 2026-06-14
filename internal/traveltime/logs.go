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
	return nil
}

// getLogs returns log entries for the given date (format "2006-01-02").
// If date is empty, today's entries are returned.
// limit ≤ 0 returns all entries.
func getLogs(date string, limit int) ([]LogEntry, error) {
	var path string
	if date == "" {
		path = todayFile()
	} else {
		// Validate to prevent path traversal.
		if !logFileRe.MatchString(date + ".jsonl") {
			return []LogEntry{}, nil
		}
		path = dayFile(date)
	}

	logsMu.Lock()
	defer logsMu.Unlock()

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

	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	if entries == nil {
		entries = []LogEntry{}
	}
	return entries, nil
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
