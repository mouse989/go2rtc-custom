package aievent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDescribeVehicleBuildsSummaryFromKnownFields(t *testing.T) {
	details := json.RawMessage(`{"name":"98A27706","brand_code":"TOYOTA","color_code":"WHITE","type":"car"}`)
	got := describeVehicle(details)
	want := "Biển số 98A27706 · TOYOTA · WHITE · car"
	if got != want {
		t.Errorf("describeVehicle = %q, want %q", got, want)
	}
}

func TestDescribeVehicleHandlesMissingFields(t *testing.T) {
	details := json.RawMessage(`{"owner_id":73510}`)
	if got := describeVehicle(details); got != "" {
		t.Errorf("describeVehicle = %q, want empty (none of the known fields present)", got)
	}
}

func TestDescribeVehicleHandlesEmptyOrInvalidInput(t *testing.T) {
	if got := describeVehicle(nil); got != "" {
		t.Errorf("describeVehicle(nil) = %q, want empty", got)
	}
	if got := describeVehicle(json.RawMessage(`not json`)); got != "" {
		t.Errorf("describeVehicle(invalid) = %q, want empty", got)
	}
}

func TestIngestHawkwiseAIFlowCreatesEventWithCorrectFields(t *testing.T) {
	setupTest(t)
	details := json.RawMessage(`{"name":"98A27706","brand_code":"TOYOTA","color_code":"WHITE"}`)
	created, err := IngestHawkwiseAIFlow(HawkwiseAIFlowInput{
		TriggerCode:     "AbnormalStop",
		EventID:         482559,
		EventIDString:   "a3066d64-b808-4df0-ba08-d2b653acb88f",
		AIFlowCode:      21,
		AIFlowName:      "Abnormal Stop",
		ConfidentScore:  0.95,
		StartTime:       time.Date(2026, 3, 11, 11, 36, 13, 0, time.UTC),
		EndTime:         time.Date(2026, 3, 11, 11, 36, 18, 0, time.UTC),
		SourceUUID:      "cam-1",
		CameraLabel:     "Camera ngã tư — Ngã tư Hàng Xanh",
		Lat:             21.0245,
		Lng:             105.8412,
		Details:         details,
		RawBody:         `{"general":{"trigger_code":"AbnormalStop"}}`,
		IntegrationName: "Hawkwise Prod Key",
	})
	if err != nil {
		t.Fatalf("IngestHawkwiseAIFlow: %v", err)
	}
	if !created {
		t.Fatal("expected created=true for a never-seen event")
	}

	ev, ok := Get("hawkwise:a3066d64-b808-4df0-ba08-d2b653acb88f")
	if !ok {
		t.Fatal("expected to find the ingested event by its hawkwise:<event_id_string> RecordID")
	}
	if ev.CategorySource != CategorySourceHawkwise {
		t.Errorf("CategorySource = %q, want %q", ev.CategorySource, CategorySourceHawkwise)
	}
	if ev.CategoryTypeID != 21 {
		t.Errorf("CategoryTypeID = %d, want 21 (ai_flow_code)", ev.CategoryTypeID)
	}
	if ev.CategoryDescription != "Abnormal Stop" {
		t.Errorf("CategoryDescription = %q, want %q", ev.CategoryDescription, "Abnormal Stop")
	}
	if ev.RecordType != "AbnormalStop" {
		t.Errorf("RecordType = %q, want %q", ev.RecordType, "AbnormalStop")
	}
	if ev.Description != "Biển số 98A27706 · TOYOTA · WHITE" {
		t.Errorf("Description = %q", ev.Description)
	}
	if ev.SourceProcessor != "Camera ngã tư — Ngã tư Hàng Xanh" {
		t.Errorf("SourceProcessor = %q", ev.SourceProcessor)
	}
	if ev.Lat != 21.0245 || ev.Lng != 105.8412 {
		t.Errorf("Lat/Lng = %v/%v", ev.Lat, ev.Lng)
	}
	if ev.Status != StatusActive {
		t.Errorf("Status = %q, want active", ev.Status)
	}
	if len(ev.VehicleDetail) == 0 {
		t.Error("expected VehicleDetail to hold the raw details object")
	}
	if ev.Source != "Hawkwise Prod Key" {
		t.Errorf("Source = %q, want the integration's display name", ev.Source)
	}

	// Also recorded into the observed-categories registry under the
	// Hawkwise source, so the admin GUI can surface the raw sample.
	observed := ListObservedCategories()
	found := false
	for _, o := range observed {
		if o.Source == CategorySourceHawkwise && o.TypeID == 21 {
			found = true
			if o.SampleRawJSON == "" {
				t.Error("expected SampleRawJSON to be populated for a Hawkwise observation")
			}
		}
	}
	if !found {
		t.Error("expected an ObservedCategory entry for (hawkwise, ai_flow_code=21)")
	}
}

