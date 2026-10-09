package aievent

// api.go — HTTP surface for the AI Event module:
//
//   POST /api/aievent/omnia/v1/push       — the external push endpoint (see
//                                            docs/omnia-integration-api-spec.md).
//                                            Public path (internal/auth's
//                                            isPublicPath) — authenticates
//                                            itself via X-API-Key against
//                                            internal/auth's Integration
//                                            store, never a user session.
//   GET  /api/aievent/active              — Map layer data source. Normal
//                                            user auth + AllowMapAIEvents.
//   GET  /api/aievent/image?path=...      — attachment image bytes. Normal
//                                            user auth + AllowMapAIEvents;
//                                            also gated behind a verified
//                                            device per-user via the
//                                            "ai_event_image" device-binding
//                                            scope (internal/auth/device_binding.go),
//                                            enforced automatically by
//                                            Middleware matching this path
//                                            prefix — no extra code needed here.
//   GET/POST/PUT/DELETE /api/aievent/category-rules[/{id}] — admin-only.
//   GET  /api/aievent/observed-categories — admin-only, read-only.
//   POST /api/aievent/test-push            — admin-only, see testpush.go.
//
// Every handler re-checks permission itself rather than trusting that a
// request reached it at all — same "never trust the outer gate alone"
// convention internal/incidents and internal/auth/api_ptz.go already use.

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
	http.HandleFunc("/api/aievent/omnia/v1/push", handleOmniaPush)
	http.HandleFunc("/api/aievent/active", handleActive)
	http.HandleFunc("/api/aievent/image", handleImage)
	http.HandleFunc("/api/aievent/category-rules", handleCategoryRules)
	http.HandleFunc("/api/aievent/category-rules/", handleCategoryRules)
	http.HandleFunc("/api/aievent/observed-categories", handleObservedCategories)
	http.HandleFunc("/api/aievent/test-push", handleTestPush)
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

// ── POST /api/aievent/omnia/v1/push ─────────────────────────────────────

// maxPushBodyBytes bounds the whole request body — matches
// docs/omnia-integration-api-spec.md §9 (25 MB).
const maxPushBodyBytes = 25 << 20

// pushRequest mirrors the OMNIA payload field-for-field, including its
// camelCase-outer/PascalCase-inner-"event" inconsistency — see the API
// spec's §5.2 note. Fields this module never uses (Classification, Status,
// CreatorUserName, IncidentAttributes, ...) are simply omitted here;
// encoding/json ignores JSON fields with no matching Go field.
type pushRequest struct {
	Type        string `json:"type"`
	PublishedAt string `json:"publishedAt"`
	RecordID    string `json:"recordId"`
	SituationID string `json:"situationId"`
	RecordType  string `json:"recordType"`
	EventCode   string `json:"eventCode"`
	Category    struct {
		TypeID      int    `json:"typeId"`
		SubtypeID   int    `json:"subtypeId"`
		Description string `json:"description"`
		Rule        string `json:"rule"`
	} `json:"category"`
	Event struct {
		Description string `json:"Description"`
		StartTime   struct {
			Value string `json:"Value"`
		} `json:"StartTime"`
		EndTime *struct {
			Value string `json:"Value"`
		} `json:"EndTime"`
		Source struct {
			Processor string `json:"Processor"`
		} `json:"Source"`
		Location *struct {
			X float64 `json:"X"`
			Y float64 `json:"Y"`
		} `json:"Location"`
		Attachments []struct {
			Name    string `json:"Name"`
			Content string `json:"Content"`
		} `json:"Attachments"`
	} `json:"event"`
}

type pushResponse struct {
	Status   string `json:"status"`
	Action   string `json:"action,omitempty"`
	RecordID string `json:"recordId,omitempty"`
	ID       string `json:"id,omitempty"`
}

