package trafficflow

import (
	"testing"
	"time"
)

func TestIngestCreatesNewRecord(t *testing.T) {
	setupTestDB(t)
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
	setupTestDB(t)
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
	setupTestDB(t)
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

func TestIngestPicksMostRecentAsLatestForSameSensor(t *testing.T) {
	setupTestDB(t)
	if _, err := Ingest(&FlowRecord{EventID: 1, SourceUUID: "cam-9", Total: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := Ingest(&FlowRecord{EventID: 2, SourceUUID: "cam-9", Total: 7}); err != nil {
		t.Fatal(err)
	}

	rec, ok := GetLatestForSensor("cam-9")
	if !ok {
		t.Fatal("expected a latest record for cam-9")
	}
	if rec.Total != 7 {
		t.Errorf("expected the most recently ingested record (Total=7), got %d", rec.Total)
	}
}

func TestIngestPersistsAcrossReconnect(t *testing.T) {
	setupTestDB(t)
	dir := t.TempDir()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := initDB(dir); err != nil {
		t.Fatalf("initDB: %v", err)
	}
	if _, err := Ingest(&FlowRecord{EventID: 7, SourceUUID: "cam-9", Total: 5}); err != nil {
		t.Fatal(err)
	}

	// Simulate a server restart: close and reopen the same on-disk database.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := initDB(dir); err != nil {
		t.Fatalf("initDB (reopen): %v", err)
	}
	if !Exists(7) {
		t.Fatal("expected EventID 7 to still be indexed after reopening the database")
	}

	// A duplicate push after "restart" must still be rejected.
	created, err := Ingest(&FlowRecord{EventID: 7, SourceUUID: "cam-9", Total: 999})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("expected created=false for an EventID already present in the reopened database")
	}
}

func TestInsertFlowRecordPreservesGivenReceivedAt(t *testing.T) {
	setupTestDB(t)
	past, err := time.Parse(time.RFC3339, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insertFlowRecord(&FlowRecord{EventID: 1, SourceUUID: "cam-1", Total: 1, ReceivedAt: past}); err != nil {
		t.Fatal(err)
	}
	rec, ok := GetLatestForSensor("cam-1")
	if !ok {
		t.Fatal("expected a record for cam-1")
	}
	if !rec.ReceivedAt.Equal(past) {
		t.Errorf("expected insertFlowRecord to preserve ReceivedAt=%v, got %v", past, rec.ReceivedAt)
	}
}

func TestDeleteRecordsOlderThanPrunesOldRowsOnly(t *testing.T) {
	setupTestDB(t)
	recent := time.Now()
	old := recent.AddDate(0, 0, -40)

	if _, err := insertFlowRecord(&FlowRecord{EventID: 1, SourceUUID: "cam-1", Total: 1, ReceivedAt: recent}); err != nil {
		t.Fatal(err)
	}
	if _, err := insertFlowRecord(&FlowRecord{EventID: 99, SourceUUID: "cam-old", Total: 1, ReceivedAt: old}); err != nil {
		t.Fatal(err)
	}

	n := deleteRecordsOlderThan(30)
	if n != 1 {
		t.Errorf("expected exactly 1 old row removed, got %d", n)
	}
	if Exists(99) {
		t.Error("expected the old EventID to be pruned")
	}
	if !Exists(1) {
		t.Error("expected the recent EventID to survive the sweep")
	}
}
