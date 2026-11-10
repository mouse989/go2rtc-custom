package trafficflow

// hawkwise_ai_flow.go — handles the OTHER kind of message Hawkwise sends to
// POST /api/trafficdata/hawkwise/v1/flow-push: discrete AI-detected events
// (general.trigger_code != "AdvancedTrafficDensity", e.g. "AbnormalStop")
// rather than periodic vehicle-counting data. Before this file existed,
// such a message still passed handlePush's validation (it has the same
// event_id/source_uuid/start_time/end_time) and got stored as a FlowRecord
// with every count field at zero — the actually useful data (license
// plate, vehicle brand/color, evidence images) has no matching struct
// field on hawkwisePush and was silently dropped by encoding/json. See
// docs/hawkwise-ai-flow-events-plan.md §1 for the full diagnosis.
//
// This package still owns parsing the raw Hawkwise envelope (it already
// has the push handler, the body bytes, and the Sensor-mapping lookup) —
// it just hands the result to internal/aievent, which has the "discrete
// AI event with a category, a map marker, and evidence images" storage
// and display pipeline already built and running for OMNIA. See
// internal/aievent/hawkwise.go for the other half of this adapter.

import (
	"encoding/json"
	"fmt"

	"github.com/AlexxIT/go2rtc/internal/aievent"
)

// hawkwiseAIFlowPush mirrors the confirmed-stable subset of Hawkwise's
// AI-flow wire format. Unlike hawkwisePush, Details is left as raw JSON:
// this message's "details" shape (vehicle/license-plate recognition
// fields) isn't fully documented yet and doesn't need individually-typed
// access — see aievent.HawkwiseAIFlowInput.Details's doc comment. Fields
// whose meaning is still unconfirmed (general.event_type, object_action,
// event_state, occurred_at) are deliberately left off this struct too —
// they're still visible to an admin via the raw sample this ingest path
// hands to aievent.ObserveCategory, without this code guessing at what
// they mean.
type hawkwiseAIFlowPush struct {
	General struct {
		TriggerCode    string  `json:"trigger_code"`
		EventID        int64   `json:"event_id"`
		EventIDString  string  `json:"event_id_string"`
		StartTime      string  `json:"start_time"`
		EndTime        string  `json:"end_time"`
		AIFlowCode     int     `json:"ai_flow_code"`
		AIFlowName     string  `json:"ai_flow_name"`
		ConfidentScore float64 `json:"confident_score"`
		Camera         struct {
			SourceUUID   string   `json:"source_uuid"`
			HardwareUUID string   `json:"hardware_uuid"`
			HardwareName *string  `json:"hardware_name"`
			Address      *string  `json:"address"`
			Longitude    *float64 `json:"longitude"`
			Latitude     *float64 `json:"latitude"`
		} `json:"camera"`
	} `json:"general"`
	Evidence struct {
		FrameURL          *string `json:"frame_url"`
		BodyCropURL       *string `json:"body_crop_url"`
		IdentifiedCropURL *string `json:"identified_crop_url"`
	} `json:"evidence"`
	Details json.RawMessage `json:"details"`
}

// buildHawkwiseAIFlowInput decodes bodyBytes as an AI-flow message and maps
// it onto aievent.HawkwiseAIFlowInput — kept separate from
// handleHawkwiseAIFlowPush (which also calls aievent.IngestHawkwiseAIFlow,
// a real store write) so this field-mapping logic — including the
// Sensor-mapping-overrides-reported-coordinate rule — can be unit tested
// without touching aievent's package-level store state at all.
func buildHawkwiseAIFlowInput(bodyBytes []byte, integrationName string) (aievent.HawkwiseAIFlowInput, error) {
	var req hawkwiseAIFlowPush
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		return aievent.HawkwiseAIFlowInput{}, fmt.Errorf("decode AI-flow push: %w", err)
	}

	startTime, _ := parseHawkwiseTime(req.General.StartTime)
	endTime, _ := parseHawkwiseTime(req.General.EndTime)

	// Prefer an admin-confirmed Sensor mapping's coordinate over Hawkwise's
	// own reported camera.{latitude,longitude} — same "don't auto-trust the
	// vendor's per-push coordinate" stance the density/pill map layer
	// already takes (see sensors.go's doc comment), so an AI-flow event
	// from a camera the admin has already placed on the map shows up at
	// the same point its density pill does.
	lat, lng := 0.0, 0.0
	if req.General.Camera.Latitude != nil {
		lat = *req.General.Camera.Latitude
	}
	if req.General.Camera.Longitude != nil {
		lng = *req.General.Camera.Longitude
	}
	if sn, ok := GetSensorBySourceUUID(req.General.Camera.SourceUUID); ok && sn.Enabled {
		lat, lng = sn.Lat, sn.Lon
	}

	hardwareName := ""
	if req.General.Camera.HardwareName != nil {
		hardwareName = *req.General.Camera.HardwareName
	}
	address := ""
	if req.General.Camera.Address != nil {
		address = *req.General.Camera.Address
	}
	cameraLabel := hardwareName
	if address != "" {
		if cameraLabel != "" {
			cameraLabel += " — "
		}
		cameraLabel += address
	}
	if cameraLabel == "" {
		cameraLabel = req.General.Camera.SourceUUID
	}

	frameURL, bodyCropURL, identifiedCropURL := "", "", ""
	if req.Evidence.FrameURL != nil {
		frameURL = *req.Evidence.FrameURL
	}
	if req.Evidence.BodyCropURL != nil {
		bodyCropURL = *req.Evidence.BodyCropURL
	}
	if req.Evidence.IdentifiedCropURL != nil {
		identifiedCropURL = *req.Evidence.IdentifiedCropURL
	}

	return aievent.HawkwiseAIFlowInput{
		TriggerCode:       req.General.TriggerCode,
		EventID:           req.General.EventID,
		EventIDString:     req.General.EventIDString,
		AIFlowCode:        req.General.AIFlowCode,
		AIFlowName:        req.General.AIFlowName,
		ConfidentScore:    req.General.ConfidentScore,
		StartTime:         startTime,
		EndTime:           endTime,
		SourceUUID:        req.General.Camera.SourceUUID,
		CameraLabel:       cameraLabel,
		Lat:               lat,
		Lng:               lng,
		FrameURL:          frameURL,
		BodyCropURL:       bodyCropURL,
		IdentifiedCropURL: identifiedCropURL,
		Details:           req.Details,
		RawBody:           string(bodyBytes),
		IntegrationName:   integrationName,
	}, nil
}

// handleHawkwiseAIFlowPush is the real call site handlePush uses: parse,
// then store. See buildHawkwiseAIFlowInput's doc comment for why these are
// two functions.
func handleHawkwiseAIFlowPush(bodyBytes []byte, integrationName string) error {
	in, err := buildHawkwiseAIFlowInput(bodyBytes, integrationName)
	if err != nil {
		return err
	}
	_, err = aievent.IngestHawkwiseAIFlow(in)
	return err
}
