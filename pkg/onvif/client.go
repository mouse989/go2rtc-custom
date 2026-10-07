package onvif

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const PathDevice = "/onvif/device_service"

type Client struct {
	url *url.URL

	deviceURL string
	mediaURL  string
	imaginURL string
	ptzURL    string

	// videoSrcTokenMu/videoSrcToken cache videoSourceToken()'s result — the
	// only field on Client mutated after NewClient returns, so (unlike
	// every other field here) it needs its own lock: a cached *Client is
	// shared across requests for the same stream (see ptz_onvif.go's
	// onvifPTZCache), so two Focus calls can race here even though nothing
	// else on Client ever gets written twice.
	videoSrcTokenMu sync.Mutex
	videoSrcToken   string
}

func NewClient(rawURL string) (*Client, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}

	baseURL := "http://" + u.Host

	client := &Client{url: u}
	client.deviceURL = baseURL + GetPath(u.Path, PathDevice)

	b, err := client.DeviceRequest(DeviceGetCapabilities)
	if err != nil {
		return nil, err
	}

	s := FindTagValue(b, "Media.+?XAddr")
	client.mediaURL = baseURL + GetPath(s, "/onvif/media_service")

	s = FindTagValue(b, "Imaging.+?XAddr")
	client.imaginURL = baseURL + GetPath(s, "/onvif/imaging_service")

	// GetCapabilities was already called with Category=All above, so the
	// PTZ service address is already present in b — no extra round trip.
	s = FindTagValue(b, "PTZ.+?XAddr")
	client.ptzURL = baseURL + GetPath(s, "/onvif/ptz_service")

	return client, nil
}

func (c *Client) GetURI() (string, error) {
	query := c.url.Query()

	token := query.Get("subtype")

	// support empty
	if i := atoi(token); i >= 0 {
		tokens, err := c.GetProfilesTokens()
		if err != nil {
			return "", err
		}
		if i >= len(tokens) {
			return "", errors.New("onvif: wrong subtype")
		}
		token = tokens[i]
	}

	getUri := c.GetStreamUri
	if query.Has("snapshot") {
		getUri = c.GetSnapshotUri
	}

	b, err := getUri(token)
	if err != nil {
		return "", err
	}

	rawURL := FindTagValue(b, "Uri")
	rawURL = strings.TrimSpace(html.UnescapeString(rawURL))

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}

	if u.User == nil && c.url.User != nil {
		u.User = c.url.User
	}

	return u.String(), nil
}

func (c *Client) GetName() (string, error) {
	b, err := c.DeviceRequest(DeviceGetDeviceInformation)
	if err != nil {
		return "", err
	}

	return FindTagValue(b, "Manufacturer") + " " + FindTagValue(b, "Model"), nil
}

func (c *Client) GetProfilesTokens() ([]string, error) {
	b, err := c.MediaRequest(MediaGetProfiles)
	if err != nil {
		return nil, err
	}

	var tokens []string

	re := regexp.MustCompile(`Profiles.+?token="([^"]+)`)
	for _, s := range re.FindAllStringSubmatch(string(b), 10) {
		tokens = append(tokens, s[1])
	}

	return tokens, nil
}

func (c *Client) HasSnapshots() bool {
	b, err := c.GetServiceCapabilities()
	if err != nil {
		return false
	}
	return strings.Contains(string(b), `SnapshotUri="true"`)
}

func (c *Client) GetProfile(token string) ([]byte, error) {
	return c.Request(
		c.mediaURL, `<trt:GetProfile><trt:ProfileToken>`+token+`</trt:ProfileToken></trt:GetProfile>`,
	)
}

func (c *Client) GetVideoSourceConfiguration(token string) ([]byte, error) {
	return c.Request(c.mediaURL, `<trt:GetVideoSourceConfiguration>
	<trt:ConfigurationToken>`+token+`</trt:ConfigurationToken>
</trt:GetVideoSourceConfiguration>`)
}

func (c *Client) GetStreamUri(token string) ([]byte, error) {
	return c.Request(c.mediaURL, `<trt:GetStreamUri>
	<trt:StreamSetup>
		<tt:Stream>RTP-Unicast</tt:Stream>
		<tt:Transport><tt:Protocol>RTSP</tt:Protocol></tt:Transport>
	</trt:StreamSetup>
	<trt:ProfileToken>`+token+`</trt:ProfileToken>
</trt:GetStreamUri>`)
}

func (c *Client) GetSnapshotUri(token string) ([]byte, error) {
	return c.Request(
		c.imaginURL, `<trt:GetSnapshotUri><trt:ProfileToken>`+token+`</trt:ProfileToken></trt:GetSnapshotUri>`,
	)
}

