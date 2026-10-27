package traffic

// duplicate_scans.go — "kiểm tra" tool: a read-only report of exactly which
// stored scans landed in the same cfg.IntervalMin-wide time slot on a given
// day (or across a range of days). This is the diagnostic half of the
// duplicate-scan fix; the correction itself happens automatically wherever
// aggregated point counts are computed (storage.go's dedupLatestPerSlot,
// used by PersistentPointsInRange/PersistentPointsForDate) — nothing here
// changes any stored file. It exists so an admin can see, concretely, how
// many extra scans a given restart produced and which one the computation
// now keeps, surfaced in www/traffic.html next to "Scan History".

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// maxDuplicateScanRangeDays caps how many days DetectDuplicateScans will
// walk in one call — this report lists every scan in every duplicate slot
// (not just aggregate counts), so an unbounded range could mean reading
// and fully parsing a very large number of daily files.
const maxDuplicateScanRangeDays = 31

// DuplicateScanEntry is one stored scan within a duplicate-scan slot.
type DuplicateScanEntry struct {
	ScannedAt  string `json:"scannedAt"` // RFC3339, as stored
	Local      string `json:"local"`     // "15:04:05 02/01/2006" Vietnam-local, for display
	Raw        int    `json:"raw"`
	Filtered   int    `json:"filtered"`
	Persistent int    `json:"persistent"`
	// Kept is true for the one scan in this slot that
	// dedupLatestPerSlot (storage.go) keeps for aggregation — the latest
	// by timestamp. The others are the "duplicates" this report flags.
	Kept bool `json:"kept"`
}

// DuplicateScanGroup is one time slot, on one day, that stored 2+ scans.
type DuplicateScanGroup struct {
	Date      string                `json:"date"`
	SlotLabel string                `json:"slotLabel"` // e.g. "10:00–10:15"
	Scans     []DuplicateScanEntry  `json:"scans"`
}

// DuplicateScanReport summarises every duplicate-scan slot found across
// [from, to] (inclusive, Vietnam-local calendar days).
type DuplicateScanReport struct {
	Groups         []DuplicateScanGroup `json:"groups"`
	IntervalMin    int                  `json:"intervalMin"`
	DaysScanned    int                  `json:"daysScanned"`   // days that had a stored file
	TotalScans     int                  `json:"totalScans"`    // every scan across those days
	DuplicateScans int                  `json:"duplicateScans"` // scans beyond the kept one in each group
}

// DetectDuplicateScans walks the stored daily files for [from, to]
// ("YYYY-MM-DD", inclusive, swapped if reversed, clamped to
// maxDuplicateScanRangeDays keeping the most recent end) and reports every
// time slot that stored more than one scan.
func DetectDuplicateScans(from, to string) (DuplicateScanReport, error) {
	fromDate, err := time.ParseInLocation("2006-01-02", from, vnLocation)
	if err != nil {
		return DuplicateScanReport{}, fmt.Errorf("invalid from date: %w", err)
	}
	toDate, err := time.ParseInLocation("2006-01-02", to, vnLocation)
	if err != nil {
		return DuplicateScanReport{}, fmt.Errorf("invalid to date: %w", err)
	}
	if toDate.Before(fromDate) {
		fromDate, toDate = toDate, fromDate
	}
	if days := int(toDate.Sub(fromDate).Hours()/24) + 1; days > maxDuplicateScanRangeDays {
		fromDate = toDate.AddDate(0, 0, -(maxDuplicateScanRangeDays - 1))
	}

	intervalMin := IntervalMin()
	report := DuplicateScanReport{IntervalMin: intervalMin}

	for d := fromDate; !d.After(toDate); d = d.AddDate(0, 0, 1) {
		dateStr := d.Format("2006-01-02")
		data, err := os.ReadFile(filepath.Join(dataDir(), dateStr+".json"))
		if err != nil {
			continue // no stored file that day — nothing to report
		}
		var rec dailyRecord
		if json.Unmarshal(data, &rec) != nil || len(rec.Scans) == 0 {
			continue
		}
		report.DaysScanned++
		report.TotalScans += len(rec.Scans)

		bySlot := map[int][]int{} // slot -> indices into rec.Scans, in stored order
		for i, s := range rec.Scans {
			t, err := time.Parse(time.RFC3339, s.ScannedAt)
			if err != nil {
				continue
			}
			local := t.In(vnLocation)
			slot := (local.Hour()*60 + local.Minute()) / intervalMin
			bySlot[slot] = append(bySlot[slot], i)
		}

		slots := make([]int, 0, len(bySlot))
		for slot := range bySlot {
			slots = append(slots, slot)
		}
		sort.Ints(slots)

		for _, slot := range slots {
			idxs := bySlot[slot]
			if len(idxs) < 2 {
				continue
			}

			// Same rule dedupLatestPerSlot uses: keep the latest timestamp.
			keepIdx := idxs[0]
			keepAt, _ := time.Parse(time.RFC3339, rec.Scans[keepIdx].ScannedAt)
			for _, i := range idxs[1:] {
				t, err := time.Parse(time.RFC3339, rec.Scans[i].ScannedAt)
				if err == nil && t.After(keepAt) {
					keepIdx, keepAt = i, t
				}
			}

			group := DuplicateScanGroup{Date: dateStr, SlotLabel: slotLabel(slot, intervalMin)}
			for _, i := range idxs {
				s := rec.Scans[i]
				t, _ := time.Parse(time.RFC3339, s.ScannedAt)
				group.Scans = append(group.Scans, DuplicateScanEntry{
					ScannedAt:  s.ScannedAt,
					Local:      t.In(vnLocation).Format("15:04:05 02/01/2006"),
					Raw:        len(s.Raw),
					Filtered:   len(s.Filtered),
					Persistent: len(s.Persistent),
					Kept:       i == keepIdx,
				})
			}
			report.Groups = append(report.Groups, group)
			report.DuplicateScans += len(idxs) - 1
		}
	}

	return report, nil
}

// slotLabel formats a slot index as its "HH:MM–HH:MM" wall-clock window.
func slotLabel(slot, intervalMin int) string {
	startMin := slot * intervalMin
	endMin := startMin + intervalMin
	return fmt.Sprintf("%02d:%02d–%02d:%02d", startMin/60, startMin%60, (endMin/60)%24, endMin%60)
}