// validatePush checks only what docs/omnia-integration-api-spec.md §6
// marks "Có" (required) for this module to function: recordId, a valid
// type, category.typeId, and a parseable event.StartTime.Value. Everything
// else is "Khuyến nghị"/"Không" — accepted whether present or not.
func validatePush(req *pushRequest) *errorBody {
	if req.RecordID == "" {
		return &errorBody{Code: "missing_required_field", Message: "recordId is required", Field: "recordId"}
	}
	if req.Type != "event.created" && req.Type != "event.terminated" {
		return &errorBody{Code: "invalid_field_value", Message: `type must be "event.created" or "event.terminated"`, Field: "type"}
	}
	if req.Category.TypeID == 0 {
		return &errorBody{Code: "missing_required_field", Message: "category.typeId is required", Field: "category.typeId"}
	}
	if req.Event.StartTime.Value == "" {
		return &errorBody{Code: "missing_required_field", Message: "event.StartTime.Value is required", Field: "event.StartTime.Value"}
	}
	if _, err := time.Parse(time.RFC3339, req.Event.StartTime.Value); err != nil {
		return &errorBody{Code: "invalid_field_value", Message: "event.StartTime.Value must be RFC3339 (e.g. 2026-09-25T03:48:54Z)", Field: "event.StartTime.Value"}
	}
	return nil
}

func handleOmniaPush(w http.ResponseWriter, r *http.Request) {
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

	key := r.Header.Get("X-API-Key")
	if key == "" {
		writeError(w, http.StatusUnauthorized, "missing_api_key", "X-API-Key header is required", "")
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
	var req pushRequest
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

	terminated := req.Type == "event.terminated"
	dryRun := r.URL.Query().Get("dry_run") == "1"

	if !dryRun {
		// Logged for both created and terminated pushes, and even when a
		// CategoryRule already matches it — this registry's job is "what
		// has the source ever sent", independent of whether it's mapped.
		ObserveCategory(req.Category.TypeID, req.Category.SubtypeID, req.RecordType, req.Category.Description)
	}

	if dryRun {
		action := "created"
		if Exists(req.RecordID) {
			action = "updated"
		}
		if terminated {
			action = "terminated"
		}
		writeJSON(w, http.StatusOK, pushResponse{Status: "ok", Action: action, RecordID: req.RecordID, ID: req.RecordID})
		return
	}

	ev := &AIEvent{
		RecordID:            req.RecordID,
		SituationID:         req.SituationID,
		RecordType:          req.RecordType,
		EventCode:           req.EventCode,
		Source:              integration.Name,
		CategoryTypeID:      req.Category.TypeID,
		CategorySubtypeID:   req.Category.SubtypeID,
		CategoryDescription: req.Category.Description,
		CategoryRule:        req.Category.Rule,
		Description:         req.Event.Description,
		SourceProcessor:     req.Event.Source.Processor,
	}
	if t, err := time.Parse(time.RFC3339, req.Event.StartTime.Value); err == nil {
		ev.StartTime = t
	}
	if req.Event.EndTime != nil {
		if t, err := time.Parse(time.RFC3339, req.Event.EndTime.Value); err == nil {
			ev.EndTime = t
		}
	}
	if req.Event.Location != nil {
		ev.Lat = req.Event.Location.Y
		ev.Lng = req.Event.Location.X
	}

	attachments := make([]Attachment, 0, len(req.Event.Attachments))
	for _, a := range req.Event.Attachments {
		attachments = append(attachments, Attachment{Name: a.Name, Content: a.Content})
	}
	// Always call ReplaceImages, even with zero attachments — an empty (or
	// omitted) Attachments list on an update intentionally clears whatever
	// images the previous push stored, per the API spec's "replace, never
	// accumulate" rule.
	paths, skipped := ReplaceImages(req.RecordID, attachments)
	ev.ImagePaths = paths
	if skipped > 0 {
		log.Warn().Str("recordId", req.RecordID).Int("skipped", skipped).Msg("[aievent] some attachments were invalid and were not stored")
	}

	created, err := Ingest(ev, terminated)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to store event", "")
		return
	}

	action := "updated"
	if created {
		action = "created"
	}
	if terminated {
		action = "terminated"
	}
	writeJSON(w, http.StatusOK, pushResponse{Status: "ok", Action: action, RecordID: req.RecordID, ID: req.RecordID})
}

// ── Per-integration push rate limit (60/min, fixed window) ──────────────
// A simple fixed window, not a proper leaky bucket — same "lightweight,
// explainable" tradeoff internal/auth/security_alerts.go's own doc comment
// states for its anomaly checks; the documented tolerance (60/min against
// an expected few pushes per 2-5 minutes) makes the boundary-burst
// imprecision a fixed window has irrelevant in practice.
const pushRateLimitPerMinute = 60

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

// ── GET /api/aievent/active ──────────────────────────────────────────────

