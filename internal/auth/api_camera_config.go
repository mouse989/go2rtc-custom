package auth

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

func registerCameraConfigHandler() {
	http.HandleFunc("/api/camera-config/apply", cameraConfigApplyHandler)
	http.HandleFunc("/api/camera-config/streams", cameraConfigStreamsHandler)
}

// camStreamInfo describes a configured go2rtc stream whose source is an rtsp://
// URL, with host + credentials parsed out so the Camera Config UI can offer a
// pick-list instead of manual IP entry.
type camStreamInfo struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     string `json:"port,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// GET /api/camera-config/streams — list go2rtc streams that have an rtsp source,
// with ip/username/password extracted from rtsp://user:pass@host[:port]/...
// Admin-only: it exposes camera credentials.
func cameraConfigStreamsHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok || user.Role != RoleAdmin {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	out := make([]camStreamInfo, 0)
	if getStreamNames != nil && getStreamSources != nil {
		names := getStreamNames()
		sort.Strings(names)
		for _, name := range names {
			for _, src := range getStreamSources(name) {
				if !strings.HasPrefix(strings.ToLower(src), "rtsp://") {
					continue
				}
				u, err := url.Parse(src)
				if err != nil || u.Host == "" {
					continue
				}
				host, port, splitErr := net.SplitHostPort(u.Host)
				if splitErr != nil {
					host, port = u.Host, ""
				}
				ci := camStreamInfo{Name: name, Host: host, Port: port}
				if u.User != nil {
					ci.Username = u.User.Username()
					if pw, has := u.User.Password(); has {
						ci.Password = pw
					}
				}
				out = append(out, ci)
				break // first rtsp source per stream is enough
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

type camConfigEntry struct {
	IP       string `json:"ip"`
	Password string `json:"password"`
}

type camConfigRequest struct {
	Entries    []camConfigEntry `json:"entries"`
	Username   string           `json:"username"`    // HTTP auth user; default "service"
	TimeoutSec int              `json:"timeout_sec"` // 0 → default 5
	Command    string           `json:"command"`     // default "0x0A94"
	Type       string           `json:"type"`        // default "T_OCTET"
	Direction  string           `json:"direction"`   // default "WRITE"
	Num        string           `json:"num"`         // default "7"
	Payload    string           `json:"payload"`     // default "0"
}

type camConfigResult struct {
	IP           string `json:"ip"`
	OK           bool   `json:"ok"`
	Status       int    `json:"status,omitempty"`
	Message      string `json:"message,omitempty"`
	ApplyOK      *bool  `json:"apply_ok,omitempty"`    // nil = not run (config failed)
	ApplyStatus  int    `json:"apply_status,omitempty"`
	ApplyMessage string `json:"apply_message,omitempty"`
}

// applyRCPQuery is the fixed RCP XML query for the commit/apply command.
// It runs automatically on each camera after a successful config command.
const applyRCPQuery = "command=0x0AD9&type=P_OCTET&direction=WRITE&num=1&payload=0x000000020000000000000002000000070000000100000002000000010000000200000001000000010000000300000001000000010000000400000001000000010000000500000001000000010000000600000001000000010000000700000001000000010000000800000001000000010000000900000001000000010000000A0000000100000001"

// ── Digest Auth helpers ───────────────────────────────────────────

func md5hex(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func parseDigestParams(s string) map[string]string {
	params := make(map[string]string)
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		idx := strings.IndexByte(part, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(part[:idx])
		val := strings.TrimSpace(part[idx+1:])
		val = strings.Trim(val, `"`)
		params[key] = val
	}
	return params
}

func buildDigestAuth(wwwAuth, method, reqURI, username, password string) string {
	params := parseDigestParams(strings.TrimPrefix(wwwAuth, "Digest "))
	realm := params["realm"]
	nonce := params["nonce"]
	qop := params["qop"]
	algorithm := params["algorithm"]
	if algorithm == "" {
		algorithm = "MD5"
	}

	cnonce := randomHex(8)
	nc := "00000001"

	var ha1 string
	if strings.EqualFold(algorithm, "MD5-sess") {
		ha1 = md5hex(md5hex(username+":"+realm+":"+password) + ":" + nonce + ":" + cnonce)
	} else {
		ha1 = md5hex(username + ":" + realm + ":" + password)
	}
	ha2 := md5hex(method + ":" + reqURI)

	useQop := strings.Contains(qop, "auth")
	var response string
	if useQop {
		response = md5hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2)
	} else {
		response = md5hex(ha1 + ":" + nonce + ":" + ha2)
	}

	header := fmt.Sprintf(
		`Digest username="%s", realm="%s", nonce="%s", uri="%s", algorithm=%s, response="%s"`,
		username, realm, nonce, reqURI, algorithm, response,
	)
	if useQop {
		header += fmt.Sprintf(`, qop=auth, nc=%s, cnonce="%s"`, nc, cnonce)
	}
	return header
}