// GetVideoSources returns the raw GetVideoSourcesResponse — used by
// videoSourceToken to resolve the VideoSourceToken that Focus move/stop
// need, since ONVIF ties Focus to a video source (Imaging service), not
// the media ProfileToken pan/tilt/zoom use.
func (c *Client) GetVideoSources() ([]byte, error) {
	return c.MediaRequest(MediaGetVideoSources)
}

// videoSourceToken resolves the first reported VideoSourceToken, assuming
// a single video source — true for every PTZ/fixed camera this talks to;
// a multi-sensor ONVIF device would need per-sensor selection this doesn't
// attempt. Resolved lazily (only once Focus is actually used — nothing
// else on Client needs it) and cached, so repeated focus presses during
// one cached handle's lifetime (see ptz_onvif.go) don't re-fetch it.
func (c *Client) videoSourceToken() (string, error) {
	c.videoSrcTokenMu.Lock()
	defer c.videoSrcTokenMu.Unlock()
	if c.videoSrcToken != "" {
		return c.videoSrcToken, nil
	}

	b, err := c.GetVideoSources()
	if err != nil {
		return "", err
	}
	re := regexp.MustCompile(`VideoSources.+?token="([^"]+)`)
	m := re.FindSubmatch(b)
	if len(m) != 2 {
		return "", errors.New("onvif: no video source token found")
	}
	c.videoSrcToken = string(m[1])
	return c.videoSrcToken, nil
}

// ContinuousFocusMove starts a continuous focus move at the given speed
// (-1.0..1.0; 0 would just leave it still) via the Imaging service — ONVIF
// keeps Focus out of ContinuousMove (which only covers pan/tilt/zoom)
// since it's a lens/imaging setting, not one of the camera's positional
// axes, so it gets its own service, endpoint and Stop operation.
func (c *Client) ContinuousFocusMove(speed float64) ([]byte, error) {
	token, err := c.videoSourceToken()
	if err != nil {
		return nil, err
	}
	return c.ImagingRequest(fmt.Sprintf(
		`<timg:Move>
	<timg:VideoSourceToken>%s</timg:VideoSourceToken>
	<timg:Focus>
		<tt:Continuous>
			<tt:Speed>%g</tt:Speed>
		</tt:Continuous>
	</timg:Focus>
</timg:Move>`,
		token, speed,
	))
}

// StopFocus halts an in-progress ContinuousFocusMove. Deliberately separate
// from PTZ's Stop (which only ever covers pan/tilt/zoom) — Imaging's Stop
// is its own SOAP operation against its own service endpoint.
func (c *Client) StopFocus() ([]byte, error) {
	token, err := c.videoSourceToken()
	if err != nil {
		return nil, err
	}
	return c.ImagingRequest(fmt.Sprintf(
		`<timg:Stop><timg:VideoSourceToken>%s</timg:VideoSourceToken></timg:Stop>`,
		token,
	))
}

func (c *Client) ImagingRequest(body string) ([]byte, error) {
	return c.Request(c.imaginURL, body)
}

// ContinuousMove starts a pan/tilt/zoom move at the given velocities (each
// -1.0 to 1.0; 0 leaves that axis still) that continues until Stop is
// called. timeoutSec bounds how long the device keeps moving on its own if
// no Stop ever arrives (e.g. the controlling browser tab loses its network
// mid-gesture) — ONVIF devices default to no timeout at all without this,
// so it is always sent, never omitted.
func (c *Client) ContinuousMove(token string, pan, tilt, zoom float64, timeoutSec int) ([]byte, error) {
	return c.PTZRequest(fmt.Sprintf(
		`<tptz:ContinuousMove>
	<tptz:ProfileToken>%s</tptz:ProfileToken>
	<tptz:Velocity>
		<tt:PanTilt x="%g" y="%g"/>
		<tt:Zoom x="%g"/>
	</tptz:Velocity>
	<tptz:Timeout>PT%dS</tptz:Timeout>
</tptz:ContinuousMove>`,
		token, pan, tilt, zoom, timeoutSec,
	))
}

