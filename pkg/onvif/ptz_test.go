package onvif

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// capabilitiesXML advertises Media/Imaging/PTZ all on the test server
// itself — NewClient only keeps the PATH portion of each XAddr (see
// GetPath), so the host in these XAddr values is never actually dialed.
const capabilitiesXML = `<?xml version="1.0" encoding="utf-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tt="http://www.onvif.org/ver10/schema" xmlns:tds="http://www.onvif.org/ver10/device/wsdl"><s:Body><tds:GetCapabilitiesResponse><tds:Capabilities>
	<tt:Media><tt:XAddr>http://ignored/onvif/media_service</tt:XAddr></tt:Media>
	<tt:Imaging><tt:XAddr>http://ignored/onvif/imaging_service</tt:XAddr></tt:Imaging>
	<tt:PTZ><tt:XAddr>http://ignored/onvif/ptz_service</tt:XAddr></tt:PTZ>
</tds:Capabilities></tds:GetCapabilitiesResponse></s:Body></s:Envelope>`

func newTestClient(t *testing.T, handler func(body string) (status int, resp string)) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/device_service") || r.URL.Path == "/" {
			w.Write([]byte(capabilitiesXML))
			return
		}
		body, _ := io.ReadAll(r.Body)
		status, resp := handler(string(body))
		w.WriteHeader(status)
		w.Write([]byte(resp))
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(fmt.Sprintf("http://admin:pass@%s/?subtype=0", strings.TrimPrefix(srv.URL, "http://")))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestGotoPresetDirectTokenSucceeds(t *testing.T) {
	c := newTestClient(t, func(body string) (int, string) {
		if !strings.Contains(body, "<tptz:PresetToken>1</tptz:PresetToken>") {
			t.Fatalf("expected request for PresetToken 1, got: %s", body)
		}
		return http.StatusOK, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><tptz:GotoPresetResponse/></s:Body></s:Envelope>`
	})

	if _, err := c.GotoPreset("profile1", "1"); err != nil {
		t.Fatalf("GotoPreset(1) should succeed, got: %v", err)
	}
}

func TestGotoPresetDetectsSOAPFaultDespiteHTTP200(t *testing.T) {
	c := newTestClient(t, func(body string) (int, string) {
		// A device that doesn't recognize PresetToken "1" but still answers
		// HTTP 200 with a SOAP Fault in the body — must NOT look like success.
		return http.StatusOK, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><s:Fault><s:Code><s:Value>s:Receiver</s:Value></s:Code><s:Reason><s:Text>InvalidArgVal</s:Text></s:Reason></s:Fault></s:Body></s:Envelope>`
	})

	if _, err := c.GotoPreset("profile1", "1"); err == nil {
		t.Fatal("expected an error when the response body contains a SOAP Fault, got nil")
	}
}

func TestSetPresetOverwritesExistingToken(t *testing.T) {
	c := newTestClient(t, func(body string) (int, string) {
		if !strings.Contains(body, "<tptz:PresetToken>1</tptz:PresetToken>") {
			t.Fatalf("expected SetPreset request to carry PresetToken 1 (overwrite, not create), got: %s", body)
		}
		if !strings.Contains(body, "<tptz:PresetName>Home</tptz:PresetName>") {
			t.Fatalf("expected PresetName 'Home', got: %s", body)
		}
		return http.StatusOK, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><tptz:SetPresetResponse/></s:Body></s:Envelope>`
	})

	if _, err := c.SetPreset("profile1", "1", "Home"); err != nil {
		t.Fatalf("SetPreset should succeed, got: %v", err)
	}
}

func TestSetPresetDetectsSOAPFaultDespiteHTTP200(t *testing.T) {
	c := newTestClient(t, func(body string) (int, string) {
		return http.StatusOK, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><s:Fault><s:Code><s:Value>s:Receiver</s:Value></s:Code><s:Reason><s:Text>InvalidArgVal</s:Text></s:Reason></s:Fault></s:Body></s:Envelope>`
	})

	if _, err := c.SetPreset("profile1", "1", "Home"); err == nil {
		t.Fatal("expected an error when the response body contains a SOAP Fault, got nil")
	}
}

