package auth

// webauthn.go — WebAuthn (FIDO2/passkey) relying-party ceremonies for the
// device-binding feature (device_binding.go holds the scope catalog and
// TrustedDevice store this wires into).
//
// Two ceremonies, each Begin/Finish:
//   - Registration: a logged-in user enrolls the device they're currently
//     using. FinishRegistration's result is stored as a "pending"
//     TrustedDevice — it doesn't grant access until an admin approves it.
//   - Login (used here as a step-up "verification", never as the primary
//     sign-in): proves the caller is holding one of their own *approved*
//     devices. Always non-discoverable (BeginLogin, not
//     BeginDiscoverableLogin) because the caller's username is already
//     known from their JWT session by the time this runs.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

var webAuthnRP *webauthn.WebAuthn

var (
	errWebAuthnNotConfigured  = errors.New("device binding is not configured (missing RP ID/origin in settings)")
	errWebAuthnSessionExpired = errors.New("device verification session expired or was never started — try again")
)

// initWebAuthn configures the relying party from admin-set settings (see
// settings.go). An empty rpID/rpOrigins leaves webAuthnRP nil — device
// binding is opt-in, so an admin who hasn't configured it yet shouldn't
// block go2rtc from starting; the ceremony functions below simply report
// errWebAuthnNotConfigured until it is.
func initWebAuthn(rpID, rpDisplayName string, rpOrigins []string) error {
	if rpID == "" || len(rpOrigins) == 0 {
		webAuthnRP = nil
		return nil
	}
	w, err := webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPDisplayName: rpDisplayName,
		RPOrigins:     rpOrigins,
	})
	if err != nil {
		return err
	}
	webAuthnRP = w
	return nil
}

// webauthnUser adapts a username + its stored TrustedDevices to the
// webauthn.User interface the library requires. WebAuthnID uses the
// username directly rather than a separate random handle — a deliberate
// simplification consistent with the rest of this package (username is
// already the primary key everywhere else, e.g. JWT claims).
type webauthnUser struct {
	username string
}

func (u webauthnUser) WebAuthnID() []byte          { return []byte(u.username) }
func (u webauthnUser) WebAuthnName() string        { return u.username }
func (u webauthnUser) WebAuthnDisplayName() string { return u.username }

// WebAuthnCredentials returns only approved devices: this is used both to
// build the registration exclusion list and — critically — the login
// ceremony's allowed-credentials list, so a pending or revoked device can
// never satisfy a step-up verification.
func (u webauthnUser) WebAuthnCredentials() []webauthn.Credential {
	devices := listDevicesForUser(u.username)
	creds := make([]webauthn.Credential, 0, len(devices))
	for _, d := range devices {
		if d.Status == DeviceStatusApproved {
			creds = append(creds, d.Credential)
		}
	}
	return creds
}

// ── Ceremony session state ──────────────────────────────────────────
// One in-flight registration/login ceremony per username at a time —
// simpler than issuing opaque per-ceremony session tokens, and sufficient
// for this use case (a user enrolling or step-up-verifying one device from
// one browser tab). Separate maps for registration vs. login so starting
// one ceremony never clobbers the other.

const webauthnSessionTTL = 5 * time.Minute

type webauthnSessionEntry struct {
	data    webauthn.SessionData
	expires time.Time
}

var (
	webauthnSessionsMu     sync.Mutex
	webauthnRegSessions    = map[string]webauthnSessionEntry{}
	webauthnVerifySessions = map[string]webauthnSessionEntry{}
)

func saveWebauthnSession(store map[string]webauthnSessionEntry, username string, s webauthn.SessionData) {
	webauthnSessionsMu.Lock()
	defer webauthnSessionsMu.Unlock()
	store[username] = webauthnSessionEntry{data: s, expires: time.Now().Add(webauthnSessionTTL)}
}

// takeWebauthnSession consumes (removes) username's pending session
// regardless of outcome — a ceremony session is single-use whether it
// succeeds, fails, or has expired.
func takeWebauthnSession(store map[string]webauthnSessionEntry, username string) (webauthn.SessionData, bool) {
	webauthnSessionsMu.Lock()
	defer webauthnSessionsMu.Unlock()
	entry, ok := store[username]
	delete(store, username)
	if !ok || time.Now().After(entry.expires) {
		return webauthn.SessionData{}, false
	}
	return entry.data, true
}

// beginDeviceRegistration starts a WebAuthn registration ceremony for
// username, returning the CredentialCreation options to serialize as JSON
// and send to the browser for navigator.credentials.create().
func beginDeviceRegistration(username string) (*protocol.CredentialCreation, error) {
	if webAuthnRP == nil {
		return nil, errWebAuthnNotConfigured
	}
	creation, session, err := webAuthnRP.BeginRegistration(webauthnUser{username: username})
	if err != nil {
		return nil, err
	}
	saveWebauthnSession(webauthnRegSessions, username, *session)
	return creation, nil
}

