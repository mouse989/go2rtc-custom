package trafficflow

import (
	"testing"
	"time"
)

func setupRecordsTest(t *testing.T) {
	t.Helper()
	dataDir = t.TempDir()
	days = map[string]map[int64]*FlowRecord{}
	index = map[int64]flowIndexEntry{}
	latest = map[string]*FlowRecord{}
	if err := initSensors(dataDir); err != nil {
		t.Fatalf("initSensors: %v", err)
	}
}

func TestIngestCreatesNewRecord(t *testing.T) {
	setupRecordsTest(t)
	created, err := Ingest(&FlowRecord{EventID: 1, SourceUUID: "cam-1", Total: 42})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if !created {
		t.Fatal("expected created=true for a new EventID")
	}
	if !Exists(1) {
		t.Fatal("expected Exists(1) to be true after Ingest")
	}
	rec, ok := GetLatestForSensor("cam-1")
	if !ok {
		t.Fatal("expected a latest record for cam-1")
	}
	if rec.Total != 42 {
		t.Errorf("expected Total=42, got %d", rec.Total)
	}
	if rec.ReceivedAt.IsZero() {
		t.Error("expected ReceivedAt to be set")
	}
}

func TestIngestDedupesByEventID(t *testing.T) {
	setupRecordsTest(t)
	if _, err := Ingest(&FlowRecord{EventID: 1, SourceUUID: "cam-1", Total: 42}); err != nil {
		t.Fatal(err)
	}
	first, _ := GetLatestForSensor("cam-1")

	// Same EventID retried (Hawkwise's own short-term retry behavior) with
	// different data must be a no-op per docs/hawkwise-flow-integration-plan.md
	// §2.5 — "nhận trùng → không cộng dồn/ghi đè".
	created, err := Ingest(&FlowRecord{EventID: 1, SourceUUID: "cam-1", Total: 999})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("expected created=false for a duplicate EventID")
	}
	second, _ := GetLatestForSensor("cam-1")
	if second.Total != first.Total {
		t.Errorf("duplicate push must not overwrite stored data: got Total=%d, want %d", second.Total, first.Total)
	}
}

func TestIngestTracksSeparateSensorsIndependently(t *testing.T) {
	setupRecordsTest(t)
	if _, err := Ingest(&FlowRecord{EventID: 1, SourceUUID: "cam-1", Total: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := Ingest(&FlowRecord{EventID: 2, SourceUUID: "cam-2", Total: 20}); err != nil {
		t.Fatal(err)
	}

	rec1, ok := GetLatestForSensor("cam-1")
	if !ok || rec1.Total != 10 {
		t.Errorf("expected cam-1 Total=10, got %+v (ok=%v)", rec1, ok)
	}
	rec2, ok := GetLatestForSensor("cam-2")
	if !ok || rec2.Total != 20 {
		t.Errorf("expected cam-2 Total=20, got %+v (ok=%v)", rec2, ok)
	}

	list := ListLatest()
	if len(list) != 2 {
		t.Fatalf("expected 2 entries from ListLatest, got %d", len(list))
	}
}

func TestIngestPersistsAcrossReload(t *testing.T) {
	setupRecordsTest(t)
	if _, err := Ingest(&FlowRecord{EventID: 7, SourceUUID: "cam-9", Total: 5}); err != nil {
		t.Fatal(err)
	}

	// Simulate a server restart: wipe in-memory state, reload from disk.
	days = map[string]map[int64]*FlowRecord{}
	index = map[int64]flowIndexEntry{}
	latest = map[string]*FlowRecord{}
	if err := initRecords(dataDir); err != nil {
		t.Fatalf("initRecords: %v", err)
	}
	if !Exists(7) {
		t.Fatal("expected EventID 7 to still be indexed after reload")
	}

	// A duplicate push after "restart" must still be rejected — the index
	// survives even though `latest`/`days` were rebuilt empty.
	created, err := Ingest(&FlowRecord{EventID: 7, SourceUUID: "cam-9", Total: 999})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("expected created=false for an EventID known only via the reloaded index")
	}
}

func TestPrimeLatestFromDiskRecoversMostRecentRecord(t *testing.T) {
	setupRecordsTest(t)
	if _, err := CreateSensor(Sensor{SourceUUID: "cam-5", Label: "Test", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := Ingest(&FlowRecord{EventID: 1, SourceUUID: "cam-5", Total: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := Ingest(&FlowRecord{EventID: 2, SourceUUID: "cam-5", Total: 2}); err != nil {
		t.Fatal(err)
	}

	// Wipe only the in-memory latest cache, as a restart would.
	latest = map[string]*FlowRecord{}
	primeLatestFromDisk()

	rec, ok := GetLatestForSensor("cam-5")
	if !ok {
		t.Fatal("expected primeLatestFromDisk to recover a record for cam-5")
	}
	if rec.Total != 2 {
		t.Errorf("expected the most recently received record (Total=2), got %d", rec.Total)
	}
}

func TestSanitizeSourceUUIDStripsPathTraversal(t *testing.T) {
	got := sanitizeSourceUUID("../../etc/passwd")
	if got == "" || got == "../../etc/passwd" {
		t.Errorf("expected sanitized output without path separators, got %q", got)
	}
	for _, c := range got {
		if c == '/' || c == '.' {
			t.Errorf("sanitized source_uuid %q still contains a path-traversal character", got)
			break
		}
	}
}

func TestDeleteRecordFilesOlderThanPrunesOldFilesOnly(t *testing.T) {
	setupRecordsTest(t)
	now := time.Now()
	if _, err := Ingest(&FlowRecord{EventID: 1, SourceUUID: "cam-1", Total: 1}); err != nil {
		t.Fatal(err)
	}

	// Manufacture an "old" day file directly, 40 days in the past, plus a
	// matching index entry — simulating data from well before retention.
	oldDayKey := now.AddDate(0, 0, -40).Format("2006-01-02")
	recMu.Lock()
	days[bucketKey("cam-old", oldDayKey)] = map[int64]*FlowRecord{99: {EventID: 99, SourceUUID: "cam-old"}}
	index[99] = flowIndexEntry{SourceUUID: "cam-old", DayKey: oldDayKey}
	recMu.Unlock()
	if err := saveDayLockedForTest("cam-old", oldDayKey); err != nil {
		t.Fatal(err)
	}

	n := deleteRecordFilesOlderThan(30)
	if n != 1 {
		t.Errorf("expected exactly 1 old file removed, got %d", n)
	}
	if Exists(99) {
		t.Error("expected the old EventID to be pruned from the index")
	}
	if !Exists(1) {
		t.Error("expected today's EventID to survive the sweep")
	}
}

// saveDayLockedForTest takes recMu itself so the test above doesn't need to
// reach into saveDayLocked's locking convention directly.
func saveDayLockedForTest(sourceUUID, dayKey string) error {
	recMu.Lock()
	defer recMu.Unlock()
	return saveDayLocked(sourceUUID, dayKey)
}
