package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// digestCameraServer simulates an IP camera whose snapshot endpoint requires
// HTTP Digest auth: it challenges any unauthenticated/Basic request with 401,
// then independently recomputes the expected digest response from the
// Authorization header it receives (using the client's own nonce/cnonce) and
// only serves the snapshot if it matches — exactly what a real camera does,
// so this actually exercises buildDigestAuth's math rather than assuming it.
func digestCameraServer(t *testing.T, username, password, qop string) *httptest.Server {
	t.Helper()
	const realm = "IP Camera"
	const nonce = "testnonce123"
	jpegBody := []byte{0xFF, 0xD8, 0xFF, 0xE0, 'h', 'i'}

	challenge := fmt.Sprintf(`Digest realm="%s", nonce="%s"`, realm, nonce)
	if qop != "" {
		challenge += fmt.Sprintf(`, qop="%s"`, qop)
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Digest ") {
			w.Header().Set("WWW-Authenticate", challenge)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		params := parseDigestParams(strings.TrimPrefix(auth, "Digest "))
		if params["username"] != username || params["nonce"] != nonce {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		ha1 := md5hex(username + ":" + realm + ":" + password)
		ha2 := md5hex(http.MethodGet + ":" + params["uri"])
		var want string
		if params["qop"] != "" {
			want = md5hex(strings.Join([]string{ha1, params["nonce"], params["nc"], params["cnonce"], params["qop"], ha2}, ":"))
		} else {
			want = md5hex(ha1 + ":" + params["nonce"] + ":" + ha2)
		}

		if params["response"] != want {
			// Expected for the wrong-password test; the happy-path tests
			// assert success via fetchHTTPJPEG's return value instead, so a
			// mismatch here is never itself a test failure — just a 401.
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpegBody)
	}))
}

func TestFetchHTTPJPEGDigestAuthQopAuth(t *testing.T) {
	srv := digestCameraServer(t, "admin", "s3cret", "auth")
	defer srv.Close()

	creds := url.UserPassword("admin", "s3cret")
	data, err := fetchHTTPJPEG(context.Background(), srv.URL+"/snapshot", creds)
	if err != nil {
		t.Fatalf("fetchHTTPJPEG: %v", err)
	}
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		t.Fatalf("expected JPEG bytes, got %v", data)
	}
}

func TestFetchHTTPJPEGDigestAuthLegacyNoQop(t *testing.T) {
	srv := digestCameraServer(t, "admin", "s3cret", "") // some older firmware omits qop entirely
	defer srv.Close()

	creds := url.UserPassword("admin", "s3cret")
	data, err := fetchHTTPJPEG(context.Background(), srv.URL+"/snapshot", creds)
	if err != nil {
		t.Fatalf("fetchHTTPJPEG: %v", err)
	}
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		t.Fatalf("expected JPEG bytes, got %v", data)
	}
}

func TestFetchHTTPJPEGDigestAuthWrongPasswordFails(t *testing.T) {
	srv := digestCameraServer(t, "admin", "s3cret", "auth")
	defer srv.Close()

	creds := url.UserPassword("admin", "wrong-password")
	_, err := fetchHTTPJPEG(context.Background(), srv.URL+"/snapshot", creds)
	if err == nil {
		t.Fatal("expected an error with the wrong password, got none")
	}
}

func TestFetchHTTPJPEGBasicAuthStillWorksInOneRoundTrip(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		user, pass, ok := r.BasicAuth()
		if !ok || user != "admin" || pass != "s3cret" {
			w.Header().Set("WWW-Authenticate", `Basic realm="cam"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte{0xFF, 0xD8, 0xFF})
	}))
	defer srv.Close()

	creds := url.UserPassword("admin", "s3cret")
	data, err := fetchHTTPJPEG(context.Background(), srv.URL+"/snapshot", creds)
	if err != nil {
		t.Fatalf("fetchHTTPJPEG: %v", err)
	}
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		t.Fatalf("expected JPEG bytes, got %v", data)
	}
	if requests != 1 {
		t.Fatalf("expected exactly 1 request for a Basic-auth camera, got %d", requests)
	}
}

func TestFetchHTTPJPEGDigestFromURLEmbeddedCreds(t *testing.T) {
	// Mirrors the ONVIF-discovered-snapshot-URI path, which calls
	// fetchHTTPJPEG(ctx, uri, nil) with credentials embedded in the URL
	// itself rather than passed as a separate creds argument.
	srv := digestCameraServer(t, "admin", "s3cret", "auth")
	defer srv.Close()

	u, _ := url.Parse(srv.URL + "/snapshot")
	u.User = url.UserPassword("admin", "s3cret")

	data, err := fetchHTTPJPEG(context.Background(), u.String(), nil)
	if err != nil {
		t.Fatalf("fetchHTTPJPEG: %v", err)
	}
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		t.Fatalf("expected JPEG bytes, got %v", data)
	}
}