// finishDeviceRegistration completes the ceremony started by
// beginDeviceRegistration, returning the verified Credential ready to be
// stored as a pending TrustedDevice by the caller.
func finishDeviceRegistration(username string, r *http.Request) (*webauthn.Credential, error) {
	if webAuthnRP == nil {
		return nil, errWebAuthnNotConfigured
	}
	session, ok := takeWebauthnSession(webauthnRegSessions, username)
	if !ok {
		return nil, errWebAuthnSessionExpired
	}
	return webAuthnRP.FinishRegistration(webauthnUser{username: username}, session, r)
}

// beginDeviceVerification starts a step-up WebAuthn login/assertion
// ceremony for username against their already-*approved* devices only.
// Returns errWebAuthnNotConfigured or, via the library, an error if the
// user has no approved device yet.
func beginDeviceVerification(username string) (*protocol.CredentialAssertion, error) {
	if webAuthnRP == nil {
		return nil, errWebAuthnNotConfigured
	}
	assertion, session, err := webAuthnRP.BeginLogin(webauthnUser{username: username})
	if err != nil {
		return nil, err
	}
	saveWebauthnSession(webauthnVerifySessions, username, *session)
	return assertion, nil
}

// finishDeviceVerification completes the ceremony started by
// beginDeviceVerification and persists the credential's advanced
// SignCount/Flags (replay-counter bookkeeping) plus last-used metadata onto
// the matching TrustedDevice. Returns the now-verified TrustedDevice.
func finishDeviceVerification(username, remoteIP string, r *http.Request) (*TrustedDevice, error) {
	if webAuthnRP == nil {
		return nil, errWebAuthnNotConfigured
	}
	session, ok := takeWebauthnSession(webauthnVerifySessions, username)
	if !ok {
		return nil, errWebAuthnSessionExpired
	}
	updated, err := webAuthnRP.FinishLogin(webauthnUser{username: username}, session, r)
	if err != nil {
		return nil, err
	}
	dev, ok := findApprovedDeviceByCredentialID(updated.ID)
	if !ok {
		// Shouldn't happen: FinishLogin only returns a credential that
		// matched one already in WebAuthnCredentials(), which only lists
		// approved devices.
		return nil, errWebAuthnSessionExpired
	}
	touchDeviceUsage(dev.ID, *updated, remoteIP)
	dev.Credential = *updated
	return dev, nil
}

// ── Device-verified cookie ──────────────────────────────────────────
// A successful step-up ceremony (finishDeviceVerification) is remembered
// for DeviceVerifyValidHours so the browser isn't asked to re-verify on
// every single request to a gated scope. One verification satisfies every
// gated scope for that user — it proves the same physical approved device
// is present, which is what every scope actually cares about — so the
// cookie carries no scope list, just who and until when. Signed (not
// encrypted: it holds no secret, only a username and expiry) with the same
// HMAC secret as the login JWT (see jwt.go) rather than a separate key, to
// avoid managing yet another secret file.

const deviceVerifiedCookieName = "go2rtc_device_verified"

type deviceVerifiedClaims struct {
	Username string `json:"u"`
	Exp      int64  `json:"exp"`
}

func signDeviceVerifiedToken(username string, validFor time.Duration) (string, error) {
	payload, err := json.Marshal(deviceVerifiedClaims{
		Username: username,
		Exp:      time.Now().Add(validFor).Unix(),
	})
	if err != nil {
		return "", err
	}
	payloadB64 := b64(payload)
	mac := hmac.New(sha256.New, jwtSecret)
	mac.Write([]byte(payloadB64))
	sig := b64(mac.Sum(nil))
	return payloadB64 + "." + sig, nil
}

// verifyDeviceVerifiedToken reports whether token is a validly-signed,
// unexpired device-verified token for username.
func verifyDeviceVerifiedToken(token, username string) bool {
	if token == "" {
		return false
	}
	dot := -1
	for i := len(token) - 1; i >= 0; i-- {
		if token[i] == '.' {
			dot = i
			break
		}
	}
	if dot < 0 {
		return false
	}
	payloadB64, sig := token[:dot], token[dot+1:]
	mac := hmac.New(sha256.New, jwtSecret)
	mac.Write([]byte(payloadB64))
	expected := b64(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return false
	}
	payload, err := b64dec(payloadB64)
	if err != nil {
		return false
	}
	var c deviceVerifiedClaims
	if err = json.Unmarshal(payload, &c); err != nil {
		return false
	}
	if c.Username != username {
		return false
	}
	return time.Now().Unix() <= c.Exp
}

// deviceVerifyValidDuration returns the configured (or default) lifetime of
// a device-verified cookie.
func deviceVerifyValidDuration() time.Duration {
	s := GetSettings()
	if s.DeviceVerifyValidHours >= 1 {
		return time.Duration(s.DeviceVerifyValidHours) * time.Hour
	}
	return 12 * time.Hour
}
