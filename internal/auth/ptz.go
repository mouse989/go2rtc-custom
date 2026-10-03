package auth

// ptz.go — pan/tilt/zoom control, entirely independent of the snapshot
// module (camera_types.go's SnapshotPath/HTTPS/ONVIF/RTSP fields are never
// read here, and vice versa — a camera's snapshot method and its PTZ method
// are separate capabilities that happen to live on the same CameraType).
//
// Two drivers, chosen per Camera Type (CameraType.PTZDriver):
//   - "onvif"      — ptz_onvif.go, for Bosch (and any ONVIF-conformant PTZ
//     camera) — confirmed working via ONVIF Device Manager.
//   - "axis_vapix" — ptz_axis.go, for Axis — chosen over ONVIF after ONVIF
//     Device Manager failed to authenticate against these cameras while the
//     same account works fine controlling PTZ on the camera's own web UI
//     (VAPIX, which ptz_axis.go talks to directly).
//
// Both drivers resolve host/credentials the same way (resolveStreamHostCreds
// in camera_types.go) — the admin confirmed PTZ uses the same account as
// RTSP/snapshot for every camera in this deployment, so no separate PTZ
// credentials are configured anywhere.
//
// Access control happens in two independent places, deliberately
// redundant: api_ptz.go's handlers re-check both User.AllowPTZ and
// UserCanAccessStream/PTZEnabledForStream below on every single move/stop
// call — a client is never trusted just because it was once shown a PTZ
// control. Separately, /api/proxy/streams (proxy.go) only reports "ptz: true"
// for a stream when that same pair of checks already passes, so a viewer
// without AllowPTZ never sees the control exist in the first place.

import "context"

const (
	ptzDriverAxisVAPIX = "axis_vapix"
	ptzDriverONVIF     = "onvif"
)

// PTZEnabledForStream reports whether streamName's assigned Camera Type has
// PTZ turned on (regardless of any particular user's permission — callers
// combine this with User.AllowPTZ themselves, since the two checks serve
// different call sites: proxy.go's capability flag and api_ptz.go's
// handlers both need it, but computed slightly differently isn't worth a
// third wrapper).
func PTZEnabledForStream(streamName string) bool {
	ct := cameraTypeForStream(streamName)
	return ct != nil && ct.PTZEnabled && ct.PTZDriver != ""
}

// SendPTZMove starts a continuous pan/tilt/zoom move on streamName. pan,
// tilt and zoom are each -1.0..1.0 (0 = that axis stays still); callers
// should clamp user input to that range themselves (api_ptz.go does).
func SendPTZMove(ctx context.Context, streamName string, pan, tilt, zoom float64) error {
	ct := cameraTypeForStream(streamName)
	if ct == nil || !ct.PTZEnabled {
		return errPTZNotEnabled
	}
	switch ct.PTZDriver {
	case ptzDriverAxisVAPIX:
		return axisPTZMove(ctx, streamName, pan, tilt, zoom)
	case ptzDriverONVIF:
		return onvifPTZMove(ctx, streamName, pan, tilt, zoom)
	default:
		return errPTZNotEnabled
	}
}

// SendPTZStop halts any in-progress move on streamName.
func SendPTZStop(ctx context.Context, streamName string) error {
	ct := cameraTypeForStream(streamName)
	if ct == nil || !ct.PTZEnabled {
		return errPTZNotEnabled
	}
	switch ct.PTZDriver {
	case ptzDriverAxisVAPIX:
		return axisPTZStop(ctx, streamName)
	case ptzDriverONVIF:
		return onvifPTZStop(ctx, streamName)
	default:
		return errPTZNotEnabled
	}
}

type ptzError string

func (e ptzError) Error() string { return string(e) }

const errPTZNotEnabled = ptzError("PTZ not enabled for this camera")
