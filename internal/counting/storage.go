package counting

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CountEvent records a vehicle crossing event (kept in evRing for live UI; not persisted to disk).
type CountEvent struct {
	Timestamp    int64  `json:"ts"`
	CameraID     string `json:"cameraId"`
	Name         string `json:"name"`
	Count        int    `json:"count"`
	Total        int    `json:"total"`
	Direction    string `json:"dir,omitempty"`
	VehicleClass string `json:"vehicleClass,omitempty"`
}

// Slot5 aggregates vehicle counts for one camera in a 5-minute bucket.
// T is "HH:MM" rounded down to the nearest 5-minute mark (e.g. "14:35").
// DT maps direction → vehicleClass → count for classified crossings.
// N is the total crossing count (all events, including unclassified).
type Slot5 struct {
	T    string                    `json:"t"`
	Cam  string                    `json:"cam"`
	Name string                    `json:"name,omitempty"`
	DT   map[string]map[string]int `json:"dt"`
	N    int                       `json:"n"`
}

// HourlySummary aggregates crossings per camera per hour (for summary endpoint).
type HourlySummary struct {
	CameraID string  `json:"cameraId"`
	Name     string  `json:"name"`
	Hours    [24]int `json:"hours"`
	Total    int     `json:"total"`
}

type dailyStore struct {
	mu        sync.Mutex
	dir       string
	mem       map[string]map[string]*Slot5 // date → (camID+"/"+t) → *Slot5
	dirty     map[string]bool              // date → needs flush
	memLoaded map[string]bool              // date → disk data already merged into mem
	stopCh    chan struct{}
}

func newDailyStore() *dailyStore {
	dir := "counting_data"
	if app_cfgDir() != "" {
		dir = filepath.Join(app_cfgDir(), "counting_data")
	}
	_ = os.MkdirAll(dir, 0755)
	s := &dailyStore{
		dir:       dir,
		mem:       make(map[string]map[string]*Slot5),
		dirty:     make(map[string]bool),
		memLoaded: make(map[string]bool),
		stopCh:    make(chan struct{}),
	}
	go s.flushLoop()
	return s
}

func app_cfgDir() string {
	if configFile != "" {
		return filepath.Dir(configFile)
	}
	return ""
}

// slot5T returns "HH:MM" rounded down to the nearest 5-minute bucket.
func slot5T(ts int64) string {
	t := time.Unix(ts, 0).Local()
	min5 := (t.Minute() / 5) * 5
	return fmt.Sprintf("%02d:%02d", t.Hour(), min5)
}

// dateOf returns "YYYY-MM-DD" for a unix timestamp.
func dateOf(ts int64) string {
	return time.Unix(ts, 0).Local().Format("2006-01-02")
}

// ensureDateLoaded merges disk data for a date into mem (called under mu).
func (s *dailyStore) ensureDateLoaded(date string) {
	if s.memLoaded[date] {
		return
	}
	s.memLoaded[date] = true
	diskSlots, err := s.readSlotsFromDisk(date)
	if err != nil {
		return
	}
	if s.mem[date] == nil {
		s.mem[date] = make(map[string]*Slot5)
	}
	for _, sl := range diskSlots {
		key := sl.Cam + "/" + sl.T
		if _, exists := s.mem[date][key]; !exists {
			cp := *sl
			if cp.DT == nil {
				cp.DT = make(map[string]map[string]int)
			}
			s.mem[date][key] = &cp
		}
	}
}

// addEvent accumulates a counting event into the in-memory 5-minute slot.
func (s *dailyStore) addEvent(ev CountEvent) {
	date := dateOf(ev.Timestamp)
	t := slot5T(ev.Timestamp)
	key := ev.CameraID + "/" + t

	s.mu.Lock()
	defer s.mu.Unlock()

	// Merge disk data on first write for this date so we don't lose prior data.
	s.ensureDateLoaded(date)

	if s.mem[date] == nil {
		s.mem[date] = make(map[string]*Slot5)
	}
	slot, ok := s.mem[date][key]
	if !ok {
		slot = &Slot5{T: t, Cam: ev.CameraID, Name: ev.Name, DT: make(map[string]map[string]int)}
		s.mem[date][key] = slot
	}
	if ev.Name != "" {
		slot.Name = ev.Name
	}
	n := ev.Count
	if n <= 0 {
		n = 1
	}
	slot.N += n
	if ev.Direction != "" && ev.VehicleClass != "" {
		if slot.DT[ev.Direction] == nil {
			slot.DT[ev.Direction] = make(map[string]int)
		}
		slot.DT[ev.Direction][ev.VehicleClass] += n
	}
	s.dirty[date] = true
}