// callRCP calls the RCP XML endpoint, handling both Basic and Digest auth automatically.
// It first attempts without credentials; if the camera returns 401 with Digest challenge
// it computes the digest response; otherwise falls back to Basic auth.
func callRCP(ctx context.Context, client *http.Client, targetURL, username, password string) (int, error) {
	u, err := url.Parse(targetURL)
	if err != nil {
		return 0, err
	}
	reqURI := u.RequestURI()

	do := func(authHeader string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
		if err != nil {
			return nil, err
		}
		if authHeader != "" {
			req.Header.Set("Authorization", authHeader)
		}
		return client.Do(req)
	}

	// Round 1: no auth — detect what the camera wants
	resp1, err := do("")
	if err != nil {
		if ctx.Err() != nil {
			return 0, fmt.Errorf("timeout")
		}
		return 0, err
	}
	resp1.Body.Close()

	if resp1.StatusCode != http.StatusUnauthorized {
		return resp1.StatusCode, nil
	}

	// Round 2: respond to the 401 challenge
	wwwAuth := resp1.Header.Get("WWW-Authenticate")
	var authHeader string
	if strings.HasPrefix(wwwAuth, "Digest ") {
		authHeader = buildDigestAuth(wwwAuth, "GET", reqURI, username, password)
	} else {
		// Basic auth — build header manually to avoid URL-embedded credentials
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
		req.SetBasicAuth(username, password)
		authHeader = req.Header.Get("Authorization")
	}

	resp2, err := do(authHeader)
	if err != nil {
		if ctx.Err() != nil {
			return 0, fmt.Errorf("timeout")
		}
		return 0, err
	}
	resp2.Body.Close()
	return resp2.StatusCode, nil
}

// ── Handler ───────────────────────────────────────────────────────

func cameraConfigApplyHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok || user.Role != RoleAdmin {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var req camConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Entries) == 0 {
		http.Error(w, "entries is empty", http.StatusBadRequest)
		return
	}
	if len(req.Entries) > 5000 {
		http.Error(w, "too many entries (max 5000)", http.StatusBadRequest)
		return
	}

	username := req.Username
	if username == "" {
		username = "service"
	}
	command := req.Command
	if command == "" {
		command = "0x0A94"
	}
	rcpType := req.Type
	if rcpType == "" {
		rcpType = "T_OCTET"
	}
	direction := req.Direction
	if direction == "" {
		direction = "WRITE"
	}
	num := req.Num
	if num == "" {
		num = "7"
	}
	payload := req.Payload
	if payload == "" {
		payload = "0"
	}
	timeoutSec := req.TimeoutSec
	if timeoutSec <= 0 || timeoutSec > 30 {
		timeoutSec = 5
	}

	rawQuery := fmt.Sprintf("command=%s&type=%s&direction=%s&num=%s&payload=%s",
		url.QueryEscape(command), url.QueryEscape(rcpType),
		url.QueryEscape(direction), url.QueryEscape(num),
		url.QueryEscape(payload))

	// Two HTTP round-trips per camera (challenge + response), so double the timeout
	client := &http.Client{
		Timeout: time.Duration(timeoutSec*2)*time.Second + time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	results := make([]camConfigResult, len(req.Entries))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 30)

	for i, entry := range req.Entries {
		i, entry := i, entry
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()

			targetURL := fmt.Sprintf("http://%s/rcp.xml?%s", entry.IP, rawQuery)

			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec*2)*time.Second)
			defer cancel()

			status, err := callRCP(ctx, client, targetURL, username, entry.Password)
			if err != nil {
				results[i] = camConfigResult{IP: entry.IP, OK: false, Message: err.Error()}
				return
			}

			isOK := status >= 200 && status < 300
			res := camConfigResult{IP: entry.IP, OK: isOK, Status: status}
			if !isOK {
				res.Message = http.StatusText(status)
				results[i] = res
				return
			}

			// Auto-apply: commit the config change on the same camera
			applyURL := fmt.Sprintf("http://%s/rcp.xml?%s", entry.IP, applyRCPQuery)
			aCtx, aCancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec*2)*time.Second)
			defer aCancel()
			applyStatus, applyErr := callRCP(aCtx, client, applyURL, username, entry.Password)
			applyOK := applyErr == nil && applyStatus >= 200 && applyStatus < 300
			res.ApplyOK = &applyOK
			if applyErr != nil {
				res.ApplyMessage = applyErr.Error()
			} else {
				res.ApplyStatus = applyStatus
				if !applyOK {
					res.ApplyMessage = http.StatusText(applyStatus)
				}
			}
			results[i] = res
		}()
	}

	wg.Wait()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(results)
}
