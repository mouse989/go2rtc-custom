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
		if err := json.NewEncoder(w).Encode(GetSettings()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}

	case http.MethodPost, http.MethodPut:
		if user.Role != RoleAdmin {
			http.Error(w, "admin only", http.StatusForbidden)
			return
		}
		var s AppSettings
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := UpdateSettings(s); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
