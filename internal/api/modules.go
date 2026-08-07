package api

import (
	"encoding/json"
	"net/http"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/auth"
)

// ModuleInfo describes one optional module the admin can include/exclude via
// app.modules in go2rtc.yaml — see main.go's module table. Modules listed
// there with an empty "" name are core/always-on (app, auth config/logs) and
// never appear here; "api" itself is always effectively on too (it's what
// serves this very page and the reverse-proxy feature) but is included below
// so a saved list is self-documenting.
type ModuleInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Group string `json:"group"`
}

// controllableModules is a curated subset of main.go's full module table —
// only the ones meaningful to toggle from this UI. Many other module names
// exist (vendor camera-source integrations like kasa/tapo/tuya/onvif clients,
// exec/ffmpeg helpers, etc.) but are inert unless actually referenced by a
// `streams:` entry, so there's no practical benefit to exposing them
// individually — leaving them out of this list just means they're always
// included when "api"-only mode isn't active, same as before this feature.
var controllableModules = []ModuleInfo{
	{"api", "API / Web UI / Reverse proxy", "Lõi hệ thống"},
	{"streams", "Nguồn camera (kéo/phục vụ stream)", "Camera & Stream"},
	{"rtsp", "RTSP server", "Giao thức stream"},
	{"webrtc", "WebRTC server", "Giao thức stream"},
	{"hls", "HLS", "Giao thức stream"},
	{"mp4", "MP4", "Giao thức stream"},
	{"mjpeg", "MJPEG", "Giao thức stream"},
	{"onvif", "ONVIF server", "Giao thức stream"},
	{"rtmp", "RTMP server", "Giao thức stream"},
	{"homekit", "HomeKit", "Giao thức stream"},
	{"hass", "Home Assistant", "Giao thức stream"},
	{"monitor", "Giám sát hệ thống (CPU/RAM)", "Tính năng khác"},
	{"traffic", "Traffic (quét kẹt xe)", "Tính năng khác"},
	{"traveltime", "Travel time / dự báo", "Tính năng khác"},
	{"workers", "Remote workers", "Tính năng khác"},
	{"counting", "Đếm xe (YOLO counting)", "Tính năng khác"},
	{"dashboard", "Dashboard", "Tính năng khác"},
	{"incidents", "Sự cố giao thông", "Tính năng khác"},
}

func registerModuleHandlers() {
	HandleFunc("api/modules", handleModules)
}

// handleModules: GET reports the known toggleable modules + which are
// currently active (app.Modules == nil means "everything runs", the
// default). PUT writes a new app.modules list to go2rtc.yaml — takes effect
// on next restart, e.g. via POST /api/restart.
func handleModules(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user.Role != auth.RoleAdmin {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet:
		ResponseJSON(w, map[string]any{
			"all":    controllableModules,
			"active": app.Modules, // nil = no restriction, everything runs
		})

	case http.MethodPut:
		var req struct {
			Modules *[]string `json:"modules"` // null = clear restriction (run everything)
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var toSave any
		if req.Modules != nil {
			known := make(map[string]bool, len(controllableModules))
			for _, m := range controllableModules {
				known[m.ID] = true
			}
			hasAPI := false
			for _, m := range *req.Modules {
				if !known[m] {
					http.Error(w, "unknown module: "+m, http.StatusBadRequest)
					return
				}
				if m == "api" {
					hasAPI = true
				}
			}
			// Excluding "api" would stop this very endpoint (and the whole
			// Web UI/reverse-proxy) from starting on next restart, with no
			// way to undo it short of hand-editing go2rtc.yaml on the box —
			// refuse instead of letting an admin lock themselves out.
			if !hasAPI {
				http.Error(w, "modules list must include \"api\" (excluding it would disable the Web UI/reverse-proxy itself)", http.StatusBadRequest)
				return
			}
			toSave = *req.Modules
		}
		// req.Modules == nil (JSON `"modules": null` or field omitted) ->
		// toSave stays a nil `any`, which PatchConfig removes from the YAML.

		if err := app.PatchConfig([]string{"app", "modules"}, toSave); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		ResponseJSON(w, map[string]bool{"ok": true})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
