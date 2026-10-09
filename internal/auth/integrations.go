package auth

// integrations.go — API keys for inbound machine-to-machine pushes from
// external systems (OMNIA/FPT VDS today; designed so a second/third source
// later only needs its own Integration record + its own payload-parsing
// endpoint, not a new auth mechanism — see docs/omnia-integration-api-spec.md).
//
// Deliberately NOT modeled as a User: these have no password, no session, no
// page permissions — just a long-lived secret scoped to exactly one API path
// prefix. The plaintext key is shown to the admin ONLY ONCE, at creation or
// rotation time; only its SHA-256 hash is ever persisted, so a leaked
// integrations.json file (backup, misconfigured access...) doesn't hand out
// a usable key the way storing it in plaintext would — the same reasoning
// that keeps User.Password as a bcrypt hash, not the password itself.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
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
	Name        string    `json:"name"`         // admin-facing label, e.g. "OMNIA / FPT VDS"
	KeyHash     string    `json:"key_hash"`     // sha256(plaintext key), hex — never the key itself
	AllowedPath string    `json:"allowed_path"` // path PREFIX this key may call, e.g. "/api/aievent/omnia/v1/push"
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
		return err
	}
	s.integrations = make(map[string]*Integration, len(list))
	for _, in := range list {
		s.integrations[in.ID] = in
	}
	return nil
}

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
	return os.WriteFile(s.path, data, 0644)
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

	in := &Integration{
		ID:          newIntegrationID(),
		Name:        name,
		KeyHash:     hash,
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

	istore.mu.Lock()
	defer istore.mu.Unlock()
	in, ok := istore.integrations[id]
	if !ok {
		return "", errIntegrationNotFound
	}
	in.KeyHash = hash
	if err := istore.saveLocked(); err != nil {
		return "", err
	}
	return plaintext, nil
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

var errIntegrationNotFound = errors.New("integration not found")
