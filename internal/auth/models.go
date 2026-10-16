package auth

// Role constants
const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// Tab constants — page-level permission keys sent to the frontend.
// Admin always has all tabs; viewers get only the tabs listed in User.Tabs.
const (
	TabCameras    = "cameras"    // / (camera grid)
	TabMap        = "map"        // /map.html
	TabMonitor    = "monitor"    // /monitor.html
	TabDashboard  = "dashboard"  // /dashboard.html
	TabDashboard2 = "dashboard2" // /dashboard2.html — independent of TabDashboard, granted separately
	TabLog        = "log"        // /log.html
	TabConfig     = "config"     // /config.html
	TabAPIDocs    = "api_docs"   // /api-docs.html
	TabCounting   = "counting"   // /counting.html
	TabIncidents  = "incidents"  // /incidents.html
)

// allAdminTabs is the full tab list automatically given to admins.
var allAdminTabs = []string{TabCameras, TabMap, TabMonitor, TabDashboard, TabDashboard2, TabLog, TabConfig, TabAPIDocs, TabCounting, TabIncidents}

// User represents an application user
type User struct {
	Username   string   `json:"username"`
	Password   string   `json:"password"`    // bcrypt hash
	Role       string   `json:"role"`        // "admin" or "viewer"
	Streams    []string `json:"streams"`     // allowed stream names (viewer); nil/empty for admin = all
	RegionIDs  []string `json:"region_ids"`  // additional access: every camera inside these regions ("địa bàn") — see regions.go
	AllowPaths []string `json:"allow_paths"` // custom API paths; nil = role defaults
	Tabs       []string `json:"tabs"`        // page-level permissions (viewer); must be granted explicitly
	Enabled    bool     `json:"enabled"`
	// MustChangePassword forces the user to set their own password before
	// using anything else. Set automatically when a new account is created
	// (see storage.go CreateUser/seedAdmin); cleared by a successful
	// POST /api/auth/change-password. Enforced in Middleware.
	MustChangePassword  bool `json:"must_change_password"`
	AllowTraffic        bool `json:"allow_traffic"`         // can use traffic overlay on map
	AllowHeatmap        bool `json:"allow_heatmap"`         // can use heatmap overlay on map
	AllowMapEdit        bool `json:"allow_map_edit"`        // can edit camera locations on map
	AllowCamNames       bool `json:"allow_cam_names"`       // can see real camera names (like admin)
	AllowViewStations   bool `json:"allow_view_stations"`   // can view traffic counting station data on map
	AllowConfigStations bool `json:"allow_config_stations"` // can add/edit/delete stations and station types
	// AllowMapSearch/AllowMapRoute gate the map's own analysis tools —
	// "Search area" (circle) and "Theo tuyến" (route corridor). Off by
	// default for new viewers, always on for admins, same convention as
	// AllowMapSnapshotPreview below.
	AllowMapSearch bool `json:"allow_map_search"`
	AllowMapRoute  bool `json:"allow_map_route"`
	// AllowMapIncidentAdd gates map.html's "Thêm sự cố" quick-add mode
	// (right-click the map to open the incidents form pre-filled with that
	// point's lat/lng) — only meaningful alongside the Incidents tab, which
	// is what actually authorizes the POST /api/incidents write itself;
	// this flag only controls whether the map shows the convenience button,
	// same client-only-gate convention as AllowMapSearch/AllowMapRoute above.
	AllowMapIncidentAdd bool `json:"allow_map_incident_add"`
	// AllowMapAIEvents gates map.html's "🤖 AI Event" layer — bubbles for
	// incidents an external AI system (OMNIA/FPT VDS and, later, other
	// sources — see internal/aievent) pushed in. View-only, unlike
	// AllowMapIncidentAdd: there's no manual-entry counterpart to this
	// layer, so this flag alone fully controls whether a user can see it.
	AllowMapAIEvents bool `json:"allow_map_ai_events"`
	// AllowMap3D gates map.html's "🧊 3D" toggle — tilts the camera (pitch)
	// and shows a 3D building-extrusion layer rendered client-side from the
	// same VietMap vector tiles the base map already fetches (no separate
	// API/endpoint involved, unlike AllowTrafficLive/AllowMapAIEvents above —
	// purely a map-rendering mode, so there's nothing to gate server-side).
	AllowMap3D bool `json:"allow_map_3d"`
	// AllowMapTrafficFlow gates map.html's Hawkwise traffic-flow layer —
	// points for admin-mapped Sensor records (internal/trafficflow) showing
	// each one's latest pushed vehicle volume/traffic_status. View-only,
	// same single-flag convention as AllowMapAIEvents above; the push
	// endpoint itself (POST /api/trafficdata/hawkwise/v1/flow-push) is
	// authenticated via the Integration API Key system, never this flag —
	// see internal/trafficflow's own doc comments.
	AllowMapTrafficFlow bool `json:"allow_map_traffic_flow"`
	// AllowMapGIS gates map.html's "Dữ liệu GIS" layers — admin-managed
	// point/segment reference geometry (e.g. "28 điểm nguy cơ ùn tắc", and
	// later layers like "Đèn giao thông"/"Bảng VMS" — see internal/gis).
	// View-only, same single-flag convention as AllowMapAIEvents/
	// AllowMapTrafficFlow above; layer/feature CRUD stays admin-only
	// (Role == RoleAdmin), never gated by this flag.
	AllowMapGIS bool `json:"allow_map_gis"`
	// GisLayerIDs optionally narrows WHICH internal/gis Layers an
	// AllowMapGIS, non-admin user may see — nil/empty means unrestricted
	// (every layer), the original and still-default behavior, so existing
	// users are unaffected. Same shape/precedent as RegionIDs above: an
	// additive allowlist an admin sets per user rather than a single
	// blanket flag. Checked in internal/gis/api.go (handleData filters the
	// layer list; handleLayerIcon checks the one requested layer). Does
	// NOT gate /api/gis/congestion-check — the "28 điểm nguy cơ ùn tắc"
	// auto-tick on the incident form is meant to work the same for every
	// user regardless of which layers they can see on the map (see that
	// handler's own doc comment).
	GisLayerIDs []string `json:"gis_layer_ids"`
	// Monitor sub-permissions (only meaningful when user has monitor tab)
	AllowMonitorWorkers   bool `json:"allow_monitor_workers"`   // xem thẻ giám sát máy chủ phân tích
	AllowMonitorProcess   bool `json:"allow_monitor_process"`   // xem go2rtc process
	AllowMonitorStreaming bool `json:"allow_monitor_streaming"` // xem streaming & users
	AllowMonitorSnapshot  bool `json:"allow_monitor_snapshot"`  // xem snapshot status
	AllowMonitorDevices   bool `json:"allow_monitor_devices"`   // xem giám sát thiết bị
	// Camera view sub-permissions (only meaningful when user has cameras tab)
	AllowCamSnapshot bool `json:"allow_cam_snapshot"` // xem ảnh snapshot camera
	AllowCamVideo    bool `json:"allow_cam_video"`    // xem video live (webrtc/mse/hls)
	// AllowPTZ grants pan/tilt/zoom control on cameras whose assigned Camera
	// Type has PTZ enabled (see CameraType.PTZEnabled in camera_types.go).
	// Without it, the PTZ overlay never even appears while viewing — see
	// proxyStreamsHandler's per-stream "ptz" flag — not just disabled, so a
	// viewer without this permission has no way to tell a camera is
	// PTZ-capable at all.
	AllowPTZ bool `json:"allow_ptz"`
	// AllowMapSnapshotPreview gates the "Xem trước" live-thumbnail overlay
	// on the map (map.html) — off by default for new viewers, always on for admins.
	AllowMapSnapshotPreview bool `json:"allow_map_snapshot_preview"`
	// View-session time limit (web viewers only — never applies to RTSP/RTSPS
	// URLs). AllowUnlimitedViewing bypasses the limit entirely (always true
	// for admins). Otherwise ViewLimitMinutes overrides the system default
	// (AppSettings.DefaultViewLimitMinutes) when > 0; see auth.ViewSessionLimit.
	AllowUnlimitedViewing bool `json:"allow_unlimited_view"`
	ViewLimitMinutes      int  `json:"view_limit_minutes"`
	// Incidents sub-permission (only meaningful when user has incidents tab)
	AllowIncidentsImport bool `json:"allow_incidents_import"` // import hàng loạt từ Excel

	// AllowTrafficLive grants access to the alternative, higher-quality live
	// traffic source (VietMap Live Traffic Tile — internal/auth/traffic_live.go)
	// and the on-map toggle to pick it over the existing traffic overlay.
	// Independent of AllowTraffic: a user without this flag keeps using
	// exactly the traffic overlay they have today, unaffected.
	AllowTrafficLive bool `json:"allow_traffic_live"`

	// AllowDashboard2CustomWindow gates dashboard2.html's manual "Từ...Đến"
	// time-window picker on the "Bản đồ ùn tắc" panel. Without it, a viewer
	// only gets the always-on default: a sliding window matching the
	// configured traffic-jam scan interval (traffic.IntervalMin), which
	// keeps the map "live" and the data it reads light. Client-only gate,
	// same convention as AllowMap3D above — there's nothing to restrict
	// server-side, since the underlying endpoints (/api/incidents,
	// /api/traffic/data) are already scoped by TabDashboard2/TabIncidents
	// regardless of which window a caller requests.
	AllowDashboard2CustomWindow bool `json:"allow_dashboard2_custom_window"`

	// DeviceBindingScopes lists which sensitive-data categories (keys from
	// DeviceScopeCatalog — see device_binding.go) this user may only reach
	// from a device an admin has approved via WebAuthn. A category the user
	// is otherwise allowed to access (e.g. by Streams/RegionIDs, or one of
	// the AllowX flags above) but that isn't listed here works exactly as
	// before — not every viewer or every kind of data needs this.
	DeviceBindingScopes []string `json:"device_binding_scopes,omitempty"`
}

