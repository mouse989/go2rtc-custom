package trafficflow

// api.go — HTTP surface for the Hawkwise Advanced Traffic Density
// integration (see docs/hawkwise-flow-integration-plan.md):
//
//   POST /api/trafficdata/hawkwise/v1/flow-push — the external push
//                                            endpoint. Public path
//                                            (internal/auth's isPublicPath)
//                                            — authenticates itself via
//                                            X-API-Key (or, since Hawkwise's
//                                            own doc leaves the header
//                                            scheme up to the receiver and
//                                            real deployments have been
//                                            observed sending the latter,
//                                            "Authorization: Bearer <key>")
//                                            against internal/auth's
//                                            Integration store, never a
//                                            user session. Same pattern as
//                                            internal/aievent's OMNIA push
//                                            handler.
//   GET  /api/trafficflow/latest          — Map layer data source: the most
//                                            recent flow record for every
//                                            mapped+enabled Sensor. Normal
//                                            user auth + AllowMapTrafficFlow.
//   GET/POST/PUT/DELETE /api/trafficflow/sensors[/{id}] — admin-only Sensor
//                                            mapping CRUD.
//   GET  /api/trafficflow/observed-sensors — admin-only, read-only: every
//                                            source_uuid seen but not yet
//                                            mapped to a Sensor.
//
// There is no trafficflow-specific test-push tool: internal/aievent's
// POST /api/aievent/test-push is already fully generic (an admin-only
// "server performs an outbound HTTP POST to any URL" helper, unrelated to
// AI Event's own payload shape — see its own doc comment) and is reused
// as-is from the admin UI for testing this endpoint too.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/auth"
)

