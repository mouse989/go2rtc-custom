package auth

import "time"

// defaultViewLimitMinutes is used when a limited user has no personal
// ViewLimitMinutes override and the admin hasn't set
// AppSettings.DefaultViewLimitMinutes.
const defaultViewLimitMinutes = 15

// ViewSessionLimit returns how long u may keep a single live-view
// connection open (WebSocket-driven WebRTC/MSE/HLS/MJPEG, or a plain HTTP
// MJPEG/MP4/HLS stream) before it is force-closed server-side, or 0 for
// unlimited. This only ever gates browser/web viewing — it has no bearing
// on the RTSP/RTSPS server, which never calls this function.
func ViewSessionLimit(u *User) time.Duration {
	if u == nil || u.Role == RoleAdmin || u.AllowUnlimitedViewing {
		return 0
	}
	minutes := u.ViewLimitMinutes
	if minutes <= 0 {
		minutes = GetSettings().DefaultViewLimitMinutes
	}
	if minutes <= 0 {
		minutes = defaultViewLimitMinutes
	}
	return time.Duration(minutes) * time.Minute
}
