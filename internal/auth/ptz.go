package auth

// ptz.go — pan/tilt/zoom/focus control, entirely independent of the
// snapshot module (camera_types.go's SnapshotPath/HTTPS/ONVIF/RTSP fields
// are never read here, and vice versa — a camera's snapshot method and its
// PTZ method are separate capabilities that happen to live on the same
// CameraType). Focus (SendPTZFocus/SendPTZFocusStop) reuses the same
// PTZEnabled/PTZDriver gate as pan/tilt/zoom rather than a capability flag
// of its own — if a specific camera's lens has no manual focus override,
// the camera itself rejects or no-ops the command, same tradeoff already
// accepted for zoom on a fixed-zoom lens.
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

// SendPTZHome recalls preset 1 — the camera's "home" position, set up
// ahead of time on the camera itself. This is the only preset this feature
// ever calls: the admin only wants a quick way back to home, not a preset
// browser for every saved angle, so there's no preset-number parameter
// anywhere in this path (API, drivers, or the UI's single house-icon
// button).
func SendPTZHome(ctx context.Context, streamName string) error {
	ct := cameraTypeForStream(streamName)
	if ct == nil || !ct.PTZEnabled {
		return errPTZNotEnabled
	}
	switch ct.PTZDriver {
	case ptzDriverAxisVAPIX:
		return axisPTZHome(ctx, streamName)
	case ptzDriverONVIF:
		return onvifPTZHome(ctx, streamName)
	default:
		return errPTZNotEnabled
	}
}

// SendPTZSaveHome overwrites preset 1 with the camera's current position —
// the save-side counterpart to SendPTZHome. Confirming with the operator
// before calling this (so they understand the previous home position is
// gone) is the caller's job (api_ptz.go's handler trusts the request it
// receives; the confirmation dialog lives in the browser, in
// www/ptz-control.js).
func SendPTZSaveHome(ctx context.Context, streamName string) error {
	ct := cameraTypeForStream(streamName)
	if ct == nil || !ct.PTZEnabled {
		return errPTZNotEnabled
	}
	switch ct.PTZDriver {
	case ptzDriverAxisVAPIX:
		return axisPTZSaveHome(ctx, streamName)
	case ptzDriverONVIF:
		return onvifPTZSaveHome(ctx, streamName)
	default:
		return errPTZNotEnabled
	}
}

// SendPTZFocus starts a continuous focus move on streamName. speed is
// -1.0..1.0 (0 leaves focus still) — negative means near, positive means
// far, same -1..1 convention as SendPTZMove's axes, and the same clamping
// expectation (callers clamp user input; api_ptz.go does).
func SendPTZFocus(ctx context.Context, streamName string, speed float64) error {
	ct := cameraTypeForStream(streamName)
	if ct == nil || !ct.PTZEnabled {
		return errPTZNotEnabled
	}
	switch ct.PTZDriver {
	case ptzDriverAxisVAPIX:
		return axisPTZFocus(ctx, streamName, speed)
	case ptzDriverONVIF:
		return onvifPTZFocus(ctx, streamName, speed)
	default:
		return errPTZNotEnabled
	}
}

// SendPTZFocusStop halts an in-progress continuous focus move started by
// SendPTZFocus. Kept separate from SendPTZStop (which only ever covers
// pan/tilt/zoom) because ONVIF genuinely treats focus as a different
// service (Imaging, not PTZ) with its own Stop operation — mirroring that
// split here keeps each driver's Stop call matched to the axis it started.
func SendPTZFocusStop(ctx context.Context, streamName string) error {
	ct := cameraTypeForStream(streamName)
	if ct == nil || !ct.PTZEnabled {
		return errPTZNotEnabled
	}
	switch ct.PTZDriver {
	case ptzDriverAxisVAPIX:
		return axisPTZFocusStop(ctx, streamName)
	case ptzDriverONVIF:
		return onvifPTZFocusStop(ctx, streamName)
	default:
		return errPTZNotEnabled
	}
}

type ptzError string

func (e ptzError) Error() string { return string(e) }

const errPTZNotEnabled = ptzError("PTZ not enabled for this camera")
