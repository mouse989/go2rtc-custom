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
