package auth

import (
	"encoding/json"
	"net/http"
)

func registerSettingsHandler() {
	http.HandleFunc("/api/settings", settingsHandler)
	http.HandleFunc("/api/heatmap-cfg", heatmapCfgHandler)
}

func settingsHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(redactedSettings(GetSettings())); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}

	case http.MethodPost, http.MethodPut:
		if user.Role != RoleAdmin {
			http.Error(w, "admin only", http.StatusForbidden)
			return
		}
		// Start from the current settings, not a zero value: callers (e.g.
		// admin.html's basic "Save Settings" button) only ever send the
		// fields they show, so decoding into an empty struct would silently
		// wipe every other setting (heatmap_cfg, view-limit default, etc.)
		// back to its zero value on every partial save.
		s := GetSettings()
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := UpdateSettings(s); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Re-init the WebAuthn relying party so an RP ID/origin change (or
		// first-time setup) takes effect immediately rather than only after
		// a restart.
		if err := initWebAuthn(s.DeviceBindingRPID, "go2rtc", s.DeviceBindingRPOrigins); err != nil {
			log.Warn().Err(err).Msg("[auth] webauthn relying party re-init failed")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(redactedSettings(s))

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// settingsOut mirrors AppSettings for API responses, except for secrets that
// must never reach a browser — currently just VietmapLiveTrafficAPIKey (see
// its doc comment in settings.go: the upstream it's for is IP-allow-listed
// to this server only, so the key has no legitimate reason to ever leave
// it). The explicit field below shadows the embedded one for JSON encoding
// (Go's encoding/json gives an explicit field priority over a
// same-named promoted field), replacing its value with "" on the wire.
// VietmapLiveTrafficConfigured tells the admin UI whether a key is already
// saved without revealing it, so "leave blank to keep" has something to
// show the admin besides silence.
type settingsOut struct {
	AppSettings
	VietmapLiveTrafficAPIKey     string `json:"vietmap_live_traffic_api_key"`
	VietmapLiveTrafficConfigured bool   `json:"vietmap_live_traffic_configured"`
}

func redactedSettings(s AppSettings) settingsOut {
	return settingsOut{
		AppSettings:                  s,
		VietmapLiveTrafficConfigured: s.VietmapLiveTrafficAPIKey != "",
	}
}

// heatmapCfgHandler GET/PUT/DELETE /api/heatmap-cfg
// GET: any authenticated user. PUT/DELETE: admin only.
func heatmapCfgHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s := GetSettings()
		w.Header().Set("Content-Type", "application/json")
		if s.HeatmapCfg == nil {
			w.Write([]byte("null"))
			return
		}
		json.NewEncoder(w).Encode(s.HeatmapCfg)

	case http.MethodPost, http.MethodPut:
		if user.Role != RoleAdmin {
			http.Error(w, "admin only", http.StatusForbidden)
			return
		}
		var cfg HeatmapConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s := GetSettings()
		s.HeatmapCfg = &cfg
		if err := UpdateSettings(s); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg)

	case http.MethodDelete:
		if user.Role != RoleAdmin {
			http.Error(w, "admin only", http.StatusForbidden)
			return
		}
		s := GetSettings()
		s.HeatmapCfg = nil
		_ = UpdateSettings(s)
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