// RequiresDeviceBindingFor reports whether scope is one of this user's
// DeviceBindingScopes. Admins are never gated (checked by the caller).
func (u *User) RequiresDeviceBindingFor(scope string) bool {
	for _, s := range u.DeviceBindingScopes {
		if s == scope {
			return true
		}
	}
	return false
}

// EffectiveTabs returns the full resolved tab list for this user.
// Admins get all tabs; viewers get exactly the tabs granted in User.Tabs.
func (u *User) EffectiveTabs() []string {
	if u.Role == RoleAdmin {
		return allAdminTabs
	}
	if len(u.Tabs) > 0 {
		return u.Tabs
	}
	return []string{}
}

// viewerDefaultPaths are the API paths that viewers can always access.
// HasPrefix matching: "/api/hls" covers "/api/hls/index.m3u8" etc.
var viewerDefaultPaths = []string{
	"/api/streams",              // list streams (filtered per user in streams/api.go)
	"/api/ws",                   // WebSocket hub (WebRTC / MSE / HLS via JS)
	"/api/webrtc",               // WebRTC SDP exchange (REST fallback)
	"/api/hls",                  // HLS playlist + segments
	"/api/mjpeg",                // MJPEG stream
	"/api/mp4",                  // MP4 stream / download
	"/api/frame",                // snapshot JPEG
	"/api/auth/me",              // own profile
	"/api/auth/change-password", // any authenticated user may change their own password
	"/api/user-location",        // POST own GPS fix after login (GET is admin-only, guarded inside handler)
	"/api/proxy",                // masked-ID proxy endpoints
	"/api/camera-locations",     // map: read camera GPS coords
	"/api/groups",               // camera groups (read-only for viewers)
	"/api/settings",             // app settings (vietmap key; write guarded inside handler)
	"/api/cameras",              // camera list with GPS coords (no stream URLs)
	"/api/traffic-heatmap",      // map: jam points for heatmap overlay (read-only)
	"/api/heatmap-cfg",          // map/dashboard: heatmap config (write guarded inside handler)
	"/api/auth/webauthn",        // device-binding enroll/step-up ceremonies (own account only)
	"/api/device-scopes",        // device-binding scope catalog (labels only, nothing sensitive)
	"/api/device-bindings",      // own trusted devices; admin-only listing/actions guarded inside handler
	// Deliberately NOT gated behind allow_map_gis (unlike /api/gis/data
	// just below it in userCanAccessPath) — whether an incident sits near
	// a "28 điểm nguy cơ ùn tắc" point/segment is meant to be the same,
	// system-wide answer for every user creating an incident, not
	// something that depends on whether that user also has permission to
	// see the underlying GIS layer on the map. See internal/gis/api.go's
	// handleCongestionCheck.
	"/api/gis/congestion-check",
}

// Claims holds JWT payload fields
type Claims struct {
	Username string `json:"sub"`
	Role     string `json:"role"`
	Exp      int64  `json:"exp"`
}

// contextKey is used for request context values
type contextKey string

const userContextKey contextKey = "auth_user"

// errNotFound is returned by storage when the user does not exist.
var errNotFound = &notFoundError{}

type notFoundError struct{}

func (e *notFoundError) Error() string { return "user not found" }
