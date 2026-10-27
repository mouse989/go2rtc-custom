package traffic

// persistent_export.go — lets an admin export persistent jam-point history
// as a CSV in the exact "Timestamp,Lat,Lng,JamFactor" shape an external
// map tool (e.g. Google My Maps) expects, over any Từ/Đến date-time range
// they pick — not just the files already on disk one at a time (the
// existing "Scan History" list only offers a whole day's raw JSON).
//
// The CSV itself is built client-side (www/traffic.html), same convention
// as admin.html's other CSV exports (travel-time logs, push log); this
// file only has to hand back the flattened point rows for the requested
// range, each already carrying its final display string for the Timestamp
// column — computed here (not in JS) so every row's wall-clock reading is
// anchored to the same Asia/Ho_Chi_Minh zone every other part of this
// package uses, regardless of the admin's own browser timezone.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PersistentExportRow is one persistent-jam-point reading, flattened to the
// 4 columns the external tool's CSV format expects.
type PersistentExportRow struct {
	// Timestamp is pre-formatted "01/02/2006 15:04:05" in Vietnam local
	// time — matches the external tool's own export exactly, not ISO 8601.
	Timestamp string  `json:"timestamp"`
	Lat       float64 `json:"lat"`
	Lng       float64 `json:"lng"`
	JamFactor float64 `json:"jamFactor"`
}

// PersistentPointsInRange returns every persistent-jam-point reading whose
// scan timestamp falls within [from, to] inclusive, across every stored
// daily history file that could overlap that range. Rows come out in
// chronological order (file order, then within-file scan order) — storage
// never reorders scans within a day, so no separate sort is needed there.
func PersistentPointsInRange(from, to time.Time) ([]PersistentExportRow, error) {
	entries, err := os.ReadDir(dataDir())
	if err != nil {
		if os.IsNotExist(err) {
			return []PersistentExportRow{}, nil
		}
		return nil, err
	}

	// Narrow to the day-files that could possibly contain a scan in range
	// before opening any of them — a file is named by its Vietnam-local
	// calendar day, so this is a cheap string-range filter over the
	// directory listing rather than reading every stored day.
	fromDay := from.In(vnLocation).Format("2006-01-02")
	toDay := to.In(vnLocation).Format("2006-01-02")

	var names []string
	for _, e := range entries {
		if e.IsDir() || !dailyFileRe.MatchString(e.Name()) {
			continue
		}
		day := strings.TrimSuffix(e.Name(), ".json")
		if day < fromDay || day > toDay {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	out := []PersistentExportRow{}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dataDir(), name))
		if err != nil {
			continue // best-effort — one unreadable day shouldn't fail the whole export
		}
		var rec dailyRecord
		if json.Unmarshal(data, &rec) != nil {
			continue
		}
		for _, scan := range dedupLatestPerSlot(rec.Scans, IntervalMin()) {
			t, err := time.Parse(time.RFC3339, scan.ScannedAt)
			if err != nil {
				continue
			}
			if t.Before(from) || t.After(to) {
				continue
			}
			ts := t.In(vnLocation).Format("01/02/2006 15:04:05")
			for _, p := range scan.Persistent {
				out = append(out, PersistentExportRow{
					Timestamp: ts, Lat: p.Lat, Lng: p.Lng, JamFactor: p.JamFactor,
				})
			}
		}
	}
	return out, nil
}

// PersistentPointsForDate returns every persistent-jam point recorded on
// one Vietnam-local calendar day ("YYYY-MM-DD"), across every scan that
// day, in the DashPoint shape (keeping Label/Area/Region) a map popup
// wants — unlike PersistentPointsInRange above, whose PersistentExportRow
// is deliberately narrowed to the external CSV tool's 4 columns. Used by
// internal/trafficinsight's "xem theo ngày" map mode (as opposed to
// LatestSnapshot's live "hiện tại" points).
func PersistentPointsForDate(date string) ([]DashPoint, error) {
	data, err := os.ReadFile(filepath.Join(dataDir(), date+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return []DashPoint{}, nil
		}
		return nil, err
	}
	var rec dailyRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	out := []DashPoint{}
	for _, scan := range dedupLatestPerSlot(rec.Scans, IntervalMin()) {
		for _, p := range scan.Persistent {
			out = append(out, DashPoint{
				Lat: p.Lat, Lng: p.Lng, JamFactor: p.JamFactor,
				Label: p.Label, Area: p.Area, Region: p.Region,
			})
		}
	}
	return out, nil
}
