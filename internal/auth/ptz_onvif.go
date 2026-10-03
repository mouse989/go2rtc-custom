package auth

// ptz_onvif.go — ONVIF PTZ driver (Bosch, and any other ONVIF-conformant
// PTZ camera). Reuses the exact same WS-Security auth as the existing ONVIF
// snapshot path (pkg/onvif) and the same host/credentials resolution as
// every other camera_types.go driver (resolveStreamHostCreds) — confirmed
// to be the same account as RTSP/snapshot for every Bosch and Axis camera
// in this deployment, so no separate PTZ credentials are configured
// anywhere.
//
// A fresh onvif.Client costs a GetCapabilities + GetProfiles round trip,
// which would make every button press noticeably laggy if repeated on every
// move/stop — so resolved clients are cached per stream, the same tradeoff
// onvifCache (camera_types.go) makes for snapshot URIs. See
// setCameraTypeAssignments for this cache's invalidation.

import (
	"context"
	"fmt"
	"sync"

	"github.com/AlexxIT/go2rtc/pkg/onvif"
)

// ptzContinuousMoveTimeoutSec bounds how long a ContinuousMove keeps the
// camera moving if the browser never sends a matching Stop (dropped
// connection, crashed tab, etc. while a direction button was held).
const ptzContinuousMoveTimeoutSec = 3

type onvifPTZHandle struct {
	client *onvif.Client
	token  string
}

var (
	onvifPTZCacheMu sync.Mutex
	onvifPTZCache   = map[string]*onvifPTZHandle{} // stream name → resolved client+profile token
)

func getOnvifPTZHandle(ctx context.Context, streamName string) (*onvifPTZHandle, error) {
	onvifPTZCacheMu.Lock()
	h, ok := onvifPTZCache[streamName]
	onvifPTZCacheMu.Unlock()
	if ok {
		return h, nil
	}

	host, creds, err := resolveStreamHostCreds(streamName)
	if err != nil {
		return nil, err
	}

	var rawURL string
	if creds != nil {
		rawURL = fmt.Sprintf("http://%s@%s/?subtype=0", creds.String(), host)
	} else {
		rawURL = fmt.Sprintf("http://%s/?subtype=0", host)
	}

	type result struct {
		h   *onvifPTZHandle
		err error
	}
	ch := make(chan result, 1)

	go func() {
		client, err := onvif.NewClient(rawURL)
		if err != nil {
			ch <- result{err: fmt.Errorf("connect: %w", err)}
			return
		}
		tokens, err := client.GetProfilesTokens()
		if err != nil {
			ch <- result{err: fmt.Errorf("GetProfilesTokens: %w", err)}
			return
		}
		if len(tokens) == 0 {
			ch <- result{err: fmt.Errorf("camera reported no ONVIF media profiles")}
			return
		}
		ch <- result{h: &onvifPTZHandle{client: client, token: tokens[0]}}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		onvifPTZCacheMu.Lock()
		onvifPTZCache[streamName] = r.h
		onvifPTZCacheMu.Unlock()
		return r.h, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func onvifPTZMove(ctx context.Context, streamName string, pan, tilt, zoom float64) error {
	h, err := getOnvifPTZHandle(ctx, streamName)
	if err != nil {
		return err
	}
	_, err = h.client.ContinuousMove(h.token, pan, tilt, zoom, ptzContinuousMoveTimeoutSec)
	if err != nil {
		// A cached handle may have gone stale (camera rebooted, token
		// expired) — drop it so the next attempt re-resolves from scratch
		// instead of repeating the same failure forever.
		onvifPTZCacheMu.Lock()
		delete(onvifPTZCache, streamName)
		onvifPTZCacheMu.Unlock()
	}
	return err
}

func onvifPTZStop(ctx context.Context, streamName string) error {
	h, err := getOnvifPTZHandle(ctx, streamName)
	if err != nil {
		return err
	}
	_, err = h.client.Stop(h.token, true, true)
	if err != nil {
		onvifPTZCacheMu.Lock()
		delete(onvifPTZCache, streamName)
		onvifPTZCacheMu.Unlock()
	}
	return err
}
