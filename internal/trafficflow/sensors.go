package trafficflow

// sensors.go — two related stores, mirroring internal/aievent/categories.go's
// ObservedCategory/CategoryRule split:
//
//   - ObservedSensor: auto-logged, one entry per distinct source_uuid the
//     push endpoint has ever seen, with Hawkwise's own reported coordinates
//     as a convenience suggestion. Needs zero admin setup — exists purely so
//     an admin can see what Hawkwise is actually sending before mapping it.
//   - Sensor: admin-authored, maps a source_uuid to an internal map point
//     (ID, label, lat/lon the admin controls). Per the user's explicit
//     instruction, this is NOT auto-trusted from Hawkwise's per-push
//     lat/lng — an admin must create/confirm it.
//
// A push whose source_uuid has no Sensor mapping is still stored (never
// dropped — see records.go's Ingest) and just excluded from the map layer
// until an admin maps it.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Sensor is one admin-managed mapping from a Hawkwise camera to a point on
// the map.
type Sensor struct {
	ID           string    `json:"id"`
	SourceUUID   string    `json:"source_uuid"`             // general.camera.source_uuid from Hawkwise — the join key on every push
	HardwareUUID string    `json:"hardware_uuid,omitempty"` // reference only, not required to be unique
	Label        string    `json:"label"`                   // admin-chosen display name
	Lat          float64   `json:"lat"`                     // admin-confirmed — may differ slightly from Hawkwise's own reported coordinate
	Lon          float64   `json:"lon"`
	Enabled      bool      `json:"enabled"` // false = hide from the map layer; data keeps being stored
	CreatedAt    time.Time `json:"created_at"`
}

