package aievent

// testpush.go — admin-only "test my own push endpoint before handing off to
// a partner" tool, used by www/admin.html's 🧪 Test API Push card (see
// docs/omnia-integration-api-spec.md). The admin supplies a destination URL
// (their own aievent push endpoint, possibly on a different host/IP than
// this admin UI itself), an API key, and a raw JSON body — typically one of
// the request templates straight out of the spec — and the SERVER performs
// the HTTP POST, not the browser: doing this as a client-side fetch() would
// hit CORS whenever the target host differs from admin.html's own origin
// (the common case here — testing a raw internal IP while the admin UI is
// served from a domain), and CORS is the target server's call to make, not
// something this tool can route around from the browser.
//
// Deliberately NOT restricted to calling back into this same server: the
// admin may be testing a staging/different environment before pointing the
// partner at production. Gated to RoleAdmin only (requireAIEventAdmin) —
// this makes the server issue an arbitrary outbound HTTP request on the
// admin's behalf, but that's the same trust level as every other
// admin-only action already in this app that reaches arbitrary network
// addresses (e.g. configuring a camera source URL go2rtc itself connects
// out to), not a new exposure for a non-admin.

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	testPushTimeout     = 15 * time.Second
	testPushMaxRespBody = 256 << 10 // 256 KB — plenty to show a full error response, not enough to be a DoS vector
)

type testPushRequest struct {
	URL           string `json:"url"`
	APIKey        string `json:"api_key"`
	Body          string `json:"body"` // raw JSON text as typed/edited by the admin — forwarded byte-for-byte, not re-validated, so a deliberately malformed body can be tested too
	DryRun        bool   `json:"dry_run"`
	SkipTLSVerify bool   `json:"skip_tls_verify"` // for testing against a self-signed cert on an internal IP
}

type testPushResult struct {
	OK           bool   `json:"ok"` // true once an HTTP response was received at all, regardless of status code
	StatusCode   int    `json:"status_code,omitempty"`
	StatusText   string `json:"status_text,omitempty"`
	ElapsedMs    int64  `json:"elapsed_ms"`
	ResponseBody string `json:"response_body,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
	Error        string `json:"error,omitempty"` // set only for a transport-level failure (DNS, TLS, timeout, connection refused) — never for a non-2xx HTTP response, which is still OK:true
}

func handleTestPush(w http.ResponseWriter, r *http.Request) {
	if !requireAIEventAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req testPushRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, runTestPush(req))
}

// runTestPush does the actual outbound HTTP round-trip and builds the
// result — split out from handleTestPush so it's testable without faking
// an admin auth context (requireAIEventAdmin runs only in the handler).
func runTestPush(req testPushRequest) testPushResult {
	req.URL = strings.TrimSpace(req.URL)
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		return testPushResult{Error: "URL phải bắt đầu bằng http:// hoặc https://"}
	}

	target := req.URL
	if req.DryRun {
		sep := "?"
		if strings.Contains(target, "?") {
			sep = "&"
		}
		target += sep + "dry_run=1"
	}

	httpReq, err := http.NewRequest(http.MethodPost, target, bytes.NewReader([]byte(req.Body)))
	if err != nil {
		return testPushResult{Error: "URL không hợp lệ: " + err.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if req.APIKey != "" {
		httpReq.Header.Set("X-API-Key", req.APIKey)
	}

	client := &http.Client{Timeout: testPushTimeout}
	if req.SkipTLSVerify {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}

	start := time.Now()
	resp, err := client.Do(httpReq)
	elapsed := time.Since(start)
	if err != nil {
		return testPushResult{ElapsedMs: elapsed.Milliseconds(), Error: err.Error()}
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, testPushMaxRespBody+1))
	truncated := len(bodyBytes) > testPushMaxRespBody
	if truncated {
		bodyBytes = bodyBytes[:testPushMaxRespBody]
	}

	return testPushResult{
		OK:           true,
		StatusCode:   resp.StatusCode,
		StatusText:   resp.Status,
		ElapsedMs:    elapsed.Milliseconds(),
		ResponseBody: string(bodyBytes),
		Truncated:    truncated,
	}
}