func RegisterHandlers() {
	http.HandleFunc("/api/trafficdata/hawkwise/v1/flow-push", handlePush)
	http.HandleFunc("/api/trafficflow/latest", handleLatest)
	http.HandleFunc("/api/trafficflow/sensors", handleSensors)
	http.HandleFunc("/api/trafficflow/sensors/", handleSensors)
	http.HandleFunc("/api/trafficflow/observed-sensors", handleObservedSensors)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

func writeError(w http.ResponseWriter, status int, code, message, field string) {
	writeJSON(w, status, struct {
		Status string    `json:"status"`
		Error  errorBody `json:"error"`
	}{Status: "error", Error: errorBody{Code: code, Message: message, Field: field}})
}

// ── POST /api/trafficdata/hawkwise/v1/flow-push ─────────────────────────

// maxPushBodyBytes bounds the whole request body — generous for a single
// camera cycle's JSON (well under 1MB even with every lane/vehicle-class
// field populated), same cap aievent uses for its own push endpoint.
const maxPushBodyBytes = 5 << 20

// hawkwisePush mirrors Hawkwise's real, already-live wire format field for
// field (general/evidence/details, snake_case) — see
// docs/hawkwise-flow-integration-plan.md §1.3/§2.3. This is deliberately
// NOT the flat camelCase shape from the earlier aspirational spec (doc 2);
// Hawkwise will not change what it sends to match us.
type hawkwisePush struct {
	General struct {
		TriggerCode   string `json:"trigger_code"`
		EventID       int64  `json:"event_id"`
		EventIDString string `json:"event_id_string"`
		SeverityLevel int    `json:"severity_level"`
		EventState    *int   `json:"event_state"`
		StartTime     string `json:"start_time"`
		EndTime       string `json:"end_time"`
		Camera        struct {
			SourceUUID   string   `json:"source_uuid"`
			HardwareUUID string   `json:"hardware_uuid"`
			Longitude    *float64 `json:"longitude"`
			Latitude     *float64 `json:"latitude"`
		} `json:"camera"`
	} `json:"general"`
	Evidence struct {
		FrameURL *string `json:"frame_url"`
	} `json:"evidence"`
	Details struct {
		TrafficStatus *string `json:"traffic_status"`
		DensityDetail struct {
			AvgOccupancy float64 `json:"avg_occupancy"`
			AvgSpeed     float64 `json:"avg_speed"`
			Total        int     `json:"total"`
			Data         []struct {
				Label    string  `json:"label"`
				Volume   int     `json:"volume"`
				AvgSpeed float64 `json:"avg_speed"`
			} `json:"data"`
			PartialData []struct {
				ID        int      `json:"id"`
				Occupancy float64  `json:"occupancy"`
				AvgSpeed  *float64 `json:"avg_speed"`
				Volume    int      `json:"volume"`
				Data      []struct {
					Label    string  `json:"label"`
					Volume   int     `json:"volume"`
					AvgSpeed float64 `json:"avg_speed"`
				} `json:"data"`
			} `json:"partial_data"`
		} `json:"density_detail"`
	} `json:"details"`
}

type pushResponse struct {
	Status     string `json:"status"`
	EventID    int64  `json:"event_id,omitempty"`
	ReceivedAt string `json:"received_at,omitempty"`
}

// validatePush checks only what docs/hawkwise-flow-integration-plan.md §2.4
// marks required for this module to function: event_id, source_uuid,
// start_time, end_time. Everything else is accepted whether present or
// not — including trigger_code values other than "AdvancedTrafficDensity"
// and vehicle-class labels this system has never seen before (per Hawkwise
// doc §5.4's explicit "tolerate unknown labels, never error" instruction).
func validatePush(req *hawkwisePush) *errorBody {
	if req.General.EventID == 0 {
		return &errorBody{Code: "missing_required_field", Message: "general.event_id is required", Field: "general.event_id"}
	}
	if req.General.Camera.SourceUUID == "" {
		return &errorBody{Code: "missing_required_field", Message: "general.camera.source_uuid is required", Field: "general.camera.source_uuid"}
	}
	if req.General.StartTime == "" {
		return &errorBody{Code: "missing_required_field", Message: "general.start_time is required", Field: "general.start_time"}
	}
	if _, err := parseHawkwiseTime(req.General.StartTime); err != nil {
		return &errorBody{Code: "invalid_field_value", Message: `general.start_time must be "yyyy-MM-dd HH:mm:ss" Vietnam local time`, Field: "general.start_time"}
	}
	if req.General.EndTime == "" {
		return &errorBody{Code: "missing_required_field", Message: "general.end_time is required", Field: "general.end_time"}
	}
	if _, err := parseHawkwiseTime(req.General.EndTime); err != nil {
		return &errorBody{Code: "invalid_field_value", Message: `general.end_time must be "yyyy-MM-dd HH:mm:ss" Vietnam local time`, Field: "general.end_time"}
	}
	return nil
}

// extractPushAPIKey reads the integration key from either X-API-Key or
// "Authorization: Bearer <key>" — Hawkwise's own doc explicitly leaves the
// header scheme up to the receiver ("Bên nhận tự chọn"), and a real
// deployment has been observed sending Bearer instead of X-API-Key, so both
// must work. X-API-Key takes priority if a caller somehow sends both.
func extractPushAPIKey(r *http.Request) string {
	if key := r.Header.Get("X-API-Key"); key != "" {
		return key
	}
	if authHeader := r.Header.Get("Authorization"); strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}
	return ""
}