// ObservedSensor is one distinct source_uuid seen in incoming pushes,
// auto-maintained by observeSensor.
type ObservedSensor struct {
	SourceUUID   string    `json:"source_uuid"`
	HardwareUUID string    `json:"hardware_uuid,omitempty"`
	ReportedLat  *float64  `json:"reported_lat,omitempty"` // Hawkwise's own camera.latitude from the most recent push
	ReportedLng  *float64  `json:"reported_lng,omitempty"`
	Count        int       `json:"count"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
}

type sensorStore struct {
	mu       sync.Mutex
	sensF    string
	obsF     string
	sensors  map[string]*Sensor         // by ID
	observed map[string]*ObservedSensor // by source_uuid
}

var sstore *sensorStore

func initSensors(dir string) error {
	sstore = &sensorStore{
		sensF:    filepath.Join(dir, "sensors.json"),
		obsF:     filepath.Join(dir, "observed_sensors.json"),
		sensors:  map[string]*Sensor{},
		observed: map[string]*ObservedSensor{},
	}
	if err := sstore.loadSensors(); err != nil {
		return err
	}
	return sstore.loadObserved()
}

func (s *sensorStore) loadSensors() error {
	data, err := os.ReadFile(s.sensF)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []*Sensor
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	for _, sn := range list {
		s.sensors[sn.ID] = sn
	}
	return nil
}

func (s *sensorStore) saveSensorsLocked() error {
	list := make([]*Sensor, 0, len(s.sensors))
	for _, sn := range s.sensors {
		list = append(list, sn)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Label < list[j].Label })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.sensF, data, 0644)
}

func (s *sensorStore) loadObserved() error {
	data, err := os.ReadFile(s.obsF)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []*ObservedSensor
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	for _, o := range list {
		s.observed[o.SourceUUID] = o
	}
	return nil
}

func (s *sensorStore) saveObservedLocked() error {
	list := make([]*ObservedSensor, 0, len(s.observed))
	for _, o := range s.observed {
		list = append(list, o)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].LastSeen.After(list[j].LastSeen) })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.obsF, data, 0644)
}

// observeSensor records one sighting of sourceUUID — called on every push,
// regardless of whether a Sensor mapping already exists, so the "what has
// Hawkwise actually sent" view stays complete even after sensors are mapped.
func observeSensor(sourceUUID, hardwareUUID string, lat, lng *float64) {
	if sourceUUID == "" {
		return
	}
	now := time.Now()
	sstore.mu.Lock()
	defer sstore.mu.Unlock()
	o, ok := sstore.observed[sourceUUID]
	if !ok {
		o = &ObservedSensor{SourceUUID: sourceUUID, FirstSeen: now}
		sstore.observed[sourceUUID] = o
	}
	o.Count++
	o.LastSeen = now
	if hardwareUUID != "" {
		o.HardwareUUID = hardwareUUID
	}
	if lat != nil && lng != nil {
		o.ReportedLat, o.ReportedLng = lat, lng
	}
	_ = sstore.saveObservedLocked() // best-effort — never blocks ingestion on a write failure
}

// ListObservedSensors returns every distinct source_uuid seen, most recently
// seen first.
func ListObservedSensors() []ObservedSensor {
	sstore.mu.Lock()
	defer sstore.mu.Unlock()
	out := make([]ObservedSensor, 0, len(sstore.observed))
	for _, o := range sstore.observed {
		out = append(out, *o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

// ListSensors returns every configured mapping, by label.
func ListSensors() []Sensor {
	sstore.mu.Lock()
	defer sstore.mu.Unlock()
	out := make([]Sensor, 0, len(sstore.sensors))
	for _, sn := range sstore.sensors {
		out = append(out, *sn)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// sensorBySourceUUIDLocked finds a sensor by its source_uuid. Caller must
// hold sstore.mu.
func sensorBySourceUUIDLocked(sourceUUID string) (*Sensor, bool) {
	for _, sn := range sstore.sensors {
		if sn.SourceUUID == sourceUUID {
			return sn, true
		}
	}
	return nil, false
}

// GetSensorBySourceUUID returns a copy of the mapping for sourceUUID, if any.
func GetSensorBySourceUUID(sourceUUID string) (*Sensor, bool) {
	sstore.mu.Lock()
	defer sstore.mu.Unlock()
	sn, ok := sensorBySourceUUIDLocked(sourceUUID)
	if !ok {
		return nil, false
	}
	cp := *sn
	return &cp, true
}

// CreateSensor registers a new source_uuid -> map point mapping. SourceUUID
// must be non-empty and not already mapped by another Sensor — Hawkwise's
// doc describes source_uuid as the camera's own stable key, so a 1:1
// mapping is enforced here.
func CreateSensor(sn Sensor) (*Sensor, error) {
	sn.SourceUUID = strings.TrimSpace(sn.SourceUUID)
	sn.Label = strings.TrimSpace(sn.Label)
	if sn.SourceUUID == "" {
		return nil, errTrafficFlow("source_uuid required")
	}
	if sn.Label == "" {
		return nil, errTrafficFlow("label required")
	}

	sstore.mu.Lock()
	defer sstore.mu.Unlock()
	if _, exists := sensorBySourceUUIDLocked(sn.SourceUUID); exists {
		return nil, errTrafficFlow("source_uuid already mapped to another sensor")
	}
	sn.ID = fmt.Sprintf("hws-%d", time.Now().UnixNano())
	sn.CreatedAt = time.Now()
	sstore.sensors[sn.ID] = &sn
	if err := sstore.saveSensorsLocked(); err != nil {
		delete(sstore.sensors, sn.ID)
		return nil, err
	}
	cp := sn
	return &cp, nil
}

// UpdateSensor replaces an existing mapping's fields (label, lat/lon,
// enabled, hardware_uuid). SourceUUID may be changed too, subject to the
// same uniqueness check as CreateSensor.
func UpdateSensor(id string, upd Sensor) (*Sensor, error) {
	upd.SourceUUID = strings.TrimSpace(upd.SourceUUID)
	upd.Label = strings.TrimSpace(upd.Label)
	if upd.SourceUUID == "" {
		return nil, errTrafficFlow("source_uuid required")
	}
	if upd.Label == "" {
		return nil, errTrafficFlow("label required")
	}

	sstore.mu.Lock()
	defer sstore.mu.Unlock()
	existing, ok := sstore.sensors[id]
	if !ok {
		return nil, errTrafficFlowNotFound
	}
	if other, exists := sensorBySourceUUIDLocked(upd.SourceUUID); exists && other.ID != id {
		return nil, errTrafficFlow("source_uuid already mapped to another sensor")
	}
	upd.ID = id
	upd.CreatedAt = existing.CreatedAt
	sstore.sensors[id] = &upd
	if err := sstore.saveSensorsLocked(); err != nil {
		return nil, err
	}
	cp := upd
	return &cp, nil
}

// DeleteSensor removes a mapping — matching pushes keep being stored under
// their source_uuid, they just stop appearing on the map.
func DeleteSensor(id string) error {
	sstore.mu.Lock()
	defer sstore.mu.Unlock()
	if _, ok := sstore.sensors[id]; !ok {
		return errTrafficFlowNotFound
	}
	delete(sstore.sensors, id)
	return sstore.saveSensorsLocked()
}

type trafficFlowError string

func (e trafficFlowError) Error() string { return string(e) }
func errTrafficFlow(msg string) error    { return trafficFlowError(msg) }

var errTrafficFlowNotFound = trafficFlowError("not found")
