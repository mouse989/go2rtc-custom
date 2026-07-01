package tcp

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

type Auth struct {
	Method  byte
	user    string
	pass    string
	header  string
	h1nonce string

	// Validator, when set, overrides the fixed (user, pass) comparison in
	// Validate: the client-supplied Basic-auth username/password/request-path
	// are handed to it, so callers can check credentials against a dynamic
	// user store (and authorize per-path) instead of one static pair.
	Validator func(user, pass, path string) bool

	// lastAuthHeader/lastValid cache the previous Validator result so a
	// long-lived RTSP session (OPTIONS/DESCRIBE/SETUP/PLAY/keepalive) doesn't
	// re-run an expensive check (e.g. bcrypt) on every single request — only
	// when the client's Authorization header actually changes.
	lastAuthHeader string
	lastValid      bool
}

// NewAuthValidator creates a server-side Auth that checks each request's
// Basic-auth credentials via the given callback instead of a fixed pair.
func NewAuthValidator(validator func(user, pass, path string) bool) *Auth {
	return &Auth{Method: AuthBasic, Validator: validator}
}

const (
	AuthNone byte = iota
	AuthUnknown
	AuthBasic
	AuthDigest
	AuthTPLink // https://drmnsamoliu.github.io/video.html
)

func NewAuth(user *url.Userinfo) *Auth {
	a := new(Auth)
	a.user = user.Username()
	a.pass, _ = user.Password()
	if a.user != "" {
		a.Method = AuthUnknown
	}
	return a
}

func (a *Auth) Read(res *Response) bool {
	auth := res.Header.Get("WWW-Authenticate")
	if len(auth) < 6 {
		return false
	}

	switch auth[:6] {
	case "Basic ":
		a.header = "Basic " + B64(a.user, a.pass)
		a.Method = AuthBasic
		return true
	case "Digest":
		realm := Between(auth, `realm="`, `"`)
		nonce := Between(auth, `nonce="`, `"`)

		a.h1nonce = HexMD5(a.user, realm, a.pass) + ":" + nonce
		a.header = fmt.Sprintf(
			`Digest username="%s", realm="%s", nonce="%s"`,
			a.user, realm, nonce,
		)
		a.Method = AuthDigest
		return true
	default:
		return false
	}
}

func (a *Auth) Write(req *Request) {
	if a == nil {
		return
	}

	switch a.Method {
	case AuthBasic:
		req.Header.Set("Authorization", a.header)
	case AuthDigest:
		// important to use String except RequestURL for RtspServer:
		// https://github.com/AlexxIT/go2rtc/issues/244
		uri := req.URL.String()
		h2 := HexMD5(req.Method, uri)
		response := HexMD5(a.h1nonce, h2)
		header := a.header + fmt.Sprintf(
			`, uri="%s", response="%s"`, uri, response,
		)
		req.Header.Set("Authorization", header)
	case AuthTPLink:
		req.URL.Host = "127.0.0.1"
	}
}

func (a *Auth) Validate(req *Request) (valid, empty bool) {
	if a == nil {
		return true, true
	}

	header := req.Header.Get("Authorization")
	if header == "" {
		return false, true
	}

	if a.Validator != nil {
		if header == a.lastAuthHeader {
			return a.lastValid, false
		}

		const prefix = "Basic "
		if !strings.HasPrefix(header, prefix) {
			return false, false
		}
		raw, err := base64.StdEncoding.DecodeString(header[len(prefix):])
		if err != nil {
			return false, false
		}
		user, pass, ok := strings.Cut(string(raw), ":")
		if !ok {
			return false, false
		}
		var path string
		if req.URL != nil {
			path = req.URL.Path
		}

		a.lastAuthHeader = header
		a.lastValid = a.Validator(user, pass, path)
		if !a.lastValid {
			return false, false
		}
		a.user = user
		return true, false
	}

	if a.Method == AuthUnknown {
		a.Method = AuthBasic
		a.header = "Basic " + B64(a.user, a.pass)
	}

	return header == a.header, false
}

func (a *Auth) ReadNone(res *Response) bool {
	auth := res.Header.Get("WWW-Authenticate")
	if strings.Contains(auth, "TP-LINK Streaming Media") {
		a.Method = AuthTPLink
		return true
	}
	return false
}

func (a *Auth) UserInfo() *url.Userinfo {
	return url.UserPassword(a.user, a.pass)
}

// User returns the username that last passed Validate (set by Validator mode).
func (a *Auth) User() string {
	if a == nil {
		return ""
	}
	return a.user
}

func Between(s, sub1, sub2 string) string {
	i := strings.Index(s, sub1)
	if i < 0 {
		return ""
	}
	s = s[i+len(sub1):]
	i = strings.Index(s, sub2)
	if i < 0 {
		return ""
	}
	return s[:i]
}

func HexMD5(s ...string) string {
	b := md5.Sum([]byte(strings.Join(s, ":")))
	return hex.EncodeToString(b[:])
}

func B64(s ...string) string {
	b := []byte(strings.Join(s, ":"))
	return base64.StdEncoding.EncodeToString(b)
}
