package auth

// devices.go — Device inventory: types + devices + ping interval config
//
// Stored in devices.json next to users.json.
// Device types are user-defined categories (Switch, Router, NVR, Camera, etc.).
// Devices hold name, IP, and an assigned type.

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// DeviceType is a user-defined category of network device.
type DeviceType struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Device is one monitored network device.
type Device struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	IP     string `json:"ip"`
	TypeID string `json:"type_id"`
}

type devicesConfig struct {
	Types           []*DeviceType `json:"types"`
	Devices         []*Device     `json:"devices"`
	PingIntervalSec int           `json:"ping_interval_sec"` // 0 → default 30
}

type devicesStore struct {
	mu   sync.RWMutex
	path string
	data devicesConfig
}

var devStore *devicesStore

func genShortID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func initDevices(path string) error {
	devStore = &devicesStore{path: path}
	return devStore.load()
}

func (s *devicesStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		s.data = devicesConfig{
			Types:           []*DeviceType{},
			Devices:         []*Device{},
			PingIntervalSec: 30,
		}
		return nil
	}
	if err != nil {
		return err
	}
	if err = json.Unmarshal(data, &s.data); err != nil {
		return err
	}
	if s.data.Types == nil {
		s.data.Types = []*DeviceType{}
	}
	if s.data.Devices == nil {
		s.data.Devices = []*Device{}
	}
	if s.data.PingIntervalSec < 1 {
		s.data.PingIntervalSec = 30
	}
	return nil
}

func (s *devicesStore) save() error {
	data, err := json.MarshalIndent(&s.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0600)
}

// ── Device Types ──────────────────────────────────────────────────

func listDeviceTypes() []*DeviceType {
	if devStore == nil {
		return []*DeviceType{}
	}
	devStore.mu.RLock()
	defer devStore.mu.RUnlock()
	out := make([]*DeviceType, len(devStore.data.Types))
	for i, t := range devStore.data.Types {
		cp := *t
		out[i] = &cp
	}
	return out
}

func upsertDeviceType(t *DeviceType) error {
	devStore.mu.Lock()
	defer devStore.mu.Unlock()
	if t.ID == "" {
		t.ID = genShortID()
	}
	for i, existing := range devStore.data.Types {
		if existing.ID == t.ID {
			cp := *t
			devStore.data.Types[i] = &cp
			return devStore.save()
		}
	}
	cp := *t
	devStore.data.Types = append(devStore.data.Types, &cp)
	return devStore.save()
}

func deleteDeviceType(id string) error {
	devStore.mu.Lock()
	defer devStore.mu.Unlock()
	for i, t := range devStore.data.Types {
		if t.ID == id {
			devStore.data.Types = append(devStore.data.Types[:i], devStore.data.Types[i+1:]...)
			return devStore.save()
		}
	}
	return nil
}

// ── Devices ───────────────────────────────────────────────────────

func listDevices() []*Device {
	if devStore == nil {
		return []*Device{}
	}
	devStore.mu.RLock()
	defer devStore.mu.RUnlock()
	out := make([]*Device, len(devStore.data.Devices))
	for i, d := range devStore.data.Devices {
		cp := *d
		out[i] = &cp
	}
	return out
}

func upsertDevice(d *Device) error {
	devStore.mu.Lock()
	defer devStore.mu.Unlock()
	if d.ID == "" {
		d.ID = genShortID()
	}
	for i, existing := range devStore.data.Devices {
		if existing.ID == d.ID {
			cp := *d
			devStore.data.Devices[i] = &cp
			return devStore.save()
		}
	}
	cp := *d
	devStore.data.Devices = append(devStore.data.Devices, &cp)
	return devStore.save()
}

func deleteDevice(id string) error {
	devStore.mu.Lock()
	defer devStore.mu.Unlock()
	for i, d := range devStore.data.Devices {
		if d.ID == id {
			devStore.data.Devices = append(devStore.data.Devices[:i], devStore.data.Devices[i+1:]...)
			return devStore.save()
		}
	}
	return nil
}

// ── Ping interval ─────────────────────────────────────────────────

func getPingIntervalSec() int {
	if devStore == nil {
		return 30
	}
	devStore.mu.RLock()
	defer devStore.mu.RUnlock()
	if devStore.data.PingIntervalSec < 1 {
		return 30
	}
	return devStore.data.PingIntervalSec
}

func setPingIntervalSec(sec int) error {
	if sec < 1 {
		sec = 30
	}
	devStore.mu.Lock()
	devStore.data.PingIntervalSec = sec
	err := devStore.save()
	devStore.mu.Unlock()
	return err
}
