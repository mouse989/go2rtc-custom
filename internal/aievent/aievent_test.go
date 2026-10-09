package aievent

import (
	"testing"
	"time"
)

func setupTest(t *testing.T) {
	t.Helper()
	dataDir = t.TempDir()
	days = map[string]map[string]*AIEvent{}
	index = map[string]string{}
	if err := initCategories(dataDir); err != nil {
		t.Fatalf("initCategories: %v", err)
	}
}

func TestIngestCreatesNewRecord(t *testing.T) {
	setupTest(t)
	created, err := Ingest(&AIEvent{RecordID: "r1", CategoryTypeID: 12, StartTime: time.Now()}, false)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if !created {
		t.Fatal("expected created=true for a new RecordID")
	}
	got, ok := Get("r1")
	if !ok {
		t.Fatal("expected to find r1")
	}
	if got.Status != StatusActive {
		t.Errorf("expected StatusActive, got %v", got.Status)
	}
	if got.ReceivedAt.IsZero() {
		t.Error("expected ReceivedAt to be set")
	}
}

func TestIngestUpdatesExistingRecordPreservingReceivedAt(t *testing.T) {
	setupTest(t)
	if _, err := Ingest(&AIEvent{RecordID: "r1", CategoryTypeID: 12, StartTime: time.Now()}, false); err != nil {
		t.Fatal(err)
	}
	first, _ := Get("r1")

	created, err := Ingest(&AIEvent{RecordID: "r1", CategoryTypeID: 99, StartTime: time.Now()}, false)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("expected created=false for an existing RecordID")
	}
	second, _ := Get("r1")
	if !second.ReceivedAt.Equal(first.ReceivedAt) {
		t.Errorf("ReceivedAt should be preserved across updates, got %v vs %v", second.ReceivedAt, first.ReceivedAt)
	}
	if second.CategoryTypeID != 99 {
		t.Errorf("expected updated CategoryTypeID 99, got %d", second.CategoryTypeID)
	}
}

func TestIngestTerminateClosesKnownRecord(t *testing.T) {
	setupTest(t)
	if _, err := Ingest(&AIEvent{RecordID: "r1", CategoryTypeID: 12, StartTime: time.Now(), Lat: 10, Lng: 106}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Ingest(&AIEvent{RecordID: "r1", CategoryTypeID: 12, StartTime: time.Now()}, true); err != nil {
		t.Fatal(err)
	}
	got, _ := Get("r1")
	if got.Status != StatusTerminated {
		t.Errorf("expected StatusTerminated, got %v", got.Status)
	}
	if got.TerminatedAt.IsZero() {
		t.Error("expected TerminatedAt to be set")
	}
}

// TestIngestTerminateUnknownRecordIDStoresClosedStub is the "out-of-order
// delivery" case documented in docs/omnia-integration-api-spec.md §8: a
// terminate for a RecordID that was never created must not error — it's
// stored directly as already-closed.
func TestIngestTerminateUnknownRecordIDStoresClosedStub(t *testing.T) {
	setupTest(t)
	created, err := Ingest(&AIEvent{RecordID: "never-seen", CategoryTypeID: 1, StartTime: time.Now()}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("expected created=true for a never-seen RecordID")
	}
	got, ok := Get("never-seen")
	if !ok {
		t.Fatal("expected the stub record to exist")
	}
	if got.Status != StatusTerminated {
		t.Errorf("expected StatusTerminated for a terminate-before-create stub, got %v", got.Status)
	}
}

func TestIngestReopensTerminatedRecord(t *testing.T) {
	setupTest(t)
	Ingest(&AIEvent{RecordID: "r1", CategoryTypeID: 1, StartTime: time.Now()}, false)
	Ingest(&AIEvent{RecordID: "r1", CategoryTypeID: 1, StartTime: time.Now()}, true)
	if _, err := Ingest(&AIEvent{RecordID: "r1", CategoryTypeID: 1, StartTime: time.Now()}, false); err != nil {
		t.Fatal(err)
	}
	got, _ := Get("r1")
	if got.Status != StatusActive {
		t.Errorf("expected a later event.created to reopen the record, got status %v", got.Status)
	}
}

func TestListActiveExcludesTerminatedAndNoCoordinates(t *testing.T) {
	setupTest(t)
	Ingest(&AIEvent{RecordID: "with-coords", CategoryTypeID: 1, StartTime: time.Now(), Lat: 10, Lng: 106}, false)
	Ingest(&AIEvent{RecordID: "no-coords", CategoryTypeID: 1, StartTime: time.Now()}, false)
	Ingest(&AIEvent{RecordID: "terminated", CategoryTypeID: 1, StartTime: time.Now(), Lat: 10, Lng: 106}, false)
	Ingest(&AIEvent{RecordID: "terminated", CategoryTypeID: 1, StartTime: time.Now(), Lat: 10, Lng: 106}, true)

	list, err := ListActive(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].RecordID != "with-coords" {
		t.Errorf("expected only 'with-coords' in the active list, got %v", list)
	}
}

func TestListActiveExcludesOldReceivedAt(t *testing.T) {
	setupTest(t)
	if _, err := Ingest(&AIEvent{RecordID: "recent", CategoryTypeID: 1, StartTime: time.Now(), Lat: 10, Lng: 106}, false); err != nil {
		t.Fatal(err)
	}

	// Forge a 1-hour-old record directly (Ingest always stamps ReceivedAt=now).
	old := &AIEvent{RecordID: "old", CategoryTypeID: 1, Status: StatusActive, Lat: 10, Lng: 106, ReceivedAt: time.Now().Add(-1 * time.Hour)}
	dayKey := dayKeyFor(old.ReceivedAt)
	mu.Lock()
	_ = loadDayLocked(dayKey)
	days[dayKey][old.RecordID] = old
	index[old.RecordID] = dayKey
	mu.Unlock()

	list, err := ListActive(10) // 10-minute window
	if err != nil {
		t.Fatal(err)
	}
	foundOld, foundRecent := false, false
	for _, ev := range list {
		if ev.RecordID == "old" {
			foundOld = true
		}
		if ev.RecordID == "recent" {
			foundRecent = true
		}
	}
	if foundOld {
		t.Error("expected the 1-hour-old record to be excluded by a 10-minute window")
	}
	if !foundRecent {
		t.Error("expected the just-ingested record to be included")
	}
}

func TestSanitizeRecordIDBlocksPathTraversalChars(t *testing.T) {
	got := SanitizeRecordID("../../etc/passwd")
	if got == "../../etc/passwd" {
		t.Fatal("expected traversal characters to be replaced")
	}
	for _, r := range got {
		if r == '/' || r == '\\' {
			t.Fatalf("sanitized ID still contains a path separator: %q", got)
		}
	}
}