// flushLoop periodically writes dirty dates to disk.
func (s *dailyStore) flushLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.flushAll()
		case <-s.stopCh:
			s.flushAll()
			return
		}
	}
}

// flushAll writes all dirty dates to disk.
func (s *dailyStore) flushAll() {
	s.mu.Lock()
	toFlush := make([]string, 0, len(s.dirty))
	for date := range s.dirty {
		toFlush = append(toFlush, date)
	}
	s.mu.Unlock()

	for _, date := range toFlush {
		s.mu.Lock()
		slots := make([]*Slot5, 0, len(s.mem[date]))
		for _, slot := range s.mem[date] {
			cp := *slot
			s2 := &cp
			// deep-copy DT map
			cp2 := make(map[string]map[string]int, len(slot.DT))
			for d, types := range slot.DT {
				tm := make(map[string]int, len(types))
				for k, v := range types {
					tm[k] = v
				}
				cp2[d] = tm
			}
			s2.DT = cp2
			slots = append(slots, s2)
		}
		s.mu.Unlock()

		if err := s.writeSlotsFile(date, slots); err == nil {
			s.mu.Lock()
			delete(s.dirty, date)
			s.mu.Unlock()
		}
	}
}

// writeSlotsFile serialises slots to the daily JSON file.
func (s *dailyStore) writeSlotsFile(date string, slots []*Slot5) error {
	path := filepath.Join(s.dir, date+".json")
	data, err := json.MarshalIndent(slots, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// readSlotsFromDisk loads slots from the daily file, handling both new ([]Slot5)
// and old (dailyRecord with Events) formats.
func (s *dailyStore) readSlotsFromDisk(date string) ([]*Slot5, error) {
	path := filepath.Join(s.dir, date+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}

	// New format: JSON array of Slot5 (starts with '[')
	if data[0] == '[' {
		var slots []*Slot5
		if err := json.Unmarshal(data, &slots); err != nil {
			return nil, err
		}
		return slots, nil
	}

	// Old format: {"date": "...", "events": [...]}
	var rec struct {
		Events []CountEvent `json:"events"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	return convertEventsToSlots(rec.Events), nil
}

// convertEventsToSlots converts old-format CountEvents into Slot5 aggregates.
func convertEventsToSlots(events []CountEvent) []*Slot5 {
	byKey := make(map[string]*Slot5)
	for _, ev := range events {
		t := slot5T(ev.Timestamp)
		key := ev.CameraID + "/" + t
		slot, ok := byKey[key]
		if !ok {
			slot = &Slot5{T: t, Cam: ev.CameraID, Name: ev.Name, DT: make(map[string]map[string]int)}
			byKey[key] = slot
		}
		if ev.Name != "" {
			slot.Name = ev.Name
		}
		n := ev.Count
		if n <= 0 {
			n = 1
		}
		slot.N += n
		if ev.Direction != "" && ev.VehicleClass != "" {
			if slot.DT[ev.Direction] == nil {
				slot.DT[ev.Direction] = make(map[string]int)
			}
			slot.DT[ev.Direction][ev.VehicleClass] += n
		}
	}
	out := make([]*Slot5, 0, len(byKey))
	for _, sl := range byKey {
		out = append(out, sl)
	}
	return out
}

// getSlots returns all 5-min slots for a given date, optionally filtered by cameraID.
// For the current session's active dates it merges in-memory data; for old dates it reads disk.
func (s *dailyStore) getSlots(date, camID string) ([]*Slot5, error) {
	s.mu.Lock()
	memSlots, inMem := s.mem[date]
	s.mu.Unlock()

	var all []*Slot5

	if inMem {
		// In-memory is authoritative for this date (includes disk data merged on first addEvent).
		s.mu.Lock()
		all = make([]*Slot5, 0, len(memSlots))
		for _, sl := range memSlots {
			all = append(all, sl)
		}
		s.mu.Unlock()
	} else {
		// Historical date — read from disk only.
		var err error
		all, err = s.readSlotsFromDisk(date)
		if err != nil {
			return nil, err
		}
	}

	if camID == "" {
		return all, nil
	}
	// Match exact cam ID or worker-prefixed variant (e.g. "workerID:camID").
	suffix := ":" + camID
	filtered := all[:0]
	for _, sl := range all {
		if sl.Cam == camID || strings.HasSuffix(sl.Cam, suffix) {
			filtered = append(filtered, sl)
		}
	}
	return filtered, nil
}

// listDates returns available data file dates (newest first).
func (s *dailyStore) listDates() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	dates := make([]string, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		name := entries[i].Name()
		if len(name) == 15 && name[10:] == ".json" {
			dates = append(dates, name[:10])
		}
	}
	return dates, nil
}

// hourlySummary aggregates slots for a date into per-camera hourly counts.
func (s *dailyStore) hourlySummary(date string) ([]HourlySummary, error) {
	slots, err := s.getSlots(date, "")
	if err != nil {
		return nil, err
	}

	byCamera := map[string]*HourlySummary{}
	c := getConfig()
	nameOf := map[string]string{}
	configIDs := map[string]bool{}
	for _, cam := range c.Cameras {
		nameOf[cam.ID] = cam.Name
		configIDs[cam.ID] = true
	}

	// resolve maps a slot's stored camera ID back to its canonical config ID.
	// Remote workers push slots prefixed with "<workerID>:" but the map/UI
	// references the unprefixed config ID, so they must be reconciled here.
	resolve := func(slotCam string) string {
		if configIDs[slotCam] {
			return slotCam
		}
		for _, cam := range c.Cameras {
			if cam.ID != "" && strings.HasSuffix(slotCam, ":"+cam.ID) {
				return cam.ID
			}
		}
		return slotCam
	}

	for _, sl := range slots {
		h := 0
		fmt.Sscanf(sl.T, "%d", &h)
		camID := resolve(sl.Cam)
		hs, ok := byCamera[camID]
		if !ok {
			name := nameOf[camID]
			if name == "" {
				name = sl.Name
			}
			hs = &HourlySummary{CameraID: camID, Name: name}
			byCamera[camID] = hs
		}
		hs.Hours[h] += sl.N
		hs.Total += sl.N
	}

	out := make([]HourlySummary, 0, len(byCamera))
	for _, hs := range byCamera {
		out = append(out, *hs)
	}
	return out, nil
}

// deleteOlderThan removes daily files older than retentionDays.
func (s *dailyStore) deleteOlderThan(days int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().AddDate(0, 0, -days).Format("2006-01-02")
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if len(name) == 15 && name[10:] == ".json" && name[:10] < cutoff {
			_ = os.Remove(filepath.Join(s.dir, name))
		}
	}
}

// addSlot merges a Slot5 (received from a remote worker) into the in-memory store.
// The slot's Cam and Name must already be prefixed by the caller (e.g. workerID+":"+cam).
func (s *dailyStore) addSlot(date string, sl *Slot5) {
	key := sl.Cam + "/" + sl.T

	s.mu.Lock()
	defer s.mu.Unlock()

	s.ensureDateLoaded(date)
	if s.mem[date] == nil {
		s.mem[date] = make(map[string]*Slot5)
	}

	existing, ok := s.mem[date][key]
	if !ok {
		cp := *sl
		dtCopy := make(map[string]map[string]int, len(sl.DT))
		for d, types := range sl.DT {
			tm := make(map[string]int, len(types))
			for k, v := range types {
				tm[k] = v
			}
			dtCopy[d] = tm
		}
		cp.DT = dtCopy
		s.mem[date][key] = &cp
	} else {
		// Remote is authoritative for its own cameras — replace slot data.
		existing.N = sl.N
		if sl.Name != "" {
			existing.Name = sl.Name
		}
		existing.DT = make(map[string]map[string]int, len(sl.DT))
		for d, types := range sl.DT {
			tm := make(map[string]int, len(types))
			for k, v := range types {
				tm[k] = v
			}
			existing.DT[d] = tm
		}
	}
	s.dirty[date] = true
}

// stop flushes in-memory data to disk and stops the background goroutine.
func (s *dailyStore) stop() {
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
}