func handlePush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only POST is supported", "")
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json", "")
		return
	}

	ip := auth.ClientIP(r)
	if locked, remaining := auth.IntegrationAuthLocked(ip); locked {
		w.Header().Set("Retry-After", strconv.Itoa(int(remaining.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many failed authentication attempts from this address, try again later", "")
		return
	}

	key := extractPushAPIKey(r)
	if key == "" {
		writeError(w, http.StatusUnauthorized, "missing_api_key", "X-API-Key (or Authorization: Bearer <key>) header is required", "")
		return
	}
	integration, ok := auth.ValidateIntegrationKey(r.URL.Path, key, ip)
	if !ok {
		if auth.RecordIntegrationAuthFailure(ip) {
			auth.RaiseIntegrationAuthFailedAlert(ip)
		}
		writeError(w, http.StatusUnauthorized, "invalid_api_key", "the provided API key is invalid or has been revoked", "")
		return
	}
	auth.RecordIntegrationAuthSuccess(ip)

	if allowed, retryAfterSec := checkPushRateLimit(integration.ID); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSec))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "rate limit exceeded for this integration", "")
		return
	}

	body := http.MaxBytesReader(w, r.Body, maxPushBodyBytes)
	var req hawkwisePush
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds the size limit", "")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error(), "")
		return
	}
	if apiErr := validatePush(&req); apiErr != nil {
		writeError(w, http.StatusBadRequest, apiErr.Code, apiErr.Message, apiErr.Field)
		return
	}

	dryRun := r.URL.Query().Get("dry_run") == "1"

	// Logged on every push, mapped or not, regardless of dry_run — this
	// registry's job is "what has Hawkwise ever sent", same as
	// internal/aievent's ObserveCategory.
	if !dryRun {
		observeSensor(req.General.Camera.SourceUUID, req.General.Camera.HardwareUUID,
			req.General.Camera.Latitude, req.General.Camera.Longitude)
	}

	if dryRun {
		action := "created"
		if Exists(req.General.EventID) {
			action = "duplicate"
		}
		writeJSON(w, http.StatusOK, struct {
			Status  string `json:"status"`
			Action  string `json:"action"`
			EventID int64  `json:"event_id"`
		}{Status: "ok", Action: action, EventID: req.General.EventID})
		return
	}

	rec := &FlowRecord{
		EventID:       req.General.EventID,
		EventIDString: req.General.EventIDString,
		SourceUUID:    req.General.Camera.SourceUUID,
		TriggerCode:   req.General.TriggerCode,
		SeverityLevel: req.General.SeverityLevel,
		EventState:    req.General.EventState,
		TrafficStatus: req.Details.TrafficStatus,
		AvgOccupancy:  req.Details.DensityDetail.AvgOccupancy,
		AvgSpeed:      req.Details.DensityDetail.AvgSpeed,
		Total:         req.Details.DensityDetail.Total,
		FrameURL:      req.Evidence.FrameURL,
		ReportedLat:   req.General.Camera.Latitude,
		ReportedLng:   req.General.Camera.Longitude,
	}
	if t, err := parseHawkwiseTime(req.General.StartTime); err == nil {
		rec.StartTime = t
	}
	if t, err := parseHawkwiseTime(req.General.EndTime); err == nil {
		rec.EndTime = t
	}
	for _, d := range req.Details.DensityDetail.Data {
		rec.VehicleCounts = append(rec.VehicleCounts, VehicleCount{Label: d.Label, Volume: d.Volume, AvgSpeed: d.AvgSpeed})
	}
	for _, p := range req.Details.DensityDetail.PartialData {
		lane := LaneFlow{ID: p.ID, Occupancy: p.Occupancy, AvgSpeed: p.AvgSpeed, Volume: p.Volume}
		for _, d := range p.Data {
			lane.Data = append(lane.Data, VehicleCount{Label: d.Label, Volume: d.Volume, AvgSpeed: d.AvgSpeed})
		}
		rec.Lanes = append(rec.Lanes, lane)
	}

	if _, err := Ingest(rec); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to store flow record", "")
		return
	}

	// Response body is for internal debug only — Hawkwise never reads it,
	// only the 2xx status code (see plan §1.5).
	writeJSON(w, http.StatusOK, pushResponse{Status: "ok", EventID: req.General.EventID, ReceivedAt: time.Now().Format(time.RFC3339)})
}

// ── Per-integration push rate limit ──────────────────────────────────────
// Sized for ~500 cameras pushing roughly every 5 minutes — a baseline of
// ~100 req/min, with generous headroom for cycles clustering near each
// 5-minute boundary. Much higher than internal/aievent's 60/min (that
// endpoint is designed for sparse, irregular events, not a steady
// per-camera time series) — see docs/hawkwise-flow-integration-plan.md §4.2.
const pushRateLimitPerMinute = 900

var (
	rateMu    sync.Mutex
	rateState = map[string]*rateWindow{} // integration ID -> window
)

type rateWindow struct {
	start time.Time
	count int
}

