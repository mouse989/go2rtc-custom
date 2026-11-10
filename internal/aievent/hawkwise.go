package aievent

// hawkwise.go — adapter that turns one Hawkwise "AI-flow" push (a discrete
// AI-detected event such as trigger_code "AbnormalStop" — NOT the periodic
// "AdvancedTrafficDensity" counting messages internal/trafficflow handles)
// into an AIEvent, reusing this package's storage/display pipeline instead
// of a third parallel subsystem. See
// docs/hawkwise-ai-flow-events-plan.md for the full design discussion.
//
// internal/trafficflow owns the HTTP endpoint, auth, and envelope
// parsing (it already has the X-API-Key/Integration machinery and decides,
// by general.trigger_code, whether a push is density data or an AI-flow
// event) — this file is only called once that decision is already made,
// and only does the AI-flow half of the work. internal/trafficflow imports
// this package; this package never imports internal/trafficflow back.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HawkwiseAIFlowInput is everything internal/trafficflow's push handler has
// already parsed/resolved from one AI-flow push, handed over for ingestion.
type HawkwiseAIFlowInput struct {
	TriggerCode    string // general.trigger_code, e.g. "AbnormalStop"
	EventID        int64  // general.event_id — Hawkwise's own dedup key
	EventIDString  string // general.event_id_string (UUID) — preferred RecordID source, falls back to EventID
	AIFlowCode     int    // general.ai_flow_code — the CategoryRule key for this source (see categories.go)
	AIFlowName     string // general.ai_flow_name, e.g. "Abnormal Stop"
	ConfidentScore float64

	StartTime time.Time
	EndTime   time.Time

	SourceUUID  string // general.camera.source_uuid
	CameraLabel string // display text composed by the caller from camera.{hardware_name,address,source_name}
	Lat, Lng    float64

	// FrameURL/BodyCropURL/IdentifiedCropURL are evidence.* image URLs,
	// reachable only from this server's own LAN (confirmed by the user —
	// see the plan doc §4.2) — never embedded directly in a page a
	// browser loads. Ingest fetches them itself and stores the bytes
	// through the same images.go pipeline OMNIA's base64 attachments use.
	FrameURL          string
	BodyCropURL       string
	IdentifiedCropURL string

	// Details is the push's raw "details" object, kept byte-for-byte (not
	// parsed into named fields — see AIEvent.VehicleDetail's doc comment).
	Details json.RawMessage

	// RawBody is the full original push body — kept as a sample on the
	// ObservedCategory entry for this (AIFlowCode) so an admin can inspect
	// every field Hawkwise actually sends, including the ones this adapter
	// doesn't interpret yet (see categories.go's SampleRawJSON doc).
	RawBody string

	IntegrationName string // the Integration record's display name, for AIEvent.Source
}

// hawkwiseVehicleSummary picks out the handful of Details fields whose
// meaning is unambiguous (a license plate reads the same regardless of
// which other fields in this payload are still unconfirmed — see the plan
// doc §5) to build a short, human-readable line for AIEvent.Description —
// the map popup's one-line summary. Everything else in Details stays
// available verbatim via AIEvent.VehicleDetail for whoever opens it.
type hawkwiseVehicleSummary struct {
	Name      string `json:"name"` // license plate
	BrandCode string `json:"brand_code"`
	ColorCode string `json:"color_code"`
	Type      string `json:"type"`
}

func describeVehicle(details json.RawMessage) string {
	if len(details) == 0 {
		return ""
	}
	var v hawkwiseVehicleSummary
	if err := json.Unmarshal(details, &v); err != nil {
		return ""
	}
	parts := make([]string, 0, 4)
	if v.Name != "" {
		parts = append(parts, "Biển số "+v.Name)
	}
	if v.BrandCode != "" {
		parts = append(parts, v.BrandCode)
	}
	if v.ColorCode != "" {
		parts = append(parts, v.ColorCode)
	}
	if v.Type != "" {
		parts = append(parts, v.Type)
	}
	return strings.Join(parts, " · ")
}

