package auth

import (
	"encoding/json"
	"net/http"
	"strings"
)

// responseJSON writes v as JSON — local copy to avoid importing internal/api (would cycle)
func responseJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func registerHandlers() {
	// Public / semi-public auth endpoints (middleware allows them through)
	http.HandleFunc("/api/auth/login", loginHandler)
	http.HandleFunc("/api/auth/logout", logoutHandler)
	http.HandleFunc("/api/auth/me", meHandler)

	// Admin-only user management  (/api/users and /api/users/{name})
	http.HandleFunc("/api/users", usersHandler)
	http.HandleFunc("/api/users/", usersHandler) // with trailing username
}

// loginHandler POST /api/auth/login  {"username":"..","password":".."}
func loginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	user, ok := Authenticate(req.Username, req.Password)
	if !ok {
		// Use WriteHeader directly — http.Error() overrides Content-Type to text/plain
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid credentials"}`))
		return
	}

	token, err := GenerateToken(user)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "go2rtc_token",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400,
	})

	responseJSON(w, map[string]interface{}{
		"token":    token,
		"username": user.Username,
		"role":     user.Role,
	})
}

// logoutHandler POST /api/auth/logout
func logoutHandler(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:   "go2rtc_token",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
	w.WriteHeader(http.StatusNoContent)
}

// meHandler GET /api/auth/me
func meHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	responseJSON(w, map[string]interface{}{
		"username":                user.Username,
		"role":                    user.Role,
		"streams":                 user.Streams,
		"allow_traffic":           user.AllowTraffic,
		"allow_heatmap":           user.AllowHeatmap,
		"allow_map_edit":          user.AllowMapEdit,
		"allow_cam_names":         user.AllowCamNames,
		"allow_view_stations":     user.AllowViewStations,
		"allow_config_stations":   user.AllowConfigStations,
		"allow_monitor_workers":   user.AllowMonitorWorkers,
		"allow_monitor_process":   user.AllowMonitorProcess,
		"allow_monitor_streaming": user.AllowMonitorStreaming,
		"allow_monitor_snapshot":  user.AllowMonitorSnapshot,
		"allow_monitor_devices":   user.AllowMonitorDevices,
		"allow_cam_snapshot":      user.AllowCamSnapshot,
		"allow_cam_video":         user.AllowCamVideo,
		"tabs":                    user.EffectiveTabs(),
	})
}

