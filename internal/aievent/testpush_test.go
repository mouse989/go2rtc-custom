package aievent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunTestPushRejectsNonHTTPURL(t *testing.T) {
	res := runTestPush(testPushRequest{URL: "ftp://example.com"})
	if res.OK || res.Error == "" {
		t.Errorf("expected a URL-scheme error, got %+v", res)
	}
}

func TestRunTestPushForwardsKeyAndBodyAndReportsStatus(t *testing.T) {
	var gotKey, gotBody, gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-API-Key")
		gotContentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	res := runTestPush(testPushRequest{URL: srv.URL, APIKey: "test-key-123", Body: `{"type":"event.created"}`})
	if !res.OK {
		t.Fatalf("expected OK, got %+v", res)
	}
	if res.StatusCode != http.StatusCreated {
		t.Errorf("expected status 201, got %d", res.StatusCode)
	}
	if res.ResponseBody != `{"status":"ok"}` {
		t.Errorf("unexpected response body: %q", res.ResponseBody)
	}
	if gotKey != "test-key-123" {
		t.Errorf("expected X-API-Key to be forwarded, got %q", gotKey)
	}
	if gotBody != `{"type":"event.created"}` {
		t.Errorf("expected the request body to be forwarded verbatim, got %q", gotBody)
	}
	if gotContentType != "application/json" {
		t.Errorf("expected Content-Type: application/json, got %q", gotContentType)
	}
}

func TestRunTestPushAppendsDryRunQueryParam(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := runTestPush(testPushRequest{URL: srv.URL, DryRun: true})
	if !res.OK {
		t.Fatalf("expected OK, got %+v", res)
	}
	if gotQuery != "dry_run=1" {
		t.Errorf("expected dry_run=1 in the query string, got %q", gotQuery)
	}
}

func TestRunTestPushReportsNon2xxAsOKWithStatusCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"invalid_api_key"}}`))
	}))
	defer srv.Close()

	res := runTestPush(testPushRequest{URL: srv.URL})
	if !res.OK {
		t.Error("expected OK:true — a non-2xx response is still a completed round-trip, not a transport error")
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", res.StatusCode)
	}
	if res.Error != "" {
		t.Errorf("expected no transport error for a non-2xx HTTP response, got %q", res.Error)
	}
}

func TestRunTestPushReportsTransportErrorForUnreachableHost(t *testing.T) {
	res := runTestPush(testPushRequest{URL: "http://127.0.0.1:1"}) // nothing listens here
	if res.OK {
		t.Error("expected OK:false for a connection failure")
	}
	if res.Error == "" {
		t.Error("expected a non-empty transport error message")
	}
}

func TestRunTestPushTruncatesOversizedResponseBody(t *testing.T) {
	big := make([]byte, testPushMaxRespBody+1024)
	for i := range big {
		big[i] = 'a'
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(big)
	}))
	defer srv.Close()

	res := runTestPush(testPushRequest{URL: srv.URL})
	if !res.OK {
		t.Fatalf("expected OK, got %+v", res)
	}
	if !res.Truncated {
		t.Error("expected Truncated=true for an oversized response body")
	}
	if len(res.ResponseBody) != testPushMaxRespBody {
		t.Errorf("expected the response body to be capped at %d bytes, got %d", testPushMaxRespBody, len(res.ResponseBody))
	}
}
