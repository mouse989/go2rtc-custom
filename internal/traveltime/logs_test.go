package traveltime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func resetLogsState(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	initLogs(dir)

	cacheMu.Lock()
	cacheDate = ""
	cacheData = nil
	cacheMu.Unlock()

	histCacheMu.Lock()
	histCache = map[string][]LogEntry{}
	histCacheOrder = nil
	histCacheMu.Unlock()
}

// writeHistoricalDayFile writes a date's day-file directly to disk — same
// shape appendLogs would have produced, but for a day other than today
// (appendLogs itself only ever writes todayFile()).
func writeHistoricalDayFile(t *testing.T, date string, entries []LogEntry) {
	t.Helper()
	path := dayFile(date)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	for _, e := range entries {
		data, _ := json.Marshal(e)
		f.Write(data)
		f.Write([]byte("\n"))
	}
}

func TestGetLogsCachesHistoricalDayAcrossCalls(t *testing.T) {
	resetLogsState(t)
	const date = "2020-06-15" // well in the past relative to any real "today"
	writeHistoricalDayFile(t, date, []LogEntry{{RouteID: "r1"}, {RouteID: "r2"}})

	got, err := getLogs(date, 0)
	if err != nil {
		t.Fatalf("getLogs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %+v", got)
	}

	// Corrupt the on-disk file so a second read (if it happened) would fail
	// to parse — proving the second getLogs call is served from histCache,
	// not disk.
	if err := os.WriteFile(dayFile(date), []byte("not json\n"), 0644); err != nil {
		t.Fatalf("corrupt file: %v", err)
	}

	got2, err := getLogs(date, 0)
	if err != nil {
		t.Fatalf("getLogs (second call): %v", err)
	}
	if len(got2) != 2 || got2[0].RouteID != "r1" || got2[1].RouteID != "r2" {
		t.Fatalf("expected cached entries despite corrupted disk file, got %+v", got2)
	}
}

func TestGetLogsHistoricalReturnsCopyNotSharedSlice(t *testing.T) {
	resetLogsState(t)
	const date = "2020-06-15"
	writeHistoricalDayFile(t, date, []LogEntry{{RouteID: "r1"}})

	got, err := getLogs(date, 0)
	if err != nil {
		t.Fatalf("getLogs: %v", err)
	}
	got[0].RouteID = "mutated"

	got2, err := getLogs(date, 0)
	if err != nil {
		t.Fatalf("getLogs (second call): %v", err)
	}
	if got2[0].RouteID != "r1" {
		t.Fatalf("mutating a previous getLogs result affected the cache: %+v", got2)
	}
}

// A date that is NOT before today (today itself goes through the separate
// today-cache path; a future-looking date string must never be cached —
// that file may not exist yet, or may still be incomplete) must always
// reflect the current on-disk content, never a stale cached answer.
func TestGetLogsNeverCachesFutureDate(t *testing.T) {
	resetLogsState(t)
	future := time.Now().In(loc()).AddDate(1, 0, 0).Format("2006-01-02")
	writeHistoricalDayFile(t, future, []LogEntry{{RouteID: "first"}})

	got, err := getLogs(future, 0)
	if err != nil {
		t.Fatalf("getLogs: %v", err)
	}
	if len(got) != 1 || got[0].RouteID != "first" {
		t.Fatalf("expected 1 entry, got %+v", got)
	}

	writeHistoricalDayFile(t, future, []LogEntry{{RouteID: "second"}})
	got2, err := getLogs(future, 0)
	if err != nil {
		t.Fatalf("getLogs (second call): %v", err)
	}
	if len(got2) != 1 || got2[0].RouteID != "second" {
		t.Fatalf("expected the updated content (not cached), got %+v", got2)
	}

	histCacheMu.RLock()
	_, cached := histCache[future]
	histCacheMu.RUnlock()
	if cached {
		t.Error("a future-looking date must never be cached")
	}
}

func TestGetLogsHistoricalCacheEvictsOldestBeyondCap(t *testing.T) {
	resetLogsState(t)
	for i := 0; i < histCacheMaxDays+1; i++ {
		date := fmt.Sprintf("2019-01-%02d", i+1) // 01..61, all comfortably in the past
		writeHistoricalDayFile(t, date, []LogEntry{{RouteID: date}})
		if _, err := getLogs(date, 0); err != nil {
			t.Fatalf("getLogs(%s): %v", date, err)
		}
	}

	histCacheMu.RLock()
	n := len(histCache)
	_, oldestStillCached := histCache["2019-01-01"]
	_, newestCached := histCache[fmt.Sprintf("2019-01-%02d", histCacheMaxDays+1)]
	histCacheMu.RUnlock()

	if n != histCacheMaxDays {
		t.Errorf("histCache has %d entries, want %d (the cap)", n, histCacheMaxDays)
	}
	if oldestStillCached {
		t.Error("expected the oldest-inserted date to have been evicted")
	}
	if !newestCached {
		t.Error("expected the most recently inserted date to still be cached")
	}
}