func checkPushRateLimit(integrationID string) (allowed bool, retryAfterSec int) {
	rateMu.Lock()
	defer rateMu.Unlock()
	now := time.Now()
	w := rateState[integrationID]
	if w == nil || now.Sub(w.start) >= time.Minute {
		w = &rateWindow{start: now}
		rateState[integrationID] = w
	}
	w.count++
	if w.count > pushRateLimitPerMinute {
		retryAfterSec = int((time.Minute - now.Sub(w.start)).Seconds()) + 1
		return false, retryAfterSec
	}
	return true, 0
}

// ── GET /api/trafficflow/latest ──────────────────────────────────────────

type latestOut struct {
	SensorID      string         `json:"sensor_id"`
	Label         string         `json:"label"`
	Lat           float64        `json:"lat"`
	Lon           float64        `json:"lon"`
	Total         int            `json:"total"`
	TrafficStatus *string        `json:"traffic_status,omitempty"`
	AvgSpeed      float64        `json:"avg_speed"`
	AvgOccupancy  float64        `json:"avg_occupancy"`
	ReceivedAt    string         `json:"received_at"`
	VehicleCounts []VehicleCount `json:"vehicle_counts,omitempty"`
}

func requireTrafficFlowViewer(w http.ResponseWriter, r *http.Request) *auth.User {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil
	}
	if user.Role != auth.RoleAdmin && !user.AllowMapTrafficFlow {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil
	}
	return user
}

func handleLatest(w http.ResponseWriter, r *http.Request) {
	if requireTrafficFlowViewer(w, r) == nil {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	out := make([]latestOut, 0, 64)
	for _, sn := range ListSensors() {
		if !sn.Enabled {
			continue
		}
		rec, ok := GetLatestForSensor(sn.SourceUUID)
		if !ok {
			continue
		}
		out = append(out, latestOut{
			SensorID: sn.ID, Label: sn.Label, Lat: sn.Lat, Lon: sn.Lon,
			Total: rec.Total, TrafficStatus: rec.TrafficStatus,
			AvgSpeed: rec.AvgSpeed, AvgOccupancy: rec.AvgOccupancy,
			ReceivedAt:    rec.ReceivedAt.Format(time.RFC3339),
			VehicleCounts: rec.VehicleCounts,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// ── Sensor mapping (admin-only CRUD) + observed sensors (admin-only, read-only) ──

func requireTrafficFlowAdmin(w http.ResponseWriter, r *http.Request) bool {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user.Role != auth.RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

type sensorReq struct {
	SourceUUID   string  `json:"source_uuid"`
	HardwareUUID string  `json:"hardware_uuid"`
	Label        string  `json:"label"`
	Lat          float64 `json:"lat"`
	Lon          float64 `json:"lon"`
	Enabled      bool    `json:"enabled"`
}

func handleSensors(w http.ResponseWriter, r *http.Request) {
	if !requireTrafficFlowAdmin(w, r) {
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/trafficflow/sensors"), "/")

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, ListSensors())

	case http.MethodPost:
		var req sensorReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sn, err := CreateSensor(Sensor{
			SourceUUID: req.SourceUUID, HardwareUUID: req.HardwareUUID,
			Label: req.Label, Lat: req.Lat, Lon: req.Lon, Enabled: req.Enabled,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, sn)

	case http.MethodPut:
		if id == "" {
			http.Error(w, "sensor id required in path", http.StatusBadRequest)
			return
		}
		var req sensorReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sn, err := UpdateSensor(id, Sensor{
			SourceUUID: req.SourceUUID, HardwareUUID: req.HardwareUUID,
			Label: req.Label, Lat: req.Lat, Lon: req.Lon, Enabled: req.Enabled,
		})
		if err != nil {
			status := http.StatusBadRequest
			if err == errTrafficFlowNotFound {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, http.StatusOK, sn)

	case http.MethodDelete:
		if id == "" {
			http.Error(w, "sensor id required", http.StatusBadRequest)
			return
		}
		if err := DeleteSensor(id); err != nil {
			status := http.StatusInternalServerError
			if err == errTrafficFlowNotFound {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleObservedSensors(w http.ResponseWriter, r *http.Request) {
	if !requireTrafficFlowAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, ListObservedSensors())
}
