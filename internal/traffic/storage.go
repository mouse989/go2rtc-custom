package traffic

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Daily history files: traffic_data/2026-06-10.json
// Each file holds every scan of that day, using the same point structure
// as the Google Sheets payload:
//
//	{"date":"2026-06-10","scans":[
//	  {"scannedAt":"...","raw":[{lat,lng,jamFactor,region,speed}],
//	   "filtered":[{...,label,area,members}],"persistent":[{...}]},
//	  ...
//	]}

var storageMu sync.Mutex // serialises read-modify-write on the daily file

// summaryCache holds each daily file's listScanFiles summary, keyed by
// filename, so GET /api/traffic/data doesn't re-read and re-JSON-parse every
// stored day on every call — with retention allowing up to 100 files and
// each holding a full day of scans (every scan_interval_min, complete with
// raw/filtered/persistent point geometry), that scan-and-parse was taking
// several seconds and got slower as more days accumulated, on a request
// path with no natural cap on how often it's polled. saveScanData updates
// this directly from the record it just wrote (no extra read); listScanFiles
// falls back to a one-time read+parse only for files not yet cached (e.g.
// history from before this process started) and remembers the result.
var (
	summaryMu    sync.Mutex
	summaryCache = map[string]scanFileInfo{}
)

func updateSummaryCache(name string, rec dailyRecord, size int64) {
	info := scanFileInfo{Name: name, Date: rec.Date, Size: size, Scans: len(rec.Scans)}
	for _, s := range rec.Scans {
		info.Raw += len(s.Raw)
		info.Filtered += len(s.Filtered)
		info.Persistent += len(s.Persistent)
	}
	summaryMu.Lock()
	summaryCache[name] = info
	summaryMu.Unlock()
}

var dailyFileRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.json$`)

// vnLocation anchors the daily file's calendar day to Vietnam wall-clock
// time, not whatever timezone the server process/OS happens to run in
// (often UTC on cloud hosts) — see the same pattern in
// internal/incidents/timeslots.go, which fixed the identical class of bug
// for incident time-slot classification.
var vnLocation = loadVNLocation()

func loadVNLocation() *time.Location {
	if loc, err := time.LoadLocation("Asia/Ho_Chi_Minh"); err == nil {
		return loc
	}
	// Fallback for minimal containers without IANA tzdata installed.
	return time.FixedZone("ICT", 7*3600)
}

// dataDir returns the directory where scan history files are stored
// (a "traffic_data" folder next to traffic.json).
func dataDir() string {
	return filepath.Join(filepath.Dir(configFile), "traffic_data")
}

// dailyRecord is the on-disk format of one day's history file.
type dailyRecord struct {
	Date  string          `json:"date"` // YYYY-MM-DD (local time)
	Scans []sheetsPayload `json:"scans"`
}

// saveScanData appends the scan result (Sheets payload format) to today's
// daily file and removes files older than the configured retention.
func saveScanData(c Config, scannedAt time.Time, raw, filtered, persistent []Point) error {
	storageMu.Lock()
	defer storageMu.Unlock()

	dir := dataDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	day := scannedAt.In(vnLocation).Format("2006-01-02")
	path := filepath.Join(dir, day+".json")

	// Load existing day file (if any) and append this scan
	rec := dailyRecord{Date: day}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &rec) // corrupted file → start fresh
		rec.Date = day
	}
	rec.Scans = append(rec.Scans, buildSheetsPayload(scannedAt, raw, filtered, persistent))

	data, err := json.MarshalIndent(rec, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}
	updateSummaryCache(day+".json", rec, int64(len(data)))

	// Retention cleanup (best-effort, by date in filename)
	if c.Storage.RetentionDays > 0 {
		cutoff := time.Now().In(vnLocation).AddDate(0, 0, -c.Storage.RetentionDays).Format("2006-01-02")
		entries, err := os.ReadDir(dir)
		if err == nil {
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() || !dailyFileRe.MatchString(name) {
					continue
				}
				if strings.TrimSuffix(name, ".json") < cutoff {
					_ = os.Remove(filepath.Join(dir, name))
				}
			}
		}
	}
	return nil
}

// scanFileInfo describes one stored daily file for the API listing.
type scanFileInfo struct {
	Name       string `json:"name"`
	Date       string `json:"date"`
	Size       int64  `json:"size"`
	Scans      int    `json:"scans"`      // number of scans recorded that day
	Raw        int    `json:"raw"`        // total raw points across the day
	Filtered   int    `json:"filtered"`   // total filtered points
	Persistent int    `json:"persistent"` // total persistent points
}

// listScanFiles returns stored daily files, newest first, up to limit.
func listScanFiles(limit int) ([]scanFileInfo, error) {
	entries, err := os.ReadDir(dataDir())
	if err != nil {
		if os.IsNotExist(err) {
			return []scanFileInfo{}, nil
		}
		return nil, err
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() && dailyFileRe.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	// Filenames are dates, so lexical order == chronological order
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if limit > 0 && len(names) > limit {
		names = names[:limit]
	}

	out := make([]scanFileInfo, 0, len(names))
	for _, name := range names {
		summaryMu.Lock()
		cached, ok := summaryCache[name]
		summaryMu.Unlock()
		if ok {
			out = append(out, cached)
			continue
		}

		// Cache miss — a day's file this process hasn't written to itself
		// (existing history from before it started, or before a restart).
		// Read and parse it once, then remember the result so it's not
		// paid again on the next call.
		path := filepath.Join(dataDir(), name)
		info := scanFileInfo{
			Name: name,
			Date: strings.TrimSuffix(name, ".json"),
		}
		if fi, err := os.Stat(path); err == nil {
			info.Size = fi.Size()
		}
		if data, err := os.ReadFile(path); err == nil {
			var rec dailyRecord
			if json.Unmarshal(data, &rec) == nil {
				info.Scans = len(rec.Scans)
				for _, s := range rec.Scans {
					info.Raw += len(s.Raw)
					info.Filtered += len(s.Filtered)
					info.Persistent += len(s.Persistent)
				}
			}
		}
		summaryMu.Lock()
		summaryCache[name] = info
		summaryMu.Unlock()
		out = append(out, info)
	}
	return out, nil
}

// HistoryStats returns the number of daily history files and their total
// size in bytes. Used by the system monitor. Safe to call before Init
// (returns zeros when the data dir doesn't exist yet).
func HistoryStats() (files int, bytes int64) {
	if configFile == "" {
		return 0, 0
	}
	entries, err := os.ReadDir(dataDir())
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if e.IsDir() || !dailyFileRe.MatchString(e.Name()) {
			continue
		}
		files++
		if info, err := e.Info(); err == nil {
			bytes += info.Size()
		}
	}
	return
}

// lastStoredScanTime returns the timestamp of the most recent scan stored
// in today's daily file (today = now's Vietnam-local calendar day), if any.
// Used by runWorker on start to decide whether the immediate "scan now"
// step should be skipped: lastScanAt (monitor.go) is in-memory only and
// always resets to zero on process restart, so it can never see a scan
// from just before the restart — only the stored file can.
func lastStoredScanTime(now time.Time) (time.Time, bool) {
	day := now.In(vnLocation).Format("2006-01-02")
	data, err := os.ReadFile(filepath.Join(dataDir(), day+".json"))
	if err != nil {
		return time.Time{}, false
	}
	var rec dailyRecord
	if err := json.Unmarshal(data, &rec); err != nil || len(rec.Scans) == 0 {
		return time.Time{}, false
	}
	// saveScanData always appends, never reorders, so the last entry is
	// the most recent scan.
	t, err := time.Parse(time.RFC3339, rec.Scans[len(rec.Scans)-1].ScannedAt)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// dedupLatestPerSlot collapses a day's stored scans so that when 2+ scans
// land in the same cfg.IntervalMin-wide time slot — which happens whenever
// the Traffic Monitor service restarts shortly after its previous scan,
// since the worker always re-scans immediately on start (see runWorker) —
// only the latest scan in that slot is kept. It never modifies the stored
// daily file: callers pass in rec.Scans and use the returned slice only
// for their own in-memory aggregation (point counts, averages, etc.),
// exactly the "fix the computation, don't touch the original file"
// approach already used for time-matched vs full-day metrics in
// internal/trafficinsight/insight.go. The slot-bucketing itself mirrors
// internal/trafficinsight/route_tti.go's slotOfTimestamp: minute-of-day
// floor-divided by the interval, calendar date ignored — safe here because
// scans is always one day's worth already (one dailyRecord).
func dedupLatestPerSlot(scans []sheetsPayload, intervalMin int) []sheetsPayload {
	if intervalMin <= 0 {
		intervalMin = 15
	}
	if len(scans) <= 1 {
		return scans
	}

	type latestInSlot struct {
		at  time.Time
		idx int
	}
	bestBySlot := map[int]latestInSlot{}
	keep := make([]bool, len(scans))
	for i := range keep {
		keep[i] = true
	}

	for i, s := range scans {
		t, err := time.Parse(time.RFC3339, s.ScannedAt)
		if err != nil {
			continue // unparseable timestamp: never dedup, always keep
		}
		local := t.In(vnLocation)
		slot := (local.Hour()*60 + local.Minute()) / intervalMin
		if cur, ok := bestBySlot[slot]; ok {
			if t.After(cur.at) {
				keep[cur.idx] = false
				bestBySlot[slot] = latestInSlot{at: t, idx: i}
			} else {
				keep[i] = false
			}
		} else {
			bestBySlot[slot] = latestInSlot{at: t, idx: i}
		}
	}

	out := make([]sheetsPayload, 0, len(scans))
	for i, s := range scans {
		if keep[i] {
			out = append(out, s)
		}
	}
	return out
}

// readScanFile returns the raw JSON of one stored daily file.
// The name is validated to prevent path traversal.
func readScanFile(name string) ([]byte, error) {
	if name != filepath.Base(name) || !dailyFileRe.MatchString(name) {
		return nil, fmt.Errorf("invalid file name")
	}
	return os.ReadFile(filepath.Join(dataDir(), name))
}