func TestGetLogsServesTodayFromCacheWithoutRereadingDisk(t *testing.T) {
	resetLogsState(t)

	entries := []LogEntry{
		{Timestamp: "2020-01-01T00:00:00Z", RouteID: "r1"},
		{Timestamp: "2020-01-01T00:15:00Z", RouteID: "r2"},
	}
	if err := appendLogs(entries); err != nil {
		t.Fatalf("appendLogs: %v", err)
	}

	// Corrupt the on-disk file so any code path that still reads it would
	// fail to parse — proving getLogs("") is served from the cache, not disk.
	if err := os.WriteFile(todayFile(), []byte("not json\n"), 0644); err != nil {
		t.Fatalf("corrupt today file: %v", err)
	}

	got, err := getLogs("", 0)
	if err != nil {
		t.Fatalf("getLogs: %v", err)
	}
	if len(got) != 2 || got[0].RouteID != "r1" || got[1].RouteID != "r2" {
		t.Fatalf("expected the 2 appended entries from cache, got %+v", got)
	}
}

func TestGetLogsReturnsCopyNotSharedSlice(t *testing.T) {
	resetLogsState(t)

	if err := appendLogs([]LogEntry{{RouteID: "r1"}}); err != nil {
		t.Fatalf("appendLogs: %v", err)
	}

	got, err := getLogs("", 0)
	if err != nil {
		t.Fatalf("getLogs: %v", err)
	}
	got[0].RouteID = "mutated"

	got2, err := getLogs("", 0)
	if err != nil {
		t.Fatalf("getLogs: %v", err)
	}
	if got2[0].RouteID != "r1" {
		t.Fatalf("mutating a previous getLogs result affected the cache: %+v", got2)
	}
}

func TestGetLogsSeedsFromDiskWhenCacheUnseeded(t *testing.T) {
	resetLogsState(t)

	// Simulate entries already on disk from before this process started
	// (e.g. a restart mid-day) — no appendLogs call has happened yet, so
	// the cache is empty/unseeded.
	path := todayFile()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(
		`{"timestamp":"t","routeId":"pre-existing"}`+"\n"), 0644); err != nil {
		t.Fatalf("seed disk file: %v", err)
	}

	got, err := getLogs("", 0)
	if err != nil {
		t.Fatalf("getLogs: %v", err)
	}
	if len(got) != 1 || got[0].RouteID != "pre-existing" {
		t.Fatalf("expected the pre-existing on-disk entry, got %+v", got)
	}
}

func TestAppendLogsAfterColdSeedDoesNotDropPreExistingEntries(t *testing.T) {
	resetLogsState(t)

	path := todayFile()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(
		`{"timestamp":"t","routeId":"pre-existing"}`+"\n"), 0644); err != nil {
		t.Fatalf("seed disk file: %v", err)
	}

	// The cache has never been seeded (cacheDate == ""), so this append must
	// take the "reload from disk" branch — it must not reset the cache to
	// just its own entry and silently lose "pre-existing".
	if err := appendLogs([]LogEntry{{RouteID: "new"}}); err != nil {
		t.Fatalf("appendLogs: %v", err)
	}

	got, err := getLogs("", 0)
	if err != nil {
		t.Fatalf("getLogs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries (pre-existing + new), got %+v", got)
	}
	ids := map[string]bool{}
	for _, e := range got {
		ids[e.RouteID] = true
	}
	if !ids["pre-existing"] || !ids["new"] {
		t.Fatalf("lost an entry across cold-seed append: %+v", got)
	}
}

func TestConcurrentAppendAndGetLogsRace(t *testing.T) {
	resetLogsState(t)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		i := i
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = appendLogs([]LogEntry{{RouteID: fmt.Sprintf("r%d", i)}})
		}()
		go func() {
			defer wg.Done()
			_, _ = getLogs("", 0)
		}()
	}
	wg.Wait() // must finish without -race flagging a data race

	got, err := getLogs("", 0)
	if err != nil {
		t.Fatalf("getLogs: %v", err)
	}
	if len(got) != 20 {
		t.Fatalf("expected all 20 concurrently-appended entries, got %d: %+v", len(got), got)
	}
}