func TestContinuousFocusMoveResolvesVideoSourceTokenAndSendsSpeed(t *testing.T) {
	var moveBody string
	c := newTestClient(t, func(body string) (int, string) {
		switch {
		case strings.Contains(body, "GetVideoSources"):
			return http.StatusOK, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><trt:GetVideoSourcesResponse>
				<tt:VideoSources token="VideoSource_1"><tt:Framerate>25</tt:Framerate></tt:VideoSources>
			</trt:GetVideoSourcesResponse></s:Body></s:Envelope>`
		case strings.Contains(body, "timg:Move"):
			moveBody = body
			return http.StatusOK, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><timg:MoveResponse/></s:Body></s:Envelope>`
		default:
			t.Fatalf("unexpected request: %s", body)
			return http.StatusInternalServerError, ""
		}
	})

	if _, err := c.ContinuousFocusMove(0.75); err != nil {
		t.Fatalf("ContinuousFocusMove: %v", err)
	}
	if !strings.Contains(moveBody, "<timg:VideoSourceToken>VideoSource_1</timg:VideoSourceToken>") {
		t.Fatalf("expected Move request to carry the resolved VideoSourceToken, got: %s", moveBody)
	}
	if !strings.Contains(moveBody, "<tt:Speed>0.75</tt:Speed>") {
		t.Fatalf("expected Move request to carry Speed=0.75, got: %s", moveBody)
	}
}

func TestStopFocusUsesVideoSourceToken(t *testing.T) {
	var stopBody string
	c := newTestClient(t, func(body string) (int, string) {
		switch {
		case strings.Contains(body, "GetVideoSources"):
			return http.StatusOK, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><trt:GetVideoSourcesResponse>
				<tt:VideoSources token="VideoSource_1"/>
			</trt:GetVideoSourcesResponse></s:Body></s:Envelope>`
		case strings.Contains(body, "timg:Stop"):
			stopBody = body
			return http.StatusOK, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><timg:StopResponse/></s:Body></s:Envelope>`
		default:
			t.Fatalf("unexpected request: %s", body)
			return http.StatusInternalServerError, ""
		}
	})

	if _, err := c.StopFocus(); err != nil {
		t.Fatalf("StopFocus: %v", err)
	}
	if !strings.Contains(stopBody, "<timg:VideoSourceToken>VideoSource_1</timg:VideoSourceToken>") {
		t.Fatalf("expected Stop request to carry the resolved VideoSourceToken, got: %s", stopBody)
	}
}

// Confirms the lazy-cache behavior videoSourceToken()'s doc comment
// promises: a second Focus call during the same Client's lifetime must not
// re-fetch GetVideoSources.
func TestVideoSourceTokenIsCachedAfterFirstResolve(t *testing.T) {
	getVideoSourcesCalls := 0
	c := newTestClient(t, func(body string) (int, string) {
		switch {
		case strings.Contains(body, "GetVideoSources"):
			getVideoSourcesCalls++
			return http.StatusOK, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><trt:GetVideoSourcesResponse>
				<tt:VideoSources token="VideoSource_1"/>
			</trt:GetVideoSourcesResponse></s:Body></s:Envelope>`
		case strings.Contains(body, "timg:Move"), strings.Contains(body, "timg:Stop"):
			return http.StatusOK, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body/></s:Envelope>`
		default:
			t.Fatalf("unexpected request: %s", body)
			return http.StatusInternalServerError, ""
		}
	})

	if _, err := c.ContinuousFocusMove(1); err != nil {
		t.Fatalf("ContinuousFocusMove: %v", err)
	}
	if _, err := c.StopFocus(); err != nil {
		t.Fatalf("StopFocus: %v", err)
	}
	if getVideoSourcesCalls != 1 {
		t.Fatalf("expected GetVideoSources to be called exactly once (cached after that), got %d calls", getVideoSourcesCalls)
	}
}

func TestVideoSourceTokenErrorsWhenNoneReported(t *testing.T) {
	c := newTestClient(t, func(body string) (int, string) {
		if strings.Contains(body, "GetVideoSources") {
			return http.StatusOK, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><trt:GetVideoSourcesResponse/></s:Body></s:Envelope>`
		}
		t.Fatalf("unexpected request: %s", body)
		return http.StatusInternalServerError, ""
	})

	if _, err := c.ContinuousFocusMove(1); err == nil {
		t.Fatal("expected an error when the camera reports no video sources")
	}
}

func TestGetPresetTokensParsesMultiplePresets(t *testing.T) {
	c := newTestClient(t, func(body string) (int, string) {
		if !strings.Contains(body, "GetPresets") {
			t.Fatalf("expected a GetPresets request, got: %s", body)
		}
		return http.StatusOK, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><tptz:GetPresetsResponse>
			<tptz:Preset token="7"><tt:Name>Home</tt:Name></tptz:Preset>
			<tptz:Preset token="8"><tt:Name>Gate</tt:Name></tptz:Preset>
		</tptz:GetPresetsResponse></s:Body></s:Envelope>`
	})

	tokens, err := c.GetPresetTokens("profile1")
	if err != nil {
		t.Fatalf("GetPresetTokens: %v", err)
	}
	if len(tokens) != 2 || tokens[0] != "7" || tokens[1] != "8" {
		t.Fatalf("expected tokens [7 8], got %v", tokens)
	}
}
