package counting

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Station is a traffic counting station placed on the map.
type Station struct {
	ID       string  `json:"id"`
	CameraID string  `json:"camera_id"`
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	Label    string  `json:"label,omitempty"`
}

var (
	stationsMu     sync.RWMutex
	_stations      []Station
	stationsLoaded bool
)

func stationsPath() string {
	dir := "."
	if d := app_cfgDir(); d != "" {
		dir = d
	}
	return filepath.Join(dir, "counting_stations.json")
}

func loadStationsOnce() {
	if stationsLoaded {
		return
	}
	stationsLoaded = true
	data, err := os.ReadFile(stationsPath())
	if err != nil {
		_stations = []Station{}
		return
	}
	if err := json.Unmarshal(data, &_stations); err != nil {
		_stations = []Station{}
	}
}

func listStations() []Station {
	stationsMu.RLock()
	defer stationsMu.RUnlock()
	loadStationsOnce()
	cp := make([]Station, len(_stations))
	copy(cp, _stations)
	return cp
}

func saveStationsLocked() error {
	data, err := json.MarshalIndent(_stations, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(stationsPath(), data, 0644)
}

func createStation(s Station) (Station, error) {
	stationsMu.Lock()
	defer stationsMu.Unlock()
	loadStationsOnce()
	s.ID = fmt.Sprintf("stn_%d", time.Now().UnixNano())
	_stations = append(_stations, s)
	return s, saveStationsLocked()
}

func updateStation(id string, upd Station) (Station, error) {
	stationsMu.Lock()
	defer stationsMu.Unlock()
	loadStationsOnce()
	for i, s := range _stations {
		if s.ID == id {
			upd.ID = id
			_stations[i] = upd
			return upd, saveStationsLocked()
		}
	}
	return Station{}, fmt.Errorf("station not found: %s", id)
}

func deleteStation(id string) error {
	stationsMu.Lock()
	defer stationsMu.Unlock()
	loadStationsOnce()
	for i, s := range _stations {
		if s.ID == id {
			_stations = append(_stations[:i], _stations[i+1:]...)
			return saveStationsLocked()
		}
	}
	return fmt.Errorf("station not found: %s", id)
}