// Re-ingesting the same event_id_string must update the existing record,
// not create a duplicate — same dedup guarantee Ingest already provides by
// RecordID, exercised here through the Hawkwise-specific "hawkwise:<uuid>"
// RecordID construction.
func TestIngestHawkwiseAIFlowDedupsByEventIDString(t *testing.T) {
	setupTest(t)
	in := HawkwiseAIFlowInput{
		TriggerCode: "AbnormalStop", EventID: 1, EventIDString: "dup-uuid",
		AIFlowCode: 21, AIFlowName: "Abnormal Stop",
		StartTime: time.Now(), EndTime: time.Now(),
		SourceUUID: "cam-1", RawBody: "{}",
	}
	created1, err := IngestHawkwiseAIFlow(in)
	if err != nil || !created1 {
		t.Fatalf("first ingest: created=%v err=%v", created1, err)
	}
	created2, err := IngestHawkwiseAIFlow(in)
	if err != nil || created2 {
		t.Fatalf("second ingest with same event_id_string: created=%v err=%v, want created=false", created2, err)
	}
}

// When Hawkwise omits event_id_string, the numeric event_id is the fallback
// dedup key.
func TestIngestHawkwiseAIFlowFallsBackToNumericEventID(t *testing.T) {
	setupTest(t)
	in := HawkwiseAIFlowInput{
		TriggerCode: "AbnormalStop", EventID: 999, EventIDString: "",
		AIFlowCode: 21, StartTime: time.Now(), EndTime: time.Now(),
		SourceUUID: "cam-1", RawBody: "{}",
	}
	if _, err := IngestHawkwiseAIFlow(in); err != nil {
		t.Fatalf("IngestHawkwiseAIFlow: %v", err)
	}
	if _, ok := Get("hawkwise:999"); !ok {
		t.Error("expected RecordID \"hawkwise:999\" when event_id_string is empty")
	}
}

func TestIngestHawkwiseAIFlowFetchesEvidenceImages(t *testing.T) {
	setupTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(fakeJPEGBytes)
	}))
	defer srv.Close()

	_, err := IngestHawkwiseAIFlow(HawkwiseAIFlowInput{
		TriggerCode: "AbnormalStop", EventID: 2, EventIDString: "img-uuid",
		AIFlowCode: 21, StartTime: time.Now(), EndTime: time.Now(),
		SourceUUID: "cam-1", FrameURL: srv.URL, RawBody: "{}",
	})
	if err != nil {
		t.Fatalf("IngestHawkwiseAIFlow: %v", err)
	}
	ev, ok := Get("hawkwise:img-uuid")
	if !ok {
		t.Fatal("event not found")
	}
	if len(ev.ImagePaths) != 1 {
		t.Fatalf("expected 1 fetched image, got %d (%v)", len(ev.ImagePaths), ev.ImagePaths)
	}
}

