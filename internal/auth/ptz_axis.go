package auth

// ptz_axis.go — Axis VAPIX HTTP PTZ driver. Chosen over ONVIF for Axis after
// hands-on testing: ONVIF Device Manager could not authenticate against the
// Axis cameras with the account in use, while that same account logs in and
// drives PTZ directly on the camera's own web UI — which is backed by this
// exact ptz.cgi endpoint. Plain authenticated HTTP GET, reusing the digest
// retry helper camera_types.go already has for snapshot fetches.
//
// Only the 3 functions this feature covers (pan, tilt, zoom) are
// implemented — continuouspantiltmove/continuouszoommove and nothing else
// of VAPIX's wider ptz.cgi surface (presets, focus, iris, ...).

import (
	"context"
	"fmt"
)

// axisPTZMove starts a continuous pan/tilt/zoom move. pan, tilt and zoom are
// each -1.0..1.0 (0 = that axis stays still) — VAPIX's own range is -100..100.
func axisPTZMove(ctx context.Context, streamName string, pan, tilt, zoom float64) error {
	host, creds, err := resolveStreamHostCreds(streamName)
	if err != nil {
		return err
	}
	rawURL := fmt.Sprintf(
		"http://%s/axis-cgi/com/ptz.cgi?continuouspantiltmove=%d,%d&continuouszoommove=%d",
		host, vapixSpeed(pan), vapixSpeed(tilt), vapixSpeed(zoom),
	)
	_, err = fetchHTTPWithDigestRetry(ctx, rawURL, creds)
	return err
}

// axisPTZStop halts an in-progress continuous move on every axis.
func axisPTZStop(ctx context.Context, streamName string) error {
	host, creds, err := resolveStreamHostCreds(streamName)
	if err != nil {
		return err
	}
	rawURL := fmt.Sprintf("http://%s/axis-cgi/com/ptz.cgi?continuouspantiltmove=0,0&continuouszoommove=0", host)
	_, err = fetchHTTPWithDigestRetry(ctx, rawURL, creds)
	return err
}

// axisPTZHome recalls VAPIX server preset 1 — the only preset this feature
// calls (see ptz.go's doc comment).
func axisPTZHome(ctx context.Context, streamName string) error {
	host, creds, err := resolveStreamHostCreds(streamName)
	if err != nil {
		return err
	}
	rawURL := fmt.Sprintf("http://%s/axis-cgi/com/ptz.cgi?gotoserverpresetno=1", host)
	_, err = fetchHTTPWithDigestRetry(ctx, rawURL, creds)
	return err
}

// axisPTZSaveHome overwrites VAPIX server preset 1 with the camera's
// current position. Preset *configuration* (saving/naming) was moved from
// ptz.cgi to ptzconfig.cgi on newer Axis firmware — unlike recall
// (gotoserverpresetno, a live-movement command that stayed on ptz.cgi) —
// so this talks to ptzconfig.cgi specifically, not ptz.cgi.
func axisPTZSaveHome(ctx context.Context, streamName string) error {
	host, creds, err := resolveStreamHostCreds(streamName)
	if err != nil {
		return err
	}
	rawURL := fmt.Sprintf("http://%s/axis-cgi/com/ptzconfig.cgi?setserverpresetno=1", host)
	_, err = fetchHTTPWithDigestRetry(ctx, rawURL, creds)
	return err
}

// vapixSpeed converts a -1.0..1.0 velocity to VAPIX's -100..100 integer scale.
func vapixSpeed(v float64) int {
	n := int(v * 100)
	if n > 100 {
		return 100
	}
	if n < -100 {
		return -100
	}
	return n
}
