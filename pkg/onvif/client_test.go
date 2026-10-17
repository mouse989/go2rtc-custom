package onvif

import "testing"

// TestResolveServiceURLPrefersAbsoluteXAddr covers the actual bug this was
// written to fix: a device (Bosch and similar) that reports an absolute
// https:// XAddr for an individual ONVIF service (PTZ/media/imaging) even
// though the base device service — and thus baseURL — was reached over
// plain http. GetPath alone would discard the scheme+host and silently
// rebuild the URL against baseURL, downgrading every PTZ SOAP call back to
// http and causing it to fail against an HTTPS-only service.
func TestResolveServiceURLPrefersAbsoluteXAddr(t *testing.T) {
	got := resolveServiceURL("https://192.168.1.50:8443/onvif/ptz_service", "http://192.168.1.50", "/onvif/ptz_service")
	want := "https://192.168.1.50:8443/onvif/ptz_service"
	if got != want {
		t.Errorf("resolveServiceURL() = %q, want %q (camera-reported absolute URL should win)", got, want)
	}
}

func TestResolveServiceURLFallsBackForRelativePath(t *testing.T) {
	got := resolveServiceURL("/onvif/ptz_service", "https://192.168.1.50", "/onvif/ptz_service")
	want := "https://192.168.1.50/onvif/ptz_service"
	if got != want {
		t.Errorf("resolveServiceURL() = %q, want %q (relative path should use baseURL's scheme/host)", got, want)
	}
}

func TestResolveServiceURLFallsBackForEmptyXAddr(t *testing.T) {
	got := resolveServiceURL("", "https://192.168.1.50", "/onvif/ptz_service")
	want := "https://192.168.1.50/onvif/ptz_service"
	if got != want {
		t.Errorf("resolveServiceURL() = %q, want %q (empty XAddr should use baseURL + default path)", got, want)
	}
}

func TestResolveServiceURLFallsBackForUnparseableXAddr(t *testing.T) {
	got := resolveServiceURL("://not a url", "https://192.168.1.50", "/onvif/ptz_service")
	want := "https://192.168.1.50/onvif/ptz_service"
	if got != want {
		t.Errorf("resolveServiceURL() = %q, want %q (unparseable XAddr should fall back)", got, want)
	}
}

func TestResolveServiceURLPreservesCameraReportedHTTPPort(t *testing.T) {
	// Same scheme as baseURL, but a DIFFERENT port — still must be taken
	// verbatim, since the bug this guards against isn't HTTPS-specific:
	// any mismatch between the device-service host:port and a given
	// service's own reported XAddr should be honored.
	got := resolveServiceURL("http://192.168.1.50:8080/onvif/media_service", "http://192.168.1.50", "/onvif/media_service")
	want := "http://192.168.1.50:8080/onvif/media_service"
	if got != want {
		t.Errorf("resolveServiceURL() = %q, want %q", got, want)
	}
}
