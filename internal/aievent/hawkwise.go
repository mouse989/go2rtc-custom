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
	"math"
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
// layer OMNIA's events use. The natural dedup key is EventIDString (falls
// back to the numeric EventID when Hawkwise omits the UUID) — each push
// already carries both its own start and end time, so there's no
// OMNIA-style event.created/event.terminated pair to track.
//
// That natural key is only a good dedup key for a genuinely one-shot
// detection (e.g. AbnormalStop). It falls apart for a condition Hawkwise's
// AI model keeps re-evaluating and re-reporting while it persists (e.g.
// ongoing congestion): Hawkwise has no native "this push continues an
// earlier situation" signal at all — no situationId, no update/terminate
// message — so each re-detection gets its own fresh EventIDString even
// though it describes the same physical situation. Ingested verbatim,
// that produces a pile of near-duplicate AIEvents at the same spot, all
// independently "active" within the map's display window — confirmed from
// a real report of exactly this (several "Đông xe" markers/previews
// stacked at one intersection).
//
// Since Hawkwise gives us nothing authoritative to group by, this merges
// heuristically instead (see findRecentHawkwiseSituation): a push whose
// (ai_flow_code, camera source_uuid, coordinate) matches a still-fresh
// existing Hawkwise record reuses THAT record's RecordID rather than
// minting a new one, so Ingest's normal update-in-place path (same as
// OMNIA's recordId re-use) replaces its content — latest wins, one marker
// per situation, instead of accumulating one per push.
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
	if merged, ok := findRecentHawkwiseSituation(in.AIFlowCode, in.SourceUUID, in.Lat, in.Lng, time.Now()); ok {
		recordID = merged
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
		CameraSourceUUID:    in.SourceUUID,
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

// hawkwiseMergeWindow is a SLIDING window measured against a candidate
// record's own UpdatedAt (its most recent push, not its original
// ReceivedAt) — so a situation Hawkwise keeps re-reporting every few
// minutes for hours stays merged into the same one record indefinitely,
// as long as consecutive pushes never go quiet for longer than this; once
// a gap exceeds it, the next push correctly starts a fresh record rather
// than reviving a stale one. Comfortably larger than the ~5-minute push
// cadence Hawkwise is known to use for a persisting condition, without
// being so large it risks bridging two genuinely separate occurrences.
const hawkwiseMergeWindow = 15 * time.Minute

// hawkwiseMergeRadiusM bounds the coordinate check — generous enough to
// absorb GPS/rounding jitter from what is physically the same fixed
// camera, tight enough that two different intersections never merge just
// because they happen to share an ai_flow_code. In practice this is a
// safety net on top of the CameraSourceUUID match below, which should
// already guarantee near-identical coordinates for the same camera.
const hawkwiseMergeRadiusM = 100.0

// findRecentHawkwiseSituation looks for the most recent still-fresh
// Hawkwise AIEvent from the same camera + ai_flow_code + nearby coordinate
// as a just-received push, so IngestHawkwiseAIFlow can reuse its RecordID
// instead of minting a new one (see that function's doc comment for why).
// sourceUUID == "" (Hawkwise omitted it) never merges — there is nothing
// stable to match on, and merging by ai_flow_code + coordinate alone risks
// conflating two different cameras pointed at the same intersection.
//
// Not atomic with the Ingest() call that follows a match: two pushes for
// the same camera arriving within the same instant could both miss each
// other here and each create their own record. Accepted as a narrow,
// low-likelihood race (one external system posting at most a few times a
// minute) rather than adding a second exported lock around both calls.
func findRecentHawkwiseSituation(aiFlowCode int, sourceUUID string, lat, lng float64, now time.Time) (recordID string, found bool) {
	if sourceUUID == "" {
		return "", false
	}
	from := now.Add(-hawkwiseMergeWindow)

	mu.Lock()
	defer mu.Unlock()

	dayKeys := map[string]bool{dayKeyFor(now): true, dayKeyFor(from): true}
	var best *AIEvent
	for dk := range dayKeys {
		if err := loadDayLocked(dk); err != nil {
			continue
		}
		for _, ev := range days[dk] {
			if ev.Status != StatusActive || ev.CategorySource != CategorySourceHawkwise {
				continue
			}
			if ev.CategoryTypeID != aiFlowCode || ev.CameraSourceUUID != sourceUUID {
				continue
			}
			if ev.UpdatedAt.Before(from) {
				continue // this chain has gone quiet too long — let a new one start
			}
			if haversineMeters(ev.Lat, ev.Lng, lat, lng) > hawkwiseMergeRadiusM {
				continue
			}
			if best == nil || ev.UpdatedAt.After(best.UpdatedAt) {
				best = ev
			}
		}
	}
	if best == nil {
		return "", false
	}
	return best.RecordID, true
}

// haversineMeters is a local copy of the same formula
// internal/traffic/monitor.go's unexported haversine uses — kept
// self-contained rather than importing internal/traffic (an unrelated
// module) for 10 lines of math.
func haversineMeters(lat1, lng1, lat2, lng2 float64) float64 {
	const earthRadiusM = 6371000.0
	toRad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat := toRad(lat2 - lat1)
	dLng := toRad(lng2 - lng1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(toRad(lat1))*math.Cos(toRad(lat2))*math.Sin(dLng/2)*math.Sin(dLng/2)
	return earthRadiusM * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
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
