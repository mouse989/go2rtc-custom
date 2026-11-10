package trafficflow

import "testing"

// abnormalStopSample is the real push body a user reported — a "trigger_code":
// "AbnormalStop" AI-flow event, structurally different from the density
// payload (see hawkwisePush) this package otherwise handles: "details" here
// is a vehicle/license-plate recognition object, not density_detail.
const abnormalStopSample = `{
  "details": {
    "owner_id": 73510,
    "owner_name": "Elcom Owner",
    "type": "car",
    "brand_code": "TOYOTA",
    "color_code": "WHITE",
    "name": "98A27706",
    "has_lp_detected": true
  },
  "general": {
    "trigger_code": "AbnormalStop",
    "event_id": 482559,
    "event_id_string": "a3066d64-b808-4df0-ba08-d2b653acb88f",
    "severity_level": 1,
    "start_time": "2026-03-11 11:36:13",
    "end_time": "2026-03-11 11:36:18",
    "ai_flow_code": 21,
    "ai_flow_name": "Abnormal Stop",
    "confident_score": 0.95,
    "camera": {
      "source_uuid": "c8b1469d-bb77-46d1-b5d1-d61e8c41780e",
      "source_name": "Stream 1",
      "hardware_name": "Camera ngã tư",
      "hardware_uuid": "839d33ae-5d71-42be-8682-600ae26acafe",
      "address": "Ngã tư Hàng Xanh",
      "longitude": 105.8412,
      "latitude": 21.0245
    }
  },
  "evidence": {
    "frame_url": "http://10.12.7.21:9490/event/evidence.jpg",
    "identified_crop_url": null,
    "body_crop_url": "http://10.12.7.21:9490/event/evidence_crop.jpg",
    "video_url": null
  }
}`

func TestBuildHawkwiseAIFlowInputMapsFields(t *testing.T) {
	setupTestDB(t)
	in, err := buildHawkwiseAIFlowInput([]byte(abnormalStopSample), "Hawkwise Prod Key")
	if err != nil {
		t.Fatalf("buildHawkwiseAIFlowInput: %v", err)
	}
	if in.TriggerCode != "AbnormalStop" {
		t.Errorf("TriggerCode = %q, want AbnormalStop", in.TriggerCode)
	}
	if in.EventID != 482559 {
		t.Errorf("EventID = %d, want 482559", in.EventID)
	}
	if in.EventIDString != "a3066d64-b808-4df0-ba08-d2b653acb88f" {
		t.Errorf("EventIDString = %q", in.EventIDString)
	}
	if in.AIFlowCode != 21 {
		t.Errorf("AIFlowCode = %d, want 21", in.AIFlowCode)
	}
	if in.AIFlowName != "Abnormal Stop" {
		t.Errorf("AIFlowName = %q, want %q", in.AIFlowName, "Abnormal Stop")
	}
	if in.SourceUUID != "c8b1469d-bb77-46d1-b5d1-d61e8c41780e" {
		t.Errorf("SourceUUID = %q", in.SourceUUID)
	}
	if in.CameraLabel != "Camera ngã tư — Ngã tư Hàng Xanh" {
		t.Errorf("CameraLabel = %q", in.CameraLabel)
	}
	// No Sensor mapping exists for this source_uuid yet, so Hawkwise's own
	// reported coordinate is used as-is.
	if in.Lat != 21.0245 || in.Lng != 105.8412 {
		t.Errorf("Lat/Lng = %v/%v, want Hawkwise's own reported 21.0245/105.8412", in.Lat, in.Lng)
	}
	if in.StartTime.IsZero() || in.EndTime.IsZero() {
		t.Error("expected StartTime/EndTime to be parsed, got zero value")
	}
	if in.FrameURL != "http://10.12.7.21:9490/event/evidence.jpg" {
		t.Errorf("FrameURL = %q", in.FrameURL)
	}
	if in.BodyCropURL != "http://10.12.7.21:9490/event/evidence_crop.jpg" {
		t.Errorf("BodyCropURL = %q", in.BodyCropURL)
	}
	if in.IdentifiedCropURL != "" {
		t.Errorf("IdentifiedCropURL = %q, want empty (source was null)", in.IdentifiedCropURL)
	}
	if len(in.Details) == 0 {
		t.Error("expected Details to carry the raw details object")
	}
	if in.RawBody != abnormalStopSample {
		t.Error("expected RawBody to be the exact original push body")
	}
	if in.IntegrationName != "Hawkwise Prod Key" {
		t.Errorf("IntegrationName = %q", in.IntegrationName)
	}
}

// An admin-confirmed Sensor mapping for this source_uuid must win over
// Hawkwise's own reported camera.{latitude,longitude} — same
// don't-auto-trust-the-vendor's-coordinate rule the density/pill map layer
// already applies (see sensors.go), so an AI-flow event from a mapped
// camera lands at the same point its density pill does.
func TestBuildHawkwiseAIFlowInputPrefersSensorMappingOverReportedCoordinate(t *testing.T) {
	setupTestDB(t)
	if _, err := CreateSensor(Sensor{
		SourceUUID: "c8b1469d-bb77-46d1-b5d1-d61e8c41780e",
		Label:      "Ngã tư Hàng Xanh (xác nhận)",
		Lat:        10.8, Lon: 106.7,
		Enabled: true,
	}); err != nil {
		t.Fatalf("CreateSensor: %v", err)
	}

	in, err := buildHawkwiseAIFlowInput([]byte(abnormalStopSample), "Hawkwise Prod Key")
	if err != nil {
		t.Fatalf("buildHawkwiseAIFlowInput: %v", err)
	}
	if in.Lat != 10.8 || in.Lng != 106.7 {
		t.Errorf("Lat/Lng = %v/%v, want the admin-mapped Sensor's 10.8/106.7, not Hawkwise's own reported coordinate", in.Lat, in.Lng)
	}
}

// A disabled Sensor mapping must NOT override the reported coordinate —
// disabling a mapping hides it from the map layer, it shouldn't silently
// redirect AI-flow events to a point the admin turned off.
func TestBuildHawkwiseAIFlowInputIgnoresDisabledSensorMapping(t *testing.T) {
	setupTestDB(t)
	if _, err := CreateSensor(Sensor{
		SourceUUID: "c8b1469d-bb77-46d1-b5d1-d61e8c41780e",
		Label:      "Disabled mapping",
		Lat:        10.8, Lon: 106.7,
		Enabled: false,
	}); err != nil {
		t.Fatalf("CreateSensor: %v", err)
	}

	in, err := buildHawkwiseAIFlowInput([]byte(abnormalStopSample), "Hawkwise Prod Key")
	if err != nil {
		t.Fatalf("buildHawkwiseAIFlowInput: %v", err)
	}
	if in.Lat != 21.0245 || in.Lng != 105.8412 {
		t.Errorf("Lat/Lng = %v/%v, want Hawkwise's own reported coordinate since the mapping is disabled", in.Lat, in.Lng)
	}
}
