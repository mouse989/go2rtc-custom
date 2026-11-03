package auth

// integrations.go — API keys for inbound machine-to-machine pushes from
// external systems (OMNIA/FPT VDS today; designed so a second/third source
// later only needs its own Integration record + its own payload-parsing
// endpoint, not a new auth mechanism — see docs/omnia-integration-api-spec.md).
//
// Deliberately NOT modeled as a User: these have no password, no session, no
// page permissions — just a long-lived secret scoped to exactly one API path
// prefix. Validation (ValidateIntegrationKey) only ever needs the SHA-256
// hash, same as a password check. But unlike a password, an admin
// legitimately needs to hand this exact same secret to an external team
// again later (re-provisioning, debugging a mismatch) without forcing an
// unplanned rotation that would break whatever is already pushing with the
// old key — so the plaintext is ALSO kept, encrypted at rest (AES-256-GCM,
// key derived from the server's own JWT secret — never a separate
// plaintext-on-disk secret) in KeyEnc. Revealing it (RevealIntegrationKey)
// is gated at the API layer behind re-entering the admin's own login
// password, throttled through the exact same brute-force lockout as
// POST /api/auth/login — see api_integrations.go's reveal handler.
import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Integration is one registered external system allowed to push data in.
type Integration struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`              // admin-facing label, e.g. "OMNIA / FPT VDS"
	KeyHash     string    `json:"key_hash"`          // sha256(plaintext key), hex — used for validation
	KeyEnc      string    `json:"key_enc,omitempty"` // AES-GCM(plaintext key), base64 — used only to let an admin reveal it again
	AllowedPath string    `json:"allowed_path"`      // path PREFIX this key may call, e.g. "/api/aievent/omnia/v1/push"
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	LastUsedAt  time.Time `json:"last_used_at,omitempty"`
	LastUsedIP  string    `json:"last_used_ip,omitempty"`
}

type integrationStore struct {
	mu           sync.RWMutex
	path         string
	integrations map[string]*Integration // by ID
}

var istore *integrationStore

func initIntegrations(path string) error {
	istore = &integrationStore{path: path, integrations: make(map[string]*Integration)}
	return istore.load()
}

func (s *integrationStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []*Integration
	if err := json.Unmarshal(data, &list); err != nil {
		// The file exists but isn't valid JSON — could be a truncated/
		// corrupted write (the old saveLocked() used a plain os.WriteFile,
		// which is NOT atomic), or storage-layer corruption outside this
		// app entirely (observed in practice: a Synology Btrfs volume
		// returning an all-zero block for a file whose checksum no longer
		// matches, with no redundant copy to self-heal from — same exact
		// byte-length as the real file, every byte 0x00). Either way, not
		// "this admin genuinely has zero integrations". Falling back to an
		// empty in-memory store is still the only option when nothing else
		// can be recovered, but doing that silently — Init() in auth.go
		// treats this whole store as non-fatal and only logs a Warn — would
		// then let the very next integration create/edit/delete call
		// saveLocked() and permanently overwrite s.path with that empty
		// list, destroying whatever bytes were still sitting there.
		// Preserve the unreadable original for forensics before that can
		// happen, and log loud enough that an admin staring at an empty
		// "Tích hợp đẩy dữ liệu" table has a chance of finding out why.
		backupPath := s.path + ".corrupt-" + time.Now().UTC().Format("20060102T150405Z")
		if bakErr := os.WriteFile(backupPath, data, 0644); bakErr == nil {
			log.Error().Err(err).Str("path", s.path).Str("backup", backupPath).
				Msg("[auth] integrations.json is not valid JSON — the unreadable original was preserved at the backup path for recovery")
		} else {
			log.Error().Err(err).Str("path", s.path).AnErr("backup_error", bakErr).
				Msg("[auth] integrations.json is not valid JSON, AND failed to back up the unreadable original (it will be overwritten on the next integration change)")
		}

		// Before giving up to an empty list, try the rotating known-good
		// backups saveLocked() keeps on every successful save (see
		// rotateBackups) — whatever corrupted the live file almost
		// certainly didn't touch these too, so this recovers every
		// integration/API key up to the last successful save instead of
		// forcing them all to be manually recreated.
		if recovered, recoveredFrom, rerr := s.recoverFromBackupLocked(); rerr == nil {
			s.integrations = recovered
			log.Warn().Str("path", s.path).Str("recoveredFrom", recoveredFrom).Int("count", len(recovered)).
				Msg("[auth] integrations.json was corrupt — recovered its contents from the most recent valid backup and rewrote it")
			if serr := s.saveLocked(); serr != nil {
				log.Warn().Err(serr).Msg("[auth] recovered integrations into memory but failed to re-save integrations.json — it is still corrupt on disk until the next successful save")
			}
			return nil
		}

		return err
	}
	s.integrations = make(map[string]*Integration, len(list))
	for _, in := range list {
		s.integrations[in.ID] = in
	}
	return nil
}

// integrationBackupSuffixes names the rotating known-good backups
// saveLocked() maintains on every successful save — index 0 is the most
// recent, keeping a short history (not just the single most recent save)
// in case corruption strikes between a save and the next one being
// noticed, or a backup copy itself happens to be mid-rotation.
var integrationBackupSuffixes = []string{".bak.0", ".bak.1", ".bak.2"}

func (s *integrationStore) backupPaths() []string {
	paths := make([]string, len(integrationBackupSuffixes))
	for i, suf := range integrationBackupSuffixes {
		paths[i] = s.path + suf
	}
	return paths
}

// rotateBackups shifts the existing backups one slot older and writes data
// (the content saveLocked() just successfully persisted to s.path) into the
// newest slot. Best-effort: a missing source file (fewer than N saves have
// happened yet) is not an error, and a rotation failure never fails the
// save itself — this is a safety net on top of a save that already
// succeeded, not a precondition for it.
func (s *integrationStore) rotateBackups(data []byte) {
	paths := s.backupPaths()
	for i := len(paths) - 1; i > 0; i-- {
		_ = os.Rename(paths[i-1], paths[i])
	}
	_ = os.WriteFile(paths[0], data, 0644)
}

// recoverFromBackupLocked tries each rotating backup, newest first, and
// returns the first one that parses as a valid integration list. Caller
// must already hold s.mu.
func (s *integrationStore) recoverFromBackupLocked() (map[string]*Integration, string, error) {
	for _, p := range s.backupPaths() {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var list []*Integration
		if err := json.Unmarshal(data, &list); err != nil {
			continue
		}
		m := make(map[string]*Integration, len(list))
		for _, in := range list {
			m[in.ID] = in
		}
		return m, p, nil
	}
	return nil, "", errors.New("no valid integrations backup found")
}

// saveLocked persists the current integration list. Writes to a temp file
// and renames over s.path rather than writing s.path directly — os.Rename
// is atomic on the same filesystem, so a crash or kill signal arriving
// mid-write can only ever leave a stray .tmp file, never a truncated/
// corrupted integrations.json (see load()'s doc comment for what used to
// happen when it did).
func (s *integrationStore) saveLocked() error {
	list := make([]*Integration, 0, len(s.integrations))
	for _, in := range s.integrations {
		list = append(list, in)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	s.rotateBackups(data)
	return nil
}

// ListIntegrations returns every registered integration (defensive copies),
// sorted by name. KeyHash is present on the Go value but the API layer
// (api_integrations.go) never serializes it to callers.
func ListIntegrations() []*Integration {
	istore.mu.RLock()
	defer istore.mu.RUnlock()
	list := make([]*Integration, 0, len(istore.integrations))
	for _, in := range istore.integrations {
		cp := *in
		list = append(list, &cp)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

// GetIntegration returns a copy of the integration with the given ID.
func GetIntegration(id string) (*Integration, bool) {
	istore.mu.RLock()
	defer istore.mu.RUnlock()
	in, ok := istore.integrations[id]
	if !ok {
		return nil, false
	}
	cp := *in
	return &cp, true
}

// CreateIntegration registers a new integration and generates its key.
// Returns the stored record plus the PLAINTEXT key — the only time it is
// ever available; callers must show it to the admin immediately and never
// log it.
func CreateIntegration(name, allowedPath string) (*Integration, string, error) {
	name = strings.TrimSpace(name)
	allowedPath = strings.TrimSpace(allowedPath)
	if name == "" {
		return nil, "", errors.New("name required")
	}
	if allowedPath == "" || !strings.HasPrefix(allowedPath, "/api/") {
		return nil, "", errors.New("allowed_path must be a /api/... path prefix")
	}

	plaintext, hash, err := newIntegrationKey()
	if err != nil {
		return nil, "", err
	}
	enc, err := encryptIntegrationKey(plaintext)
	if err != nil {
		return nil, "", err
	}

	in := &Integration{
		ID:          newIntegrationID(),
		Name:        name,
		KeyHash:     hash,
		KeyEnc:      enc,
		AllowedPath: allowedPath,
		Enabled:     true,
		CreatedAt:   time.Now(),
	}

	istore.mu.Lock()
	defer istore.mu.Unlock()
	istore.integrations[in.ID] = in
	if err := istore.saveLocked(); err != nil {
		delete(istore.integrations, in.ID)
		return nil, "", err
	}
	cp := *in
	return &cp, plaintext, nil
}

// UpdateIntegration changes name/allowedPath/enabled for an existing
// integration. Never touches the key — use RotateIntegrationKey for that.
func UpdateIntegration(id, name, allowedPath string, enabled bool) (*Integration, error) {
	name = strings.TrimSpace(name)
	allowedPath = strings.TrimSpace(allowedPath)
	if name == "" {
		return nil, errors.New("name required")
	}
	if allowedPath == "" || !strings.HasPrefix(allowedPath, "/api/") {
		return nil, errors.New("allowed_path must be a /api/... path prefix")
	}

	istore.mu.Lock()
	defer istore.mu.Unlock()
	in, ok := istore.integrations[id]
	if !ok {
		return nil, errIntegrationNotFound
	}
	in.Name = name
	in.AllowedPath = allowedPath
	in.Enabled = enabled
	if err := istore.saveLocked(); err != nil {
		return nil, err
	}
	cp := *in
	return &cp, nil
}

// RotateIntegrationKey replaces an integration's key with a freshly
// generated one, immediately invalidating the old key, and returns the new
// plaintext (again, the only time it is ever visible).
func RotateIntegrationKey(id string) (string, error) {
	plaintext, hash, err := newIntegrationKey()
	if err != nil {
		return "", err
	}
	enc, err := encryptIntegrationKey(plaintext)
	if err != nil {
		return "", err
	}

	istore.mu.Lock()
	defer istore.mu.Unlock()
	in, ok := istore.integrations[id]
	if !ok {
		return "", errIntegrationNotFound
	}
	in.KeyHash = hash
	in.KeyEnc = enc
	if err := istore.saveLocked(); err != nil {
		return "", err
	}
	return plaintext, nil
}

// RevealIntegrationKey decrypts and returns the plaintext key for an
// existing integration, so an admin can hand it to an external team again
// (re-provisioning, debugging a mismatch) without forcing a rotation that
// would break whatever is already pushing with the current key. Callers
// (api_integrations.go's reveal handler) MUST re-verify the admin's own
// password before calling this — it performs no auth check of its own.
// Returns errIntegrationKeyUnavailable for an integration created before
// this field existed (KeyEnc empty) — rotating once enables reveal for it
// going forward.
func RevealIntegrationKey(id string) (string, error) {
	istore.mu.RLock()
	in, ok := istore.integrations[id]
	enc := ""
	if ok {
		enc = in.KeyEnc
	}
	istore.mu.RUnlock()
	if !ok {
		return "", errIntegrationNotFound
	}
	if enc == "" {
		return "", errIntegrationKeyUnavailable
	}
	return decryptIntegrationKey(enc)
}

// DeleteIntegration removes an integration permanently — its key stops
// working immediately.
func DeleteIntegration(id string) error {
	istore.mu.Lock()
	defer istore.mu.Unlock()
	if _, ok := istore.integrations[id]; !ok {
		return errIntegrationNotFound
	}
	delete(istore.integrations, id)
	return istore.saveLocked()
}

// ValidateIntegrationKey checks key against every enabled integration
// allowed to call path, touching LastUsedAt/LastUsedIP on a match. A
// disabled integration, a key that doesn't hash-match any stored key, or a
// match whose AllowedPath doesn't prefix path all report ok=false —
// identical "invalid" response either way, so a caller can't distinguish
// "wrong key" from "right key, wrong endpoint" by probing.
func ValidateIntegrationKey(path, key, remoteIP string) (*Integration, bool) {
	if key == "" {
		return nil, false
	}
	sum := sha256.Sum256([]byte(key))
	hashHex := hex.EncodeToString(sum[:])

	istore.mu.Lock()
	defer istore.mu.Unlock()
	for _, in := range istore.integrations {
		if !in.Enabled {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(in.KeyHash), []byte(hashHex)) != 1 {
			continue
		}
		if !strings.HasPrefix(path, in.AllowedPath) {
			continue
		}
		in.LastUsedAt = time.Now()
		in.LastUsedIP = remoteIP
		_ = istore.saveLocked() // best-effort — a failed usage-stamp write never blocks the push itself
		cp := *in
		return &cp, true
	}
	return nil, false
}

func newIntegrationKey() (plaintext, hashHex string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	plaintext = hex.EncodeToString(b)
	sum := sha256.Sum256([]byte(plaintext))
	return plaintext, hex.EncodeToString(sum[:]), nil
}

func newIntegrationID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// integrationEncKey derives a dedicated AES-256 key from the server's JWT
// secret via HMAC, rather than reusing jwtSecretRef directly — a distinct
// key per purpose so this encryption and token signing never share key
// material, without needing a second secret file on disk (jwtSecretRef is
// already generated/persisted once at startup — see jwt.go's initSecret).
func integrationEncKey() []byte {
	mac := hmac.New(sha256.New, jwtSecretRef)
	mac.Write([]byte("integration-key-enc-v1"))
	return mac.Sum(nil)
}

// encryptIntegrationKey/decryptIntegrationKey store the plaintext key
// recoverably (AES-256-GCM, random nonce prepended, base64) so
// RevealIntegrationKey can hand it back out later — see this file's top
// comment for why that's acceptable here even though a password never
// gets the same treatment.
func encryptIntegrationKey(plaintext string) (string, error) {
	block, err := aes.NewCipher(integrationEncKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func decryptIntegrationKey(enc string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(integrationEncKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("encrypted key is corrupt")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

var errIntegrationNotFound = errors.New("integration not found")
var errIntegrationKeyUnavailable = errors.New("this integration's key was created before the reveal feature existed — rotate it to enable this")
