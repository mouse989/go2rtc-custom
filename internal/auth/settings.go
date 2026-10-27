package auth

import (
	"encoding/json"
	"os"
	"sync"
)

// HeatmapConfig holds the user-configurable heatmap rendering parameters.
type HeatmapConfig struct {
	R5  int    `json:"r5"`  // heatmap-radius at zoom 5
	R15 int    `json:"r15"` // heatmap-radius at zoom 15
	I5  int    `json:"i5"`  // heatmap-intensity×10 at zoom 5
	I15 int    `json:"i15"` // heatmap-intensity×10 at zoom 15
	Op  int    `json:"op"`  // opacity 0-100
	C1  string `json:"c1"`  // color: low
	C1a int    `json:"c1a"` // alpha 0-100
	C2  string `json:"c2"`  // color: mid
	C2a int    `json:"c2a"`
	C3  string `json:"c3"` // color: high
	C3a int    `json:"c3a"`
	C4  string `json:"c4"` // color: max
	C4a int    `json:"c4a"`
	Crf int    `json:"crf"` // dot circle-radius at JF=0
	Crt int    `json:"crt"` // dot circle-radius at JF=10
}

// JamColorStop is one color stop in the JamFactor (JF, 0–10) severity
// color scale — shared across every map that renders JF-colored points
// (map.html, dashboard2.html, trafficinsight.html, …). Turned into a
// MapLibre/VietMapGL `interpolate` paint expression client-side (app.js's
// jfColorExpression), so the color between two stops blends continuously
// rather than snapping at hard thresholds — see jam_color.go for the
// built-in default stops.
type JamColorStop struct {
	Value float64 `json:"value"` // 0–10
	Color string  `json:"color"` // "#rrggbb"
}

