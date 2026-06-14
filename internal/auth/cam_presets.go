package auth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
)

// ErrPresetLocked is returned when trying to modify or delete a locked preset.
var ErrPresetLocked = errors.New("preset is locked and cannot be modified or deleted")

// CamPreset describes a saved RCP XML command template.
type CamPreset struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Command   string `json:"command"`
	Type      string `json:"type"`
	Direction string `json:"direction"`
	Num       string `json:"num"`
	Payload   string `json:"payload"`
	Locked    bool   `json:"locked,omitempty"` // locked presets cannot be edited or deleted
}

// applyPresetID is the well-known ID for the mandatory apply-config command.
const applyPresetID = "apply_config"

type presetStore struct {
	mu      sync.RWMutex
	path    string
	presets []*CamPreset
}

var pstore *presetStore

// defaultCamPresets are seeded into the JSON file on first run.
// The apply_config preset is always locked and re-ensured on every load.
var defaultCamPresets = []*CamPreset{
	{
		ID:        "gop",
		Name:      "Config GOP Profile 7",
		Command:   "0x0A94",
		Type:      "T_OCTET",
		Direction: "WRITE",
		Num:       "7",
		Payload:   "0",
	},
	{
		ID:        "stream2",
		Name:      "Config Stream Encoder – Stream 2 Profile 7",
		Command:   "0x0AD2",
		Type:      "P_OCTET",
		Direction: "WRITE",
		Num:       "1",
		Payload:   "0x000000000000000200000007",
	},
	{
		ID:        applyPresetID,
		Name:      "Apply Config (tự động sau mỗi lệnh)",
		Command:   "0x0AD9",
		Type:      "P_OCTET",
		Direction: "WRITE",
		Num:       "1",
		Payload:   "0x000000020000000000000002000000070000000100000002000000010000000200000001000000010000000300000001000000010000000400000001000000010000000500000001000000010000000600000001000000010000000700000001000000010000000800000001000000010000000900000001000000010000000A0000000100000001",
		Locked:    true,
	},
}

func initCamPresets(path string) error {
	pstore = &presetStore{path: path}
	return pstore.load()
}

func (s *presetStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		s.presets = make([]*CamPreset, len(defaultCamPresets))
		for i, p := range defaultCamPresets {
			cp := *p
			s.presets[i] = &cp
		}
		return s.saveLocked()
	}
	if err != nil {
		return err
	}
	var list []*CamPreset
	if err = json.Unmarshal(data, &list); err != nil {
		return err
	}
	s.presets = list
	// Ensure the locked apply preset is always present and locked.
	s.ensureSystemPresets()
	return nil
}

// ensureSystemPresets guarantees locked system presets exist in the file.
// Called with the mutex already held.
func (s *presetStore) ensureSystemPresets() {
	applyTemplate := defaultCamPresets[len(defaultCamPresets)-1] // apply_config is last
	found := false
	for _, p := range s.presets {
		if p.ID == applyPresetID {
			p.Locked = true // ensure the flag stays set even if file was hand-edited
			found = true
			break
		}
	}
	if !found {
		cp := *applyTemplate
		s.presets = append(s.presets, &cp)
		_ = s.saveLocked()
	}
}

func (s *presetStore) saveLocked() error {
	data, err := json.MarshalIndent(s.presets, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0600)
}

func listCamPresets() []*CamPreset {
	pstore.mu.RLock()
	defer pstore.mu.RUnlock()
	out := make([]*CamPreset, len(pstore.presets))
	for i, p := range pstore.presets {
		cp := *p
		out[i] = &cp
	}
	return out
}

func createCamPreset(p *CamPreset) (*CamPreset, error) {
	pstore.mu.Lock()
	defer pstore.mu.Unlock()
	cp := *p
	cp.ID = newPresetID()
	cp.Locked = false // API-created presets are never locked
	pstore.presets = append(pstore.presets, &cp)
	if err := pstore.saveLocked(); err != nil {
		return nil, err
	}
	ret := cp
	return &ret, nil
}

func updateCamPreset(p *CamPreset) error {
	pstore.mu.Lock()
	defer pstore.mu.Unlock()
	for i, e := range pstore.presets {
		if e.ID == p.ID {
			if e.Locked {
				return ErrPresetLocked
			}
			cp := *p
			cp.Locked = false // cannot un-lock via API
			pstore.presets[i] = &cp
			return pstore.saveLocked()
		}
	}
	return fmt.Errorf("preset %q not found", p.ID)
}

func deleteCamPreset(id string) error {
	pstore.mu.Lock()
	defer pstore.mu.Unlock()
	for i, p := range pstore.presets {
		if p.ID == id {
			if p.Locked {
				return ErrPresetLocked
			}
			pstore.presets = append(pstore.presets[:i], pstore.presets[i+1:]...)
			return pstore.saveLocked()
		}
	}
	return fmt.Errorf("preset %q not found", id)
}

func newPresetID() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