type activeEventOut struct {
	RecordID        string   `json:"record_id"`
	CategoryLabel   string   `json:"category_label"`
	Icon            string   `json:"icon"` // "" → generic fallback marker (www/map.html)
	Description     string   `json:"description,omitempty"`
	SourceProcessor string   `json:"source_processor,omitempty"`
	Lat             float64  `json:"lat"`
	Lng             float64  `json:"lng"`
	ReceivedAt      string   `json:"received_at"`
	ImagePaths      []string `json:"image_paths,omitempty"`
}

func requireAIEventViewer(w http.ResponseWriter, r *http.Request) *auth.User {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil
	}
	if user.Role != auth.RoleAdmin && !user.AllowMapAIEvents {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil
	}
	return user
}

func handleActive(w http.ResponseWriter, r *http.Request) {
	if requireAIEventViewer(w, r) == nil {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	windowMin := auth.GetSettings().AIEventWindowMinutes
	list, err := ListActive(windowMin)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	out := make([]activeEventOut, 0, len(list))
	for _, ev := range list {
		label, icon, matched := ResolveIcon(ev.CategoryTypeID, ev.CategorySubtypeID)
		if !matched {
			label = ev.CategoryDescription
			if label == "" {
				label = ev.RecordType
			}
			icon = ""
		}
		out = append(out, activeEventOut{
			RecordID: ev.RecordID, CategoryLabel: label, Icon: icon,
			Description: ev.Description, SourceProcessor: ev.SourceProcessor,
			Lat: ev.Lat, Lng: ev.Lng,
			ReceivedAt: ev.ReceivedAt.Format(time.RFC3339),
			ImagePaths: ev.ImagePaths,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// ── GET /api/aievent/image ───────────────────────────────────────────────

func handleImage(w http.ResponseWriter, r *http.Request) {
	if requireAIEventViewer(w, r) == nil {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	full, ok := ImageFilePath(rel)
	if !ok {
		http.Error(w, "not found (expired or never existed)", http.StatusNotFound)
		return
	}
	http.ServeFile(w, r, full)
}

// ── Category rules (admin-only CRUD) + observed categories (admin-only, read-only) ──

func requireAIEventAdmin(w http.ResponseWriter, r *http.Request) bool {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user.Role != auth.RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

type categoryRuleReq struct {
	TypeID    int    `json:"type_id"`
	SubtypeID *int   `json:"subtype_id"`
	Label     string `json:"label"`
	Icon      string `json:"icon"`
}

func handleCategoryRules(w http.ResponseWriter, r *http.Request) {
	if !requireAIEventAdmin(w, r) {
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/aievent/category-rules"), "/")

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, ListCategoryRules())

	case http.MethodPost:
		var req categoryRuleReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rule, err := CreateCategoryRule(req.TypeID, req.SubtypeID, req.Label, req.Icon)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, rule)

	case http.MethodPut:
		if id == "" {
			http.Error(w, "rule id required in path", http.StatusBadRequest)
			return
		}
		var req categoryRuleReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rule, err := UpdateCategoryRule(id, req.TypeID, req.SubtypeID, req.Label, req.Icon)
		if err != nil {
			status := http.StatusBadRequest
			if err == errAIEventNotFound {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, http.StatusOK, rule)

	case http.MethodDelete:
		if id == "" {
			http.Error(w, "rule id required", http.StatusBadRequest)
			return
		}
		if err := DeleteCategoryRule(id); err != nil {
			status := http.StatusInternalServerError
			if err == errAIEventNotFound {
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

func handleObservedCategories(w http.ResponseWriter, r *http.Request) {
	if !requireAIEventAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, ListObservedCategories())
}

// ── Image retention sweeper ──────────────────────────────────────────────

// startImageRetentionSweeper runs CleanupOldImages on a fixed cadence much
// finer than the default 4h retention window itself, so actual on-disk
// image lifetime stays close to what the admin configured rather than
// drifting toward "retention + up to one sweep interval" — same reasoning
// that moved internal/incidents' Traffic Event image-adjacent cleanup
// cadence question before it, applied here from the start.
func startImageRetentionSweeper() {
	go func() {
		for range time.Tick(15 * time.Minute) {
			hours := auth.GetSettings().AIEventImageRetentionHours
			n := CleanupOldImages(hours)
			if n > 0 {
				log.Info().Int("deleted_files", n).Msg("[aievent] image retention sweep")
			}
		}
	}()
}
