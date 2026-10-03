package auth

// api_ptz.go — HTTP API for pan/tilt/zoom control.
//
// POST /api/ptz/move  body: {"stream": "...", "pan": -1..1, "tilt": -1..1, "zoom": -1..1}
// POST /api/ptz/stop  body: {"stream": "..."}
//
// Every call re-checks permission from scratch (ptzAllowed) — a client is
// never trusted just because /api/proxy/streams once reported "ptz: true"
// for this stream; that flag is a UI convenience, not the access boundary.

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

func registerPTZHandlers() {
	http.HandleFunc("/api/ptz/move", ptzMoveHandler)
	http.HandleFunc("/api/ptz/stop", ptzStopHandler)
}

// ptzRequestTimeout bounds how long a move/stop HTTP call to the camera may
// take — PTZ is an interactive, held-button control, so a slow or
// unreachable camera must fail fast rather than hang the request.
const ptzRequestTimeout = 5 * time.Second

func ptzMoveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		Stream string  `json:"stream"`
		Pan    float64 `json:"pan"`
		Tilt   float64 `json:"tilt"`
		Zoom   float64 `json:"zoom"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Stream == "" {
		http.Error(w, "stream required", http.StatusBadRequest)
		return
	}
	if !ptzAllowed(user, req.Stream) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), ptzRequestTimeout)
	defer cancel()
	if err := SendPTZMove(ctx, req.Stream, clampPTZ(req.Pan), clampPTZ(req.Tilt), clampPTZ(req.Zoom)); err != nil {
		http.Error(w, "ptz move failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func ptzStopHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		Stream string `json:"stream"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Stream == "" {
		http.Error(w, "stream required", http.StatusBadRequest)
		return
	}
	if !ptzAllowed(user, req.Stream) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), ptzRequestTimeout)
	defer cancel()
	if err := SendPTZStop(ctx, req.Stream); err != nil {
		http.Error(w, "ptz stop failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ptzAllowed re-checks, independently of whatever the client was shown,
// that user may control PTZ on stream: the global permission, that the
// user can see this specific stream at all, and that the stream's camera
// type actually has PTZ configured.
func ptzAllowed(user *User, stream string) bool {
	if user.Role != RoleAdmin && !user.AllowPTZ {
		return false
	}
	if !UserCanAccessStream(user, stream) {
		return false
	}
	return PTZEnabledForStream(stream)
}

func clampPTZ(v float64) float64 {
	if v > 1 {
		return 1
	}
	if v < -1 {
		return -1
	}
	return v
}