// GotoPreset recalls a saved preset position (as set up on the camera
// itself, outside this app). presetToken is an ONVIF preset token, not
// necessarily a plain number — see GetPresetTokens when the camera doesn't
// use "1" as the literal token for its first preset. c.Request only treats
// a non-200 HTTP status as failure, so this additionally checks the body
// for a SOAP Fault — some devices answer an unknown/invalid preset token
// with HTTP 200 and a Fault in the body, which would otherwise look like a
// silent, do-nothing "success".
func (c *Client) GotoPreset(profileToken, presetToken string) ([]byte, error) {
	b, err := c.PTZRequest(fmt.Sprintf(
		`<tptz:GotoPreset>
	<tptz:ProfileToken>%s</tptz:ProfileToken>
	<tptz:PresetToken>%s</tptz:PresetToken>
</tptz:GotoPreset>`,
		profileToken, presetToken,
	))
	if err != nil {
		return b, err
	}
	if bytes.Contains(b, []byte("Fault")) {
		return b, fmt.Errorf("onvif: GotoPreset(%s) fault: %s", presetToken, b)
	}
	return b, nil
}

// SetPreset saves the device's current pan/tilt/zoom position as a preset.
// Per the ONVIF PTZ spec, passing a presetToken OVERWRITES that existing
// preset's position; an empty presetToken instead creates a new preset
// (not used by this app — see ptz_onvif.go's onvifPTZSaveHome, which
// always passes "1"). Same HTTP-200-with-a-Fault-body check as GotoPreset.
func (c *Client) SetPreset(profileToken, presetToken, presetName string) ([]byte, error) {
	b, err := c.PTZRequest(fmt.Sprintf(
		`<tptz:SetPreset>
	<tptz:ProfileToken>%s</tptz:ProfileToken>
	<tptz:PresetToken>%s</tptz:PresetToken>
	<tptz:PresetName>%s</tptz:PresetName>
</tptz:SetPreset>`,
		profileToken, presetToken, presetName,
	))
	if err != nil {
		return b, err
	}
	if bytes.Contains(b, []byte("Fault")) {
		return b, fmt.Errorf("onvif: SetPreset(%s) fault: %s", presetToken, b)
	}
	return b, nil
}

// GetPresetTokens returns every saved preset's token for the given
// profile, in whatever order the device reports them — used only as a
// fallback when GotoPreset(token, "1") fails, to find the real token for
// the camera's first/home preset on a device that doesn't number its
// tokens "1", "2", ...
func (c *Client) GetPresetTokens(profileToken string) ([]string, error) {
	b, err := c.PTZRequest(fmt.Sprintf(
		`<tptz:GetPresets><tptz:ProfileToken>%s</tptz:ProfileToken></tptz:GetPresets>`,
		profileToken,
	))
	if err != nil {
		return nil, err
	}

	var tokens []string
	re := regexp.MustCompile(`Preset.+?token="([^"]+)`)
	for _, s := range re.FindAllStringSubmatch(string(b), 50) {
		tokens = append(tokens, s[1])
	}
	return tokens, nil
}

// Stop halts an in-progress ContinuousMove on the given axes.
func (c *Client) Stop(token string, panTilt, zoom bool) ([]byte, error) {
	return c.PTZRequest(fmt.Sprintf(
		`<tptz:Stop>
	<tptz:ProfileToken>%s</tptz:ProfileToken>
	<tptz:PanTilt>%t</tptz:PanTilt>
	<tptz:Zoom>%t</tptz:Zoom>
</tptz:Stop>`,
		token, panTilt, zoom,
	))
}

func (c *Client) PTZRequest(body string) ([]byte, error) {
	return c.Request(c.ptzURL, body)
}

func (c *Client) GetServiceCapabilities() ([]byte, error) {
	// some cameras answer GetServiceCapabilities for media only for path = "/onvif/media"
	return c.Request(
		c.mediaURL, `<trt:GetServiceCapabilities />`,
	)
}

func (c *Client) DeviceRequest(operation string) ([]byte, error) {
	switch operation {
	case DeviceGetServices:
		operation = `<tds:GetServices><tds:IncludeCapability>true</tds:IncludeCapability></tds:GetServices>`
	case DeviceGetCapabilities:
		operation = `<tds:GetCapabilities><tds:Category>All</tds:Category></tds:GetCapabilities>`
	default:
		operation = `<tds:` + operation + `/>`
	}
	return c.Request(c.deviceURL, operation)
}

func (c *Client) MediaRequest(operation string) ([]byte, error) {
	operation = `<trt:` + operation + `/>`
	return c.Request(c.mediaURL, operation)
}

func (c *Client) Request(url, body string) ([]byte, error) {
	if url == "" {
		return nil, errors.New("onvif: unsupported service")
	}

	e := NewEnvelopeWithUser(c.url.User)
	e.Append(body)

	client := &http.Client{Timeout: 10 * time.Second}
	res, err := client.Post(url, `application/soap+xml;charset=utf-8`, bytes.NewReader(e.Bytes()))
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, errors.New("onvif: wrong response " + res.Status)
	}

	return io.ReadAll(res.Body)
}
