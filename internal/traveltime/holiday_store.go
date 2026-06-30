package traveltime

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"
)

// HolidayEntry is a custom holiday or day-type override configured via admin UI.
// DateStart/DateEnd are inclusive "YYYY-MM-DD" strings (DateEnd = DateStart for a single day).
// Kind controls which traffic-profile scale factor is applied:
//
//	"tet"          → hkTet         (streets very quiet)
//	"national"     → hkNational    (moderate traffic reduction)
//	"school_break" → hkSchoolBreak (lighter morning commute)
//	"pre_holiday"  → hkPreHoliday  (afternoon/evening spike)
//	"working_day"  → hkNone        (override: treat as a normal working day)
type HolidayEntry struct {
	ID        string `json:"id"`             // short random key, e.g. "a3f8c2d1"
	DateStart string `json:"date_start"`     // "2026-04-07" (inclusive)
	DateEnd   string `json:"date_end"`       // "2026-04-07" or range end (inclusive)
	Kind      string `json:"kind"`           // see above
	Label     string `json:"label"`          // Vietnamese label shown on dashboard
	Note      string `json:"note,omitempty"` // admin-only note (not displayed on dashboard)
}

var (
	holidayMu       sync.RWMutex
	holidayEntries  []HolidayEntry
	holidayFilePath string
)

func initHolidayStore(path string) {
	holidayFilePath = path
	holidayMu.Lock()
	defer holidayMu.Unlock()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		holidayEntries = []HolidayEntry{}
		return
	}
	if err != nil {
		log.Warn().Err(err).Msg("[traveltime] holiday store read error")
		holidayEntries = []HolidayEntry{}
		return
	}
	if err = json.Unmarshal(data, &holidayEntries); err != nil {
		log.Warn().Err(err).Msg("[traveltime] holiday store parse error")
		holidayEntries = []HolidayEntry{}
	}
}

// listHolidayEntries returns all entries, optionally filtered by year.
// year == 0 returns all entries across all years.
func listHolidayEntries(year int) []HolidayEntry {
	holidayMu.RLock()
	defer holidayMu.RUnlock()
	if year == 0 {
		out := make([]HolidayEntry, len(holidayEntries))
		copy(out, holidayEntries)
		return out
	}
	prefix := fmt.Sprintf("%04d-", year)
	var out []HolidayEntry
	for _, e := range holidayEntries {
		if strings.HasPrefix(e.DateStart, prefix) || strings.HasPrefix(e.DateEnd, prefix) {
			out = append(out, e)
		}
	}
	if out == nil {
		out = []HolidayEntry{}
	}
	return out
}

// upsertHolidayEntry creates or updates a holiday entry (matched by ID).
func upsertHolidayEntry(e *HolidayEntry) error {
	if e.DateStart == "" {
		return fmt.Errorf("date_start is required")
	}
	if e.Kind == "" {
		return fmt.Errorf("kind is required")
	}
	if e.Label == "" {
		return fmt.Errorf("label is required")
	}
	if e.DateEnd == "" || e.DateEnd < e.DateStart {
		e.DateEnd = e.DateStart
	}
	holidayMu.Lock()
	defer holidayMu.Unlock()
	if e.ID == "" {
		e.ID = randHolidayID()
	}
	for i, existing := range holidayEntries {
		if existing.ID == e.ID {
			holidayEntries[i] = *e
			return writeHolidayFile()
		}
	}
	holidayEntries = append(holidayEntries, *e)
	return writeHolidayFile()
}

// deleteHolidayEntry removes the entry with the given ID.
func deleteHolidayEntry(id string) error {
	holidayMu.Lock()
	defer holidayMu.Unlock()
	for i, e := range holidayEntries {
		if e.ID == id {
			holidayEntries = append(holidayEntries[:i], holidayEntries[i+1:]...)
			return writeHolidayFile()
		}
	}
	return nil
}

// writeHolidayFile must be called with holidayMu write-locked.
func writeHolidayFile() error {
	data, err := json.MarshalIndent(holidayEntries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(holidayFilePath, data, 0644)
}

// lookupCustomKind checks whether d matches any admin-configured holiday entry.
// Returns (kind, label, true) when matched; the custom entry takes priority over
// the hardcoded calendar in holidays.go.
func lookupCustomKind(d time.Time) (hkKind, string, bool) {
	ds := d.In(loc()).Format("2006-01-02")
	holidayMu.RLock()
	defer holidayMu.RUnlock()
	for _, e := range holidayEntries {
		if ds >= e.DateStart && ds <= e.DateEnd {
			return kindStringToHK(e.Kind), e.Label, true
		}
	}
	return hkNone, "", false
}

func kindStringToHK(s string) hkKind {
	switch s {
	case "tet":
		return hkTet
	case "national":
		return hkNational
	case "school_break":
		return hkSchoolBreak
	case "pre_holiday":
		return hkPreHoliday
	default: // "working_day" or unrecognised → treat as normal day
		return hkNone
	}
}

var holidayRNG = rand.New(rand.NewSource(time.Now().UnixNano()))

const holidayIDChars = "abcdefghijklmnopqrstuvwxyz0123456789"

func randHolidayID() string {
	b := make([]byte, 8)
	for i := range b {
		b[i] = holidayIDChars[holidayRNG.Intn(len(holidayIDChars))]
	}
	return string(b)
}
