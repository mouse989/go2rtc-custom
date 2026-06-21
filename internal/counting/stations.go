package counting

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// StationType defines display thresholds for color-coding station icons.
// Count < ThreshYellow → green; ≥ ThreshYellow → yellow;
// ≥ ThreshOrange → orange; ≥ ThreshRed → red.
type StationType struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ThreshYellow int    `json:"thresh_yellow"`
	ThreshOrange int    `json:"thresh_orange"`
	ThreshRed    int    `json:"thresh_red"`
}

// Station is a traffic counting station placed on the map.
type Station struct {
	ID       string  `json:"id"`
	TypeID   string  `json:"type_id,omitempty"`
	CameraID string  `json:"camera_id"`
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	Label    string  `json:"label,omitempty"`
}

// ── Stations ────────────────────────────────────────────────────

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

// ── Station types ────────────────────────────────────────────────

var (
	typesMu     sync.RWMutex
	_stnTypes   []StationType
	typesLoaded bool
)

func stationTypesPath() string {
	dir := "."
	if d := app_cfgDir(); d != "" {
		dir = d
	}
	return filepath.Join(dir, "counting_station_types.json")
}

func loadTypesOnce() {
	if typesLoaded {
		return
	}
	typesLoaded = true
	data, err := os.ReadFile(stationTypesPath())
	if err != nil {
		_stnTypes = []StationType{}
		return
	}
	if err := json.Unmarshal(data, &_stnTypes); err != nil {
		_stnTypes = []StationType{}
	}
}

func listStationTypes() []StationType {
	typesMu.RLock()
	defer typesMu.RUnlock()
	loadTypesOnce()
	cp := make([]StationType, len(_stnTypes))
	copy(cp, _stnTypes)
	return cp
}

func saveTypesLocked() error {
	data, err := json.MarshalIndent(_stnTypes, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(stationTypesPath(), data, 0644)
}

func createStationType(t StationType) (StationType, error) {
	typesMu.Lock()
	defer typesMu.Unlock()
	loadTypesOnce()
	t.ID = fmt.Sprintf("stype_%d", time.Now().UnixNano())
	_stnTypes = append(_stnTypes, t)
	return t, saveTypesLocked()
}

func updateStationType(id string, upd StationType) (StationType, error) {
	typesMu.Lock()
	defer typesMu.Unlock()
	loadTypesOnce()
	for i, t := range _stnTypes {
		if t.ID == id {
			upd.ID = id
			_stnTypes[i] = upd
			return upd, saveTypesLocked()
		}
	}
	return StationType{}, fmt.Errorf("station type not found: %s", id)
}

func deleteStationType(id string) error {
	typesMu.Lock()
	defer typesMu.Unlock()
	loadTypesOnce()
	for i, t := range _stnTypes {
		if t.ID == id {
			_stnTypes = append(_stnTypes[:i], _stnTypes[i+1:]...)
			return saveTypesLocked()
		}
	}
	return fmt.Errorf("station type not found: %s", id)
}
