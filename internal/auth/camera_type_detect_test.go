package auth

import (
	"reflect"
	"testing"
)

func withMockStreams(t *testing.T, sources map[string][]string) {
	t.Helper()
	savedNames, savedSources := getStreamNames, getStreamSources

	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	getStreamNames = func() []string { return names }
	getStreamSources = func(name string) []string { return sources[name] }

	t.Cleanup(func() {
		getStreamNames = savedNames
		getStreamSources = savedSources
	})
}

func TestNormalizeRTSPPatternIgnoresHostCredsAndDigits(t *testing.T) {
	a, err := normalizeRTSPPattern("rtsp://admin:pass1@10.0.1.5:554/Streaming/Channels/101/picture")
	if err != nil {
		t.Fatalf("normalizeRTSPPattern: %v", err)
	}
	b, err := normalizeRTSPPattern("rtsp://root:other@10.0.9.200:8554/Streaming/Channels/201/picture")
	if err != nil {
		t.Fatalf("normalizeRTSPPattern: %v", err)
	}
	if a != b {
		t.Fatalf("expected same shape for same vendor URLs with different host/creds/channel, got %q vs %q", a, b)
	}
}

func TestNormalizeRTSPPatternDistinguishesDifferentShapes(t *testing.T) {
	hik, _ := normalizeRTSPPattern("rtsp://admin:pass@10.0.1.5:554/Streaming/Channels/101/picture")
	dahua, _ := normalizeRTSPPattern("rtsp://admin:pass@10.0.1.5:554/cam/realmonitor?channel=1&subtype=0")
	if hik == dahua {
		t.Fatalf("expected different shapes for structurally different URLs, both got %q", hik)
	}
}

func TestNormalizeRTSPPatternIncludesQueryString(t *testing.T) {
	a, _ := normalizeRTSPPattern("rtsp://u:p@10.0.0.1:554/cam/realmonitor?channel=1&subtype=0")
	b, _ := normalizeRTSPPattern("rtsp://u:p@10.0.0.2:554/cam/realmonitor?channel=2&subtype=1")
	if a != b {
		t.Fatalf("expected same shape ignoring only the digits within the query, got %q vs %q", a, b)
	}
}

func TestDetectVendorSignatureRecognizesKnownVendors(t *testing.T) {
	cases := map[string]string{
		"rtsp://admin:pass@10.0.0.1:554/Streaming/Channels/101/picture":  "Hikvision",
		"rtsp://admin:pass@10.0.0.1:554/cam/realmonitor?channel=1":       "Dahua",
		"rtsp://root:pass@10.0.0.1/axis-media/media.amp?videocodec=h264": "Axis",
		"rtsp://admin:pass@10.0.0.1:554/h264Preview_01_main":             "Reolink",
		"rtsp://user:pass@10.0.0.1:554/onvif1":                           "ONVIF Generic",
	}
	for url, want := range cases {
		sig := detectVendorSignature(url)
		if sig == nil {
			t.Errorf("%s: expected vendor %s, got no match", url, want)
			continue
		}
		if sig.Vendor != want {
			t.Errorf("%s: expected vendor %s, got %s", url, want, sig.Vendor)
		}
	}
}

func TestDetectVendorSignatureUnknownReturnsNil(t *testing.T) {
	if sig := detectVendorSignature("rtsp://user:pass@10.0.0.1:554/some/totally/custom/path"); sig != nil {
		t.Fatalf("expected no vendor match, got %+v", sig)
	}
}

func TestDetectCameraTypeFindsSiblingStreamsAndSuggestsVendor(t *testing.T) {
	withMockStreams(t, map[string][]string{
		"gate-cam-1": {"rtsp://admin:pass1@10.0.1.5:554/Streaming/Channels/101/picture"},
		"gate-cam-2": {"rtsp://admin:pass2@10.0.1.6:554/Streaming/Channels/101/picture"},
		"lobby-cam":  {"rtsp://root:x@10.0.2.9:554/cam/realmonitor?channel=1&subtype=0"}, // different vendor
		"broken-cam": {"not-a-url-at-all"},                                               // must not crash matching
		"no-src-cam": {},                                                                 // must not crash matching
	})

	det, err := DetectCameraType("rtsp://admin:pass3@10.0.1.7:554/Streaming/Channels/201/picture")
	if err != nil {
		t.Fatalf("DetectCameraType: %v", err)
	}
	if det.Vendor != "Hikvision" {
		t.Fatalf("expected vendor Hikvision, got %q", det.Vendor)
	}
	if det.SnapshotPath == "" {
		t.Fatal("expected a suggested snapshot path")
	}
	want := []string{"gate-cam-1", "gate-cam-2"}
	if !reflect.DeepEqual(det.MatchingStreams, want) {
		t.Fatalf("expected matching streams %v, got %v", want, det.MatchingStreams)
	}
}

func TestDetectCameraTypeUnknownVendorStillMatchesByShape(t *testing.T) {
	withMockStreams(t, map[string][]string{
		"cam-a": {"rtsp://u:p@10.0.0.1:554/some/custom/path/1"},
		"cam-b": {"rtsp://u:p@10.0.0.2:554/some/custom/path/2"},
	})

	det, err := DetectCameraType("rtsp://u:p@10.0.0.3:554/some/custom/path/3")
	if err != nil {
		t.Fatalf("DetectCameraType: %v", err)
	}
	if det.Vendor != "" {
		t.Fatalf("expected no vendor guess for an unrecognized shape, got %q", det.Vendor)
	}
	want := []string{"cam-a", "cam-b"}
	if !reflect.DeepEqual(det.MatchingStreams, want) {
		t.Fatalf("expected matching streams %v, got %v", want, det.MatchingStreams)
	}
}

func TestDetectCameraTypeNoMatchesReturnsEmptySliceNotNil(t *testing.T) {
	withMockStreams(t, map[string][]string{
		"other-cam": {"rtsp://u:p@10.0.0.1:554/totally/different/shape"},
	})

	det, err := DetectCameraType("rtsp://u:p@10.0.0.9:554/Streaming/Channels/101/picture")
	if err != nil {
		t.Fatalf("DetectCameraType: %v", err)
	}
	if det.MatchingStreams == nil {
		t.Fatal("expected an empty slice, not nil (JSON should encode [] not null)")
	}
	if len(det.MatchingStreams) != 0 {
		t.Fatalf("expected no matches, got %v", det.MatchingStreams)
	}
}