// A dead/unreachable evidence URL must not fail the whole event — Hawkwise
// doesn't guarantee these links stay valid (see the plan doc), so this is
// an expected, tolerated outcome.
func TestIngestHawkwiseAIFlowToleratesUnreachableEvidenceURL(t *testing.T) {
	setupTest(t)
	created, err := IngestHawkwiseAIFlow(HawkwiseAIFlowInput{
		TriggerCode: "AbnormalStop", EventID: 3, EventIDString: "dead-url-uuid",
		AIFlowCode: 21, StartTime: time.Now(), EndTime: time.Now(),
		SourceUUID: "cam-1", FrameURL: "http://127.0.0.1:1/unreachable", RawBody: "{}",
	})
	if err != nil {
		t.Fatalf("IngestHawkwiseAIFlow should not fail just because the image fetch failed: %v", err)
	}
	if !created {
		t.Fatal("expected the event to still be created")
	}
	ev, ok := Get("hawkwise:dead-url-uuid")
	if !ok {
		t.Fatal("event not found")
	}
	if len(ev.ImagePaths) != 0 {
		t.Errorf("expected no images for an unreachable URL, got %v", ev.ImagePaths)
	}
}

// backdateHawkwiseRecord directly rewrites a stored record's UpdatedAt —
// white-box access to this package's own days/index state (same convention
// setupTest already uses), needed to simulate "this chain went quiet a
// while ago" without waiting real wall-clock time.
func backdateHawkwiseRecord(t *testing.T, recordID string, when time.Time) {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	dk, ok := index[recordID]
	if !ok {
		t.Fatalf("backdateHawkwiseRecord: %q not indexed", recordID)
	}
	ev, ok := days[dk][recordID]
	if !ok {
		t.Fatalf("backdateHawkwiseRecord: %q not found in day %q", recordID, dk)
	}
	ev.UpdatedAt = when
}

// Two pushes from the same camera + ai_flow_code + nearby coordinate,
// close together in time, must collapse into ONE record — the second
// push's content replaces the first's rather than creating a second
// marker at the same spot. This is the exact symptom reported: repeated
// Hawkwise "Đông xe" pushes for one ongoing situation piling up as
// separate map markers/previews.
func TestIngestHawkwiseAIFlowMergesRepeatedSituationFromSameCamera(t *testing.T) {
	setupTest(t)
	base := HawkwiseAIFlowInput{
		TriggerCode: "Congestion", AIFlowCode: 30, AIFlowName: "Đông xe",
		SourceUUID: "cam-nga-tu-1", Lat: 10.123, Lng: 106.456,
		StartTime: time.Now(), EndTime: time.Now(), RawBody: "{}",
	}

	first := base
	first.EventID = 1
	first.EventIDString = "evt-1"
	created1, err := IngestHawkwiseAIFlow(first)
	if err != nil || !created1 {
		t.Fatalf("first ingest: created=%v err=%v", created1, err)
	}

	second := base
	second.EventID = 2
	second.EventIDString = "evt-2"
	second.Lat, second.Lng = 10.12305, 106.45598 // a few metres off — same physical camera
	created2, err := IngestHawkwiseAIFlow(second)
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if created2 {
		t.Error("expected the second push to merge into the first record (created=false), got created=true")
	}

	if _, ok := Get("hawkwise:evt-1"); !ok {
		t.Fatal("expected the merged record to still be addressable by the first push's RecordID")
	}
	if _, ok := Get("hawkwise:evt-2"); ok {
		t.Error("expected no separate record under the second push's own RecordID — it should have merged into the first")
	}

	active, err := ListActive(0)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("expected exactly 1 active event after 2 merged pushes, got %d", len(active))
	}
	if active[0].EventCode != "evt-2" {
		t.Errorf("expected the merged record's EventCode to reflect the LATEST push (evt-2), got %q", active[0].EventCode)
	}
}