// AppSettings holds persisted application-level settings.
type AppSettings struct {
	VietmapAPIKey       string `json:"vietmap_api_key"`
	SnapshotIntervalSec int    `json:"snapshot_interval_sec"` // 0 → default 15 s
	SnapshotConcurrency int    `json:"snapshot_concurrency"`  // 0 → auto (interval_sec × 10)
	// SnapshotRTSPKeepAlive: for camera types with no HTTP snapshot URL (grab
	// a keyframe from go2rtc's own RTSP stream instead), keep that RTSP
	// connection open between polls rather than reconnecting every cycle.
	// Off by default: an open RTSP connection receives the camera's full,
	// continuous video stream the whole time it's held open (RTSP has no way
	// to ask for "just the next keyframe" without a live session), not only
	// the small burst a connect→grab→disconnect cycle pulls — so this trades
	// per-cycle reconnect overhead (CPU/sockets) for sustained per-camera
	// bandwidth, which at a few thousand cameras is usually the worse trade.
	// See internal/mjpeg/keepalive.go's GetKeepAliveStats for the live cost.
	SnapshotRTSPKeepAlive bool `json:"snapshot_rtsp_keepalive"`
	// SnapshotStaleThresholdSec: how old an on-disk snapshot may be before
	// GET /api/proxy/frame refuses to serve it and shows a "disconnected"
	// placeholder instead — otherwise a camera that's been offline for hours
	// keeps silently showing its last-known-good frame forever, looking
	// exactly like a live view. 0 → auto (4× snapshot_interval_sec, floored
	// at 60s — see snapshotStaleThreshold in proxy.go for why it scales
	// with the interval instead of a bare fixed value).
	SnapshotStaleThresholdSec int     `json:"snapshot_stale_threshold_sec"`
	MapSearchRadiusKm         float64 `json:"map_search_radius_km"` // 0 → default 1 km
	MapRouteWidthM            float64 `json:"map_route_width_m"`    // "Theo tuyến" tool: 0 → default 100 m
	// EventWindowMinutes: how many minutes back from "now" the Map page's
	// "🚨 Traffic Event" layer (internal/incidents/api.go's handleActive)
	// and its "🤖 AI Event" layer (internal/aievent/api.go's handleActive)
	// both show bubbles for. An event whose time falls outside
	// [now-window, now] is no longer "live" and drops off its layer on the
	// next poll. One shared setting for both layers — they're independent
	// data sources with independent on/off toggles, but a single "how
	// recent counts as live" window keeps that meaning consistent across
	// the system instead of an admin having to keep two numbers in sync by
	// hand. 0 → default 10 minutes.
	EventWindowMinutes int `json:"event_window_minutes"`
	// TrafficEventWindowMinutes/AIEventWindowMinutes: deprecated, no longer
	// read by either handler (replaced by EventWindowMinutes above) — kept
	// only so initSettings can migrate an old settings.json's values into
	// EventWindowMinutes the first time it's loaded after the upgrade,
	// without losing whatever an admin had already customized.
	TrafficEventWindowMinutes int `json:"traffic_event_window_minutes,omitempty"`
	AIEventWindowMinutes      int `json:"ai_event_window_minutes,omitempty"`
	// AIEventImageRetentionHours: how many hours an AI-event attachment
	// image stays on disk before the cleanup sweep deletes it (the event's
	// own metadata record is kept indefinitely — only the image files are
	// pruned, since they're only useful for a short review window after the
	// fact). 0 → default 4 hours. See internal/aievent's image retention job.
	AIEventImageRetentionHours int            `json:"ai_event_image_retention_hours"`
	HeatmapCfg                 *HeatmapConfig `json:"heatmap_cfg,omitempty"`
	DefaultViewLimitMinutes    int            `json:"default_view_limit_minutes"` // 0 → built-in default (see ViewSessionLimit)

	// Security-alert "normal login hours" window (Asia/Ho_Chi_Minh). When
	// enabled, a successful login outside [LoginHourStart, LoginHourEnd)
	// raises an off_hours_login alert — see security_alerts.go.
	LoginHourRangeEnabled bool `json:"login_hour_range_enabled"`
	LoginHourStart        int  `json:"login_hour_start"` // 0-23
	LoginHourEnd          int  `json:"login_hour_end"`   // 0-23

	// Login brute-force lockout thresholds — see login_throttle.go.
	// 0 → built-in default (5 failures / 5 minutes).
	MaxLoginFailures    int `json:"max_login_failures"`
	LoginLockoutMinutes int `json:"login_lockout_minutes"`

	// Default state of the Map page's "📊 Trạm đo" (counting stations) layer
	// toggle for users who haven't touched it yet in their own browser (a
	// per-browser click is remembered separately, in localStorage). Inverted
	// so the zero value (false) keeps today's behavior — layer on by default.
	StationsLayerDefaultOff bool `json:"stations_layer_default_off"`

	// Device-binding (WebAuthn/passkey) settings — see device_binding.go and
	// webauthn.go. Disabled (RPID empty) by default: registering and
	// step-up-verifying a device only works once an admin fills these in,
	// and WebAuthn requires RPID to be the exact domain (no scheme/port)
	// the browser's address bar shows, and RPOrigins the exact
	// "https://" + that domain the browser sends as Origin — a mismatch
	// fails every ceremony, so both must be set together and kept in sync
	// with the real deployment hostname.
	DeviceBindingEnabled   bool     `json:"device_binding_enabled"`
	DeviceBindingRPID      string   `json:"device_binding_rp_id"`      // e.g. "dieuhanh.utmc.io.vn"
	DeviceBindingRPOrigins []string `json:"device_binding_rp_origins"` // e.g. ["https://dieuhanh.utmc.io.vn"]
	// DeviceVerifyValidHours: once a step-up verification succeeds, how long
	// the resulting cookie satisfies further requests to the same gated
	// scope before the browser is asked to verify again. 0 → default 12h.
	DeviceVerifyValidHours int `json:"device_verify_valid_hours"`

	// VietmapLiveTrafficAPIKey is the key for VietMap's "Live Traffic Tile"
	// vector-tile API (internal/auth/traffic_live.go) — separate from
	// VietmapAPIKey above (used for map tiles/geocoding and the older
	// traffic-style passthrough). That API is IP-allow-listed to this
	// server only, so this key must never reach the browser: the settings
	// HTTP handler (api_settings.go) always redacts this field on GET and
	// only overwrites it on POST when a new non-empty value is sent.
	VietmapLiveTrafficAPIKey string `json:"vietmap_live_traffic_api_key,omitempty"`

	// TrafficFlowRetentionDays: how many days of "📊 Đo đếm lưu lượng" data
	// (internal/trafficflow — Hawkwise push records, stored in
	// trafficflow_data/trafficflow.db) to keep before the retention sweep
	// deletes older rows. 0 → default 30 days. -1 → keep forever (the sweep
	// never deletes anything). Admin-configurable per explicit request —
	// this used to be a hardcoded Go constant.
	TrafficFlowRetentionDays int `json:"traffic_flow_retention_days"`

	// JamColorStops: admin-customized JF color scale (see JamColorStop
	// above). Empty/nil → every page falls back to jamColorDefaultStops
	// (jam_color.go) — GET /api/jam-color-cfg always resolves this for the
	// caller, so no page needs to hold its own copy of the default.
	JamColorStops []JamColorStop `json:"jam_color_stops,omitempty"`
}

var (
	settingsMu   sync.RWMutex
	appSettings  AppSettings
	settingsFile string
)

func initSettings(path string) error {
	settingsFile = path
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // start empty; file created on first save
		}
		return err
	}
	if err := json.Unmarshal(data, &appSettings); err != nil {
		return err
	}
	// One-time migration: a settings.json saved before EventWindowMinutes
	// existed only has the two now-deprecated per-layer fields. Carry
	// whichever one an admin had actually customized forward instead of
	// silently resetting both layers to the 10-minute default. Prefers the
	// larger of the two if both were customized differently, so neither
	// layer's events disappear sooner than an admin had configured. Runs
	// again (harmlessly) on every restart until the next settings save
	// persists EventWindowMinutes to disk.
	if appSettings.EventWindowMinutes == 0 {
		if appSettings.TrafficEventWindowMinutes > appSettings.EventWindowMinutes {
			appSettings.EventWindowMinutes = appSettings.TrafficEventWindowMinutes
		}
		if appSettings.AIEventWindowMinutes > appSettings.EventWindowMinutes {
			appSettings.EventWindowMinutes = appSettings.AIEventWindowMinutes
		}
	}
	return nil
}

// GetSettings returns a copy of the current settings.
func GetSettings() AppSettings {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return appSettings
}

// UpdateSettings replaces settings and persists them to disk.
func UpdateSettings(s AppSettings) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(settingsFile, data, 0644); err != nil {
		return err
	}
	settingsMu.Lock()
	appSettings = s
	settingsMu.Unlock()
	return nil
}
