package auth

// camera_type_detect.go — guess a camera's vendor/snapshot convention from
// one sample RTSP URL, and find every other stream already configured in
// go2rtc.yaml whose source URL has the same "shape".
//
// Most camera fleets are configured a handful of cameras at a time from the
// same vendor/model, so their RTSP URLs share an identical path structure —
// only the host, port, credentials and channel/stream number differ, e.g.:
//
//	rtsp://admin:pass@10.0.1.5:554/Streaming/Channels/101/picture
//	rtsp://admin:pass2@10.0.2.9:554/Streaming/Channels/201/picture
//
// Given one such URL, normalizeRTSPPattern reduces it to a shape ("path with
// every run of digits collapsed to one placeholder") that both of the above
// share, so every OTHER stream in the config with that same shape can be
// found by comparing their configured source URLs the same way — entirely
// server-side, since those URLs carry camera passwords that must never reach
// the browser. Only the one sample URL the admin already knows and pastes in
// is ever sent from the client.

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
)

var patternDigitsRe = regexp.MustCompile(`\d+`)

// normalizeRTSPPattern reduces a stream source URL to its path+query
// "shape": scheme, userinfo, host and port are stripped (those are always
// per-camera, never part of a vendor/model's URL convention), and every run
// of digits is collapsed to a single placeholder so a differing channel
// number or IP octet doesn't count as a different shape.
func normalizeRTSPPattern(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	p := u.Path
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return patternDigitsRe.ReplaceAllString(p, "#"), nil
}

// cameraVendorSignature recognizes a common vendor/model family from its
// RTSP URL path convention, to suggest a starting point — the same vendors
// already offered as one-click templates in the Camera Type dialog
// (www/admin.html's Quick Fill buttons), recognized automatically instead of
// requiring the admin to already know which button matches their camera.
type cameraVendorSignature struct {
	Vendor       string
	SnapshotPath string
	Onvif        bool
	match        func(path string) bool
}

var cameraVendorSignatures = []cameraVendorSignature{
	{
		Vendor:       "Hikvision",
		SnapshotPath: "/ISAPI/Streaming/channels/101/picture",
		match: func(path string) bool {
			return strings.Contains(path, "/streaming/channels/")
		},
	},
	{
		Vendor:       "Dahua",
		SnapshotPath: "/cgi-bin/snapshot.cgi",
		match: func(path string) bool {
			return strings.Contains(path, "/cam/realmonitor")
		},
	},
	{
		Vendor:       "Axis",
		SnapshotPath: "/axis-cgi/jpg/image.cgi",
		match: func(path string) bool {
			return strings.Contains(path, "/axis-media/media.amp") || strings.Contains(path, "/mpeg4/media.amp")
		},
	},
	{
		Vendor:       "Reolink",
		SnapshotPath: "/cgi-bin/api.cgi?cmd=Snap&channel=0&rs=snapshot",
		match: func(path string) bool {
			return strings.Contains(path, "/h264preview_")
		},
	},
	{
		Vendor: "ONVIF Generic",
		Onvif:  true,
		match: func(path string) bool {
			return strings.Contains(path, "/onvif")
		},
	},
}

// detectVendorSignature matches a sample URL's path against known vendor
// conventions, case-insensitively. Returns nil when nothing matches — the
// pattern-based stream matching in DetectCameraType still works without a
// named vendor, it just can't suggest a snapshot path or name.
func detectVendorSignature(rawURL string) *cameraVendorSignature {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	path := strings.ToLower(u.Path)
	for i := range cameraVendorSignatures {
		if cameraVendorSignatures[i].match(path) {
			return &cameraVendorSignatures[i]
		}
	}
	return nil
}

// CameraTypeDetection is the result of matching one sample stream source URL
// against every other configured stream.
type CameraTypeDetection struct {
	Pattern         string   `json:"pattern"`                // the normalized shape, for display/debugging
	Vendor          string   `json:"vendor,omitempty"`       // recognized vendor name, empty if unrecognized
	SnapshotPath    string   `json:"snapshotPath,omitempty"` // suggested Camera Type snapshot_path
	Onvif           bool     `json:"onvif"`                  // suggest ONVIF auto-discovery instead of a fixed path
	MatchingStreams []string `json:"matchingStreams"`        // every configured stream sharing this URL shape, sorted
}

// DetectCameraType parses sampleURL, matches it against known vendor
// conventions, and finds every configured stream whose own source URL(s)
// reduce to the same normalized shape (see normalizeRTSPPattern) —
// including sampleURL's own stream, if it happens to be one of the
// configured ones, since re-detecting from an already-typed camera is a
// reasonable way to find its siblings.
func DetectCameraType(sampleURL string) (*CameraTypeDetection, error) {
	pattern, err := normalizeRTSPPattern(sampleURL)
	if err != nil {
		return nil, err
	}

	det := &CameraTypeDetection{Pattern: pattern}
	if sig := detectVendorSignature(sampleURL); sig != nil {
		det.Vendor = sig.Vendor
		det.SnapshotPath = sig.SnapshotPath
		det.Onvif = sig.Onvif
	}

	det.MatchingStreams = []string{}
	if getStreamNames == nil || getStreamSources == nil {
		return det, nil
	}

	matches := []string{}
	for _, name := range getStreamNames() {
		for _, src := range getStreamSources(name) {
			if !strings.Contains(src, "://") {
				continue
			}
			p, err := normalizeRTSPPattern(src)
			if err == nil && p == pattern {
				matches = append(matches, name)
				break
			}
		}
	}
	sort.Strings(matches)
	det.MatchingStreams = matches
	return det, nil
}