// usersHandler handles /api/users and /api/users/{username}  (admin only)
func usersHandler(w http.ResponseWriter, r *http.Request) {
	// Extract optional {username} from URL
	path := strings.TrimPrefix(r.URL.Path, "/api/users")
	targetUser := strings.Trim(path, "/")

	// Require admin role
	caller, ok := UserFromContext(r.Context())
	if !ok || caller.Role != RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet:
		if targetUser != "" {
			u, found := GetUser(targetUser)
			if !found {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			u.Password = ""
			responseJSON(w, u)
		} else {
			responseJSON(w, ListUsers())
		}

	case http.MethodPost:
		var req struct {
			Username              string   `json:"username"`
			Password              string   `json:"password"`
			Role                  string   `json:"role"`
			Streams               []string `json:"streams"`
			AllowPaths            []string `json:"allow_paths"`
			Tabs                  []string `json:"tabs"`
			Enabled               *bool    `json:"enabled"`
			AllowTraffic          bool     `json:"allow_traffic"`
			AllowHeatmap          bool     `json:"allow_heatmap"`
			AllowMapEdit          bool     `json:"allow_map_edit"`
			AllowCamNames         bool     `json:"allow_cam_names"`
			AllowViewStations     bool     `json:"allow_view_stations"`
			AllowConfigStations   bool     `json:"allow_config_stations"`
			AllowMonitorWorkers   bool     `json:"allow_monitor_workers"`
			AllowMonitorProcess   bool     `json:"allow_monitor_process"`
			AllowMonitorStreaming bool     `json:"allow_monitor_streaming"`
			AllowMonitorSnapshot  bool     `json:"allow_monitor_snapshot"`
			AllowMonitorDevices   bool     `json:"allow_monitor_devices"`
			AllowCamSnapshot      bool     `json:"allow_cam_snapshot"`
			AllowCamVideo         bool     `json:"allow_cam_video"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Username == "" || req.Password == "" {
			http.Error(w, "username and password required", http.StatusBadRequest)
			return
		}
		if req.Role != RoleAdmin && req.Role != RoleViewer {
			req.Role = RoleViewer
		}
		enabled := true
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		u := &User{
			Username:              req.Username,
			Role:                  req.Role,
			Streams:               req.Streams,
			AllowPaths:            req.AllowPaths,
			Tabs:                  req.Tabs,
			Enabled:               enabled,
			AllowTraffic:          req.AllowTraffic,
			AllowHeatmap:          req.AllowHeatmap,
			AllowMapEdit:          req.AllowMapEdit,
			AllowCamNames:         req.AllowCamNames,
			AllowViewStations:     req.AllowViewStations,
			AllowConfigStations:   req.AllowConfigStations,
			AllowMonitorWorkers:   req.AllowMonitorWorkers,
			AllowMonitorProcess:   req.AllowMonitorProcess,
			AllowMonitorStreaming: req.AllowMonitorStreaming,
			AllowMonitorSnapshot:  req.AllowMonitorSnapshot,
			AllowMonitorDevices:   req.AllowMonitorDevices,
			AllowCamSnapshot:      req.AllowCamSnapshot,
			AllowCamVideo:         req.AllowCamVideo,
		}
		if _, exists := GetUser(req.Username); exists {
			http.Error(w, "user already exists", http.StatusConflict)
			return
		}
		if err := CreateUser(u, req.Password); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		u.Password = ""
		w.WriteHeader(http.StatusCreated)
		responseJSON(w, u)

	case http.MethodPut:
		if targetUser == "" {
			http.Error(w, "username required in path", http.StatusBadRequest)
			return
		}
		var req struct {
			Password              string   `json:"password"`
			Role                  string   `json:"role"`
			Streams               []string `json:"streams"`
			AllowPaths            []string `json:"allow_paths"`
			Tabs                  []string `json:"tabs"`
			Enabled               *bool    `json:"enabled"`
			AllowTraffic          *bool    `json:"allow_traffic"`
			AllowHeatmap          *bool    `json:"allow_heatmap"`
			AllowMapEdit          *bool    `json:"allow_map_edit"`
			AllowCamNames         *bool    `json:"allow_cam_names"`
			AllowViewStations     *bool    `json:"allow_view_stations"`
			AllowConfigStations   *bool    `json:"allow_config_stations"`
			AllowMonitorWorkers   *bool    `json:"allow_monitor_workers"`
			AllowMonitorProcess   *bool    `json:"allow_monitor_process"`
			AllowMonitorStreaming *bool    `json:"allow_monitor_streaming"`
			AllowMonitorSnapshot  *bool    `json:"allow_monitor_snapshot"`
			AllowMonitorDevices   *bool    `json:"allow_monitor_devices"`
			AllowCamSnapshot      *bool    `json:"allow_cam_snapshot"`
			AllowCamVideo         *bool    `json:"allow_cam_video"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		existing, found := GetUser(targetUser)
		if !found {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if req.Role == RoleAdmin || req.Role == RoleViewer {
			existing.Role = req.Role
		}
		if req.Streams != nil {
			existing.Streams = req.Streams
		}
		if req.AllowPaths != nil {
			existing.AllowPaths = req.AllowPaths
		}
		if req.Tabs != nil {
			existing.Tabs = req.Tabs
		}
		if req.Enabled != nil {
			existing.Enabled = *req.Enabled
		}
		if req.AllowTraffic != nil {
			existing.AllowTraffic = *req.AllowTraffic
		}
		if req.AllowHeatmap != nil {
			existing.AllowHeatmap = *req.AllowHeatmap
		}
		if req.AllowMapEdit != nil {
			existing.AllowMapEdit = *req.AllowMapEdit
		}
		if req.AllowCamNames != nil {
			existing.AllowCamNames = *req.AllowCamNames
		}
		if req.AllowViewStations != nil {
			existing.AllowViewStations = *req.AllowViewStations
		}
		if req.AllowConfigStations != nil {
			existing.AllowConfigStations = *req.AllowConfigStations
		}
		if req.AllowMonitorWorkers != nil {
			existing.AllowMonitorWorkers = *req.AllowMonitorWorkers
		}
		if req.AllowMonitorProcess != nil {
			existing.AllowMonitorProcess = *req.AllowMonitorProcess
		}
		if req.AllowMonitorStreaming != nil {
			existing.AllowMonitorStreaming = *req.AllowMonitorStreaming
		}
		if req.AllowMonitorSnapshot != nil {
			existing.AllowMonitorSnapshot = *req.AllowMonitorSnapshot
		}
		if req.AllowMonitorDevices != nil {
			existing.AllowMonitorDevices = *req.AllowMonitorDevices
		}
		if req.AllowCamSnapshot != nil {
			existing.AllowCamSnapshot = *req.AllowCamSnapshot
		}
		if req.AllowCamVideo != nil {
			existing.AllowCamVideo = *req.AllowCamVideo
		}
		if err := UpdateUser(existing, req.Password); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		existing.Password = ""
		responseJSON(w, existing)

	case http.MethodDelete:
		if targetUser == "" {
			http.Error(w, "username required", http.StatusBadRequest)
			return
		}
		if targetUser == caller.Username {
			http.Error(w, "cannot delete yourself", http.StatusBadRequest)
			return
		}
		if err := DeleteUser(targetUser); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