// A different camera (different source_uuid) must never merge, even with
// the same ai_flow_code and an identical coordinate.
func TestIngestHawkwiseAIFlowDoesNotMergeDifferentCamera(t *testing.T) {
	setupTest(t)
	base := HawkwiseAIFlowInput{
		AIFlowCode: 30, Lat: 10.123, Lng: 106.456,
		StartTime: time.Now(), EndTime: time.Now(), RawBody: "{}",
	}
	a := base
	a.EventID, a.EventIDString, a.SourceUUID = 1, "evt-a", "cam-A"
	if _, err := IngestHawkwiseAIFlow(a); err != nil {
		t.Fatal(err)
	}
	b := base
	b.EventID, b.EventIDString, b.SourceUUID = 2, "evt-b", "cam-B"
	created, err := IngestHawkwiseAIFlow(b)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("expected a different camera's push to create its own record, not merge")
	}
	active, _ := ListActive(0)
	if len(active) != 2 {
		t.Errorf("expected 2 separate active events for 2 different cameras, got %d", len(active))
	}
}

// A coordinate far outside the merge radius must never merge, even from
// what claims to be the same camera/ai_flow_code — treated as a data
// inconsistency worth keeping separate rather than silently merging.
func TestIngestHawkwiseAIFlowDoesNotMergeFarApartCoordinate(t *testing.T) {
	setupTest(t)
	base := HawkwiseAIFlowInput{
		AIFlowCode: 30, SourceUUID: "cam-1",
		StartTime: time.Now(), EndTime: time.Now(), RawBody: "{}",
	}
	a := base
	a.EventID, a.EventIDString = 1, "evt-a"
	a.Lat, a.Lng = 10.123, 106.456
	if _, err := IngestHawkwiseAIFlow(a); err != nil {
		t.Fatal(err)
	}
	b := base
	b.EventID, b.EventIDString = 2, "evt-b"
	b.Lat, b.Lng = 10.223, 106.556 // >100m away — well outside hawkwiseMergeRadiusM
	created, err := IngestHawkwiseAIFlow(b)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("expected a far-apart coordinate to create its own record, not merge")
	}
}

// Once a chain has gone quiet longer than hawkwiseMergeWindow, the next
// push for the same camera/ai_flow_code/coordinate must start a fresh
// record rather than reviving the stale one.
func TestIngestHawkwiseAIFlowDoesNotMergeAfterWindowExpires(t *testing.T) {
	setupTest(t)
	base := HawkwiseAIFlowInput{
		AIFlowCode: 30, SourceUUID: "cam-1", Lat: 10.123, Lng: 106.456,
		StartTime: time.Now(), EndTime: time.Now(), RawBody: "{}",
	}
	a := base
	a.EventID, a.EventIDString = 1, "evt-a"
	if _, err := IngestHawkwiseAIFlow(a); err != nil {
		t.Fatal(err)
	}
	backdateHawkwiseRecord(t, "hawkwise:evt-a", time.Now().Add(-(hawkwiseMergeWindow + time.Minute)))

	b := base
	b.EventID, b.EventIDString = 2, "evt-b"
	created, err := IngestHawkwiseAIFlow(b)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("expected a push after the merge window expired to create a new record, not revive the stale one")
	}
}

// Hawkwise omitting source_uuid must never merge blind — there is nothing
// stable to match on, and merging by ai_flow_code + coordinate alone risks
// conflating two different cameras pointed at the same intersection.
func TestIngestHawkwiseAIFlowDoesNotMergeWithoutSourceUUID(t *testing.T) {
	setupTest(t)
	base := HawkwiseAIFlowInput{
		AIFlowCode: 30, Lat: 10.123, Lng: 106.456,
		StartTime: time.Now(), EndTime: time.Now(), RawBody: "{}",
	}
	a := base
	a.EventID, a.EventIDString = 1, "evt-a"
	if _, err := IngestHawkwiseAIFlow(a); err != nil {
		t.Fatal(err)
	}
	b := base
	b.EventID, b.EventIDString = 2, "evt-b"
	created, err := IngestHawkwiseAIFlow(b)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("expected a push with no source_uuid to never merge")
	}
}
