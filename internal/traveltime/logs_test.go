package traveltime

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func resetLogsState(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	initLogs(dir)

	cacheMu.Lock()
	cacheDate = ""
	cacheData = nil
	cacheMu.Unlock()
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

