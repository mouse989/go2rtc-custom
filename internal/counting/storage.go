package counting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CountEvent records a vehicle crossing event.
type CountEvent struct {
	Timestamp int64  `json:"ts"`
	CameraID  string `json:"cameraId"`
	Name      string `json:"name"`
	Count     int    `json:"count"`          // crossings in this event
	Total     int    `json:"total"`          // cumulative total for this camera today
	Direction    string `json:"dir,omitempty"`          // "down" | "up" | "right" | "left"
	VehicleClass string `json:"vehicleClass,omitempty"` // "car" | "motorcycle" | "bus" | "truck"
}

// dailyRecord aggregates all events for one day.
type dailyRecord struct {
	Date   string       `json:"date"`
	Events []CountEvent `json:"events"`
}

// HourlySummary aggregates crossings per camera per hour.
type HourlySummary struct {
	CameraID string       `json:"cameraId"`
	Name     string       `json:"name"`
	Hours    [24]int      `json:"hours"` // index = hour (0-23)
	Total    int          `json:"total"`
}

type dailyStore struct {
	mu  sync.Mutex
	dir string
}

func newDailyStore() *dailyStore {
	dir := "counting_data"
	if app_cfgDir() != "" {
		dir = filepath.Join(app_cfgDir(), "counting_data")
	}
	_ = os.MkdirAll(dir, 0755)
	return &dailyStore{dir: dir}
}

func app_cfgDir() string {
	// Reuse the same config dir convention as other modules
	if configFile != "" {
		return filepath.Dir(configFile)
	}
	return ""
}

// append adds a CountEvent to today's daily file.
func (s *dailyStore) append(ev CountEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	day := time.Unix(ev.Timestamp, 0).Format("2006-01-02")
	path := filepath.Join(s.dir, day+".json")

	rec := dailyRecord{Date: day}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &rec)
	}
	rec.Events = append(rec.Events, ev)

	data, _ := json.MarshalIndent(rec, "", "  ")
	return os.WriteFile(path, data, 0644)
}

// getEvents returns all events for a given date (YYYY-MM-DD).
func (s *dailyStore) getEvents(date string) ([]CountEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.dir, date+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rec dailyRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	return rec.Events, nil
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

// hourlySummary aggregates events for a date into per-camera hourly counts.
func (s *dailyStore) hourlySummary(date string) ([]HourlySummary, error) {
	events, err := s.getEvents(date)
	if err != nil {
		return nil, err
	}

	byCamera := map[string]*HourlySummary{}
	c := getConfig()
	nameOf := map[string]string{}
	for _, cam := range c.Cameras {
		nameOf[cam.ID] = cam.Name
	}

	for _, ev := range events {
		h := time.Unix(ev.Timestamp, 0).Hour()
		hs, ok := byCamera[ev.CameraID]
		if !ok {
			hs = &HourlySummary{CameraID: ev.CameraID, Name: nameOf[ev.CameraID]}
			byCamera[ev.CameraID] = hs
		}
		hs.Hours[h] += ev.Count
		hs.Total += ev.Count
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
