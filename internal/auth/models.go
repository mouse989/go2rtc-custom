package auth

// Role constants
const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// Tab constants — page-level permission keys sent to the frontend.
// Admin always has all tabs; viewers get only the tabs listed in User.Tabs.
const (
	TabCameras   = "cameras"   // / (camera grid)
	TabMap       = "map"       // /map.html
	TabMonitor   = "monitor"   // /monitor.html
	TabDashboard = "dashboard" // /dashboard.html
	TabLog       = "log"       // /log.html
	TabConfig    = "config"    // /config.html
	TabAPIDocs   = "api_docs"  // /api-docs.html
	TabCounting  = "counting"  // /counting.html
)

// allAdminTabs is the full tab list automatically given to admins.
var allAdminTabs = []string{TabCameras, TabMap, TabMonitor, TabDashboard, TabLog, TabConfig, TabAPIDocs, TabCounting}

// User represents an application user
type User struct {
	Username     string   `json:"username"`
	Password     string   `json:"password"`      // bcrypt hash
	Role         string   `json:"role"`           // "admin" or "viewer"
	Streams      []string `json:"streams"`        // allowed stream names (viewer); nil/empty for admin = all
	AllowPaths   []string `json:"allow_paths"`    // custom API paths; nil = role defaults
	Tabs         []string `json:"tabs"`           // page-level permissions (viewer); must be granted explicitly
	Enabled      bool     `json:"enabled"`
	AllowTraffic        bool     `json:"allow_traffic"`         // can use traffic overlay on map
	AllowHeatmap        bool     `json:"allow_heatmap"`         // can use heatmap overlay on map
	AllowMapEdit        bool     `json:"allow_map_edit"`        // can edit camera locations on map
	AllowCamNames       bool     `json:"allow_cam_names"`       // can see real camera names (like admin)
	AllowViewStations   bool     `json:"allow_view_stations"`   // can view traffic counting station data on map
	AllowConfigStations bool     `json:"allow_config_stations"` // can add/edit/delete stations and station types
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
	"/api/streams",    // list streams (filtered per user in streams/api.go)
	"/api/ws",         // WebSocket hub (WebRTC / MSE / HLS via JS)
	"/api/webrtc",     // WebRTC SDP exchange (REST fallback)
	"/api/hls",        // HLS playlist + segments
	"/api/mjpeg",      // MJPEG stream
	"/api/mp4",        // MP4 stream / download
	"/api/frame",      // snapshot JPEG
	"/api/auth/me",           // own profile
	"/api/proxy",             // masked-ID proxy endpoints
	"/api/camera-locations",  // map: read camera GPS coords
	"/api/groups",            // camera groups (read-only for viewers)
	"/api/settings",          // app settings (vietmap key; write guarded inside handler)
	"/api/cameras",           // camera list with GPS coords (no stream URLs)
	"/api/traffic-heatmap",   // map: jam points for heatmap overlay (read-only)
	"/api/heatmap-cfg",       // map/dashboard: heatmap config (write guarded inside handler)
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