// IngestHawkwiseAIFlow stores in as an AIEvent — same store, dedup, and map
// layer OMNIA's events use. Dedup key is EventIDString (falls back to the
// numeric EventID when Hawkwise omits the UUID), which, per the plan doc's
// own conclusion, is always present and never needs a separate "is this an
// update to an already-open event" decision the way OMNIA's
// event.created/event.terminated pair does — each Hawkwise AI-flow push
// already carries both its own start and end time.
func IngestHawkwiseAIFlow(in HawkwiseAIFlowInput) (created bool, err error) {
	// Logged on every push, mapped or not — this registry's job is "what has
	// Hawkwise's AI-flow side ever sent", same spirit as ObserveCategory's
	// use for OMNIA, with the raw body kept too (CategorySourceOMNIA never
	// needs this; its schema is already fully documented).
	ObserveCategory(CategorySourceHawkwise, in.AIFlowCode, 0, in.TriggerCode, in.AIFlowName, in.RawBody)

	recordID := "hawkwise:" + in.EventIDString
	if in.EventIDString == "" {
		recordID = fmt.Sprintf("hawkwise:%d", in.EventID)
	}

	ev := &AIEvent{
		RecordID:            recordID,
		RecordType:          in.TriggerCode,
		EventCode:           in.EventIDString,
		Source:              in.IntegrationName,
		CategorySource:      CategorySourceHawkwise,
		CategoryTypeID:      in.AIFlowCode,
		CategoryDescription: in.AIFlowName,
		Description:         describeVehicle(in.Details),
		SourceProcessor:     in.CameraLabel,
		StartTime:           in.StartTime,
		EndTime:             in.EndTime,
		Lat:                 in.Lat,
		Lng:                 in.Lng,
		VehicleDetail:       in.Details,
	}

	var attachments []Attachment
	if a, ok := fetchHawkwiseImage(in.FrameURL, "frame"); ok {
		attachments = append(attachments, a)
	}
	if a, ok := fetchHawkwiseImage(in.BodyCropURL, "body_crop"); ok {
		attachments = append(attachments, a)
	}
	if a, ok := fetchHawkwiseImage(in.IdentifiedCropURL, "identified_crop"); ok {
		attachments = append(attachments, a)
	}
	paths, skipped := ReplaceImages(recordID, attachments)
	ev.ImagePaths = paths
	if skipped > 0 {
		log.Warn().Str("recordId", recordID).Int("skipped", skipped).Msg("[aievent] some Hawkwise evidence images were invalid and were not stored")
	}

	return Ingest(ev, false)
}

// hawkwiseImageFetchTimeout bounds each evidence-image GET — these URLs are
// only reachable on this server's own LAN (confirmed with the user), so a
// slow/unreachable NVR shouldn't be able to stall ingestion of the event
// itself; the event is still stored even if every image fetch fails.
const hawkwiseImageFetchTimeout = 8 * time.Second

var hawkwiseImageClient = &http.Client{Timeout: hawkwiseImageFetchTimeout}

// fetchHawkwiseImage GETs url (a LAN-only Hawkwise evidence link) and
// returns it as an Attachment ready for ReplaceImages — reusing that
// function's existing base64-decode/size-cap/magic-byte-sniff validation
// rather than duplicating it, by base64-encoding what was just downloaded.
// A failed or oversized fetch is logged and skipped, never fatal to the
// event itself — Hawkwise doesn't guarantee these URLs stay valid for long
// (see the plan doc §4.2), so a missing image here is an expected,
// tolerated outcome, not a bug.
func fetchHawkwiseImage(url, name string) (Attachment, bool) {
	if url == "" {
		return Attachment{}, false
	}
	resp, err := hawkwiseImageClient.Get(url)
	if err != nil {
		log.Warn().Err(err).Str("url", url).Msg("[aievent] Hawkwise evidence image fetch failed (continuing without it)")
		return Attachment{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Warn().Int("status", resp.StatusCode).Str("url", url).Msg("[aievent] Hawkwise evidence image fetch returned non-200 (continuing without it)")
		return Attachment{}, false
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maxImageBytes {
		return Attachment{}, false
	}
	return Attachment{Name: name, Content: base64.StdEncoding.EncodeToString(raw)}, true
}
