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

var dailyFileRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.json$`)

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

	day := scannedAt.Format("2006-01-02")
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

	// Retention cleanup (best-effort, by date in filename)
	if c.Storage.RetentionDays > 0 {
		cutoff := time.Now().AddDate(0, 0, -c.Storage.RetentionDays).Format("2006-01-02")
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

// readScanFile returns the raw JSON of one stored daily file.
// The name is validated to prevent path traversal.
func readScanFile(name string) ([]byte, error) {
	if name != filepath.Base(name) || !dailyFileRe.MatchString(name) {
		return nil, fmt.Errorf("invalid file name")
	}
	return os.ReadFile(filepath.Join(dataDir(), name))
}
