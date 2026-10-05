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
	"time"
)

const PathDevice = "/onvif/device_service"

type Client struct {
	url *url.URL

	deviceURL string
	mediaURL  string
	imaginURL string
	ptzURL    string
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
