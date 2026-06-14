package auth

import (
	"encoding/json"
	"os"
	"sync"
)

// HeatmapConfig holds the user-configurable heatmap rendering parameters.
type HeatmapConfig struct {
	R5  int    `json:"r5"`   // heatmap-radius at zoom 5
	R15 int    `json:"r15"`  // heatmap-radius at zoom 15
	I5  int    `json:"i5"`   // heatmap-intensity×10 at zoom 5
	I15 int    `json:"i15"`  // heatmap-intensity×10 at zoom 15
	Op  int    `json:"op"`   // opacity 0-100
	C1  string `json:"c1"`   // color: low
	C1a int    `json:"c1a"`  // alpha 0-100
	C2  string `json:"c2"`   // color: mid
	C2a int    `json:"c2a"`
	C3  string `json:"c3"`   // color: high
	C3a int    `json:"c3a"`
	C4  string `json:"c4"`   // color: max
	C4a int    `json:"c4a"`
	Crf int    `json:"crf"`  // dot circle-radius at JF=0
	Crt int    `json:"crt"`  // dot circle-radius at JF=10
}

// AppSettings holds persisted application-level settings.
type AppSettings struct {
	VietmapAPIKey        string         `json:"vietmap_api_key"`
	SnapshotIntervalSec  int            `json:"snapshot_interval_sec"`   // 0 → default 15 s
	SnapshotConcurrency  int            `json:"snapshot_concurrency"`    // 0 → auto (interval_sec × 10)
	MapSearchRadiusKm    float64        `json:"map_search_radius_km"`    // 0 → default 1 km
	HeatmapCfg           *HeatmapConfig `json:"heatmap_cfg,omitempty"`
}

var (
	settingsMu   sync.RWMutex
	appSettings  AppSettings
	settingsFile string
)

func initSettings(path string) error {
	settingsFile = path
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // start empty; file created on first save
		}
		return err
	}
	return json.Unmarshal(data, &appSettings)
}

// GetSettings returns a copy of the current settings.
func GetSettings() AppSettings {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return appSettings
}

// UpdateSettings replaces settings and persists them to disk.
func UpdateSettings(s AppSettings) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(settingsFile, data, 0644); err != nil {
		return err
	}
	settingsMu.Lock()
	appSettings = s
	settingsMu.Unlock()
	return nil
}
