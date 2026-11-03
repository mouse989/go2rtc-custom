package trafficmetrics

// api.go — GET/PUT /api/trafficmetrics/config, the one admin-editable
// surface this module exposes: the Z/LOS calibration constants and 6-level
// LOS table. Registered in internal/auth/models.go's viewerDefaultPaths
// (same convention as /api/jam-color-cfg) since every page that colors by
// LOS — not just admins — needs to read it.

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/AlexxIT/go2rtc/internal/auth"
)

func registerHandlers() {
	http.HandleFunc("/api/trafficmetrics/config", handleConfig)
}

func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user.Role != auth.RoleAdmin {
		http.Error(w, "admin only", http.StatusForbidden)
		return false
	}
	return true
}

func requireLogin(w http.ResponseWriter, r *http.Request) bool {
	_, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !requireLogin(w, r) {
			return
		}
		writeJSON(w, GetConfig())

	case http.MethodPut, http.MethodPost:
		if !requireAdmin(w, r) {
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			http.Error(w, "read error: "+err.Error(), http.StatusBadRequest)
			return
		}
		var c Config
		if err := json.Unmarshal(body, &c); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := validateConfig(&c); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		setConfig(c)
		if err := saveConfig(); err != nil {
			http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, GetConfig())

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
