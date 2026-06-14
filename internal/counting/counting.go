package counting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/rs/zerolog"
)

var (
	log        zerolog.Logger
	cfgMu      sync.RWMutex
	cfg        Config
	configFile string
	mgr        *Manager
)

// Config holds the counting module configuration.
type Config struct {
	Running      bool           `json:"running"`
	DefaultFPS   float64        `json:"defaultFps"`   // processing FPS per camera (1-5)
	BlobMinArea  int            `json:"blobMinArea"`  // minimum blob area in pixels
	BlobMaxArea  int            `json:"blobMaxArea"`  // maximum blob area in pixels
	Threshold    float32        `json:"threshold"`    // background subtraction threshold (10-60)
	LearningRate float32        `json:"learningRate"` // background model adaptation rate
	FrameWidth   int            `json:"frameWidth"`   // resize width for processing (default 320)
	Cameras      []CameraConfig `json:"cameras"`
	Storage      StorageConfig  `json:"storage"`
}

// CameraConfig defines per-camera counting settings.
type CameraConfig struct {
	ID         string  `json:"id"`
	StreamName string  `json:"streamName"` // go2rtc stream name
	Name       string  `json:"name"`       // display name
	FPS        float64 `json:"fps"`        // 0 = use Config.DefaultFPS
	LinePos    float64 `json:"linePos"`    // 0.0-1.0, relative crossing line position
	LineAxis   string  `json:"lineAxis"`   // "h" (horizontal) or "v" (vertical)
	Enabled    bool    `json:"enabled"`
	Tier       int     `json:"tier"` // 1=normal, 2=important, 3=critical
}

// StorageConfig holds data retention settings.
type StorageConfig struct {
	Enabled       bool `json:"enabled"`
	RetentionDays int  `json:"retentionDays"`
}

func defaultConfig() Config {
	return Config{
		Running:      false,
		DefaultFPS:   2.5,
		BlobMinArea:  200,
		BlobMaxArea:  30000,
		Threshold:    28,
		LearningRate: 0.005,
		FrameWidth:   320,
		Cameras:      []CameraConfig{},
		Storage:      StorageConfig{Enabled: true, RetentionDays: 30},
	}
}

func Init() {
	log = app.GetLogger("counting")

	configFile = "counting.json"
	if app.ConfigPath != "" {
		configFile = filepath.Join(filepath.Dir(app.ConfigPath), "counting.json")
	}

	cfg = defaultConfig()
	if data, err := os.ReadFile(configFile); err == nil {
		if err2 := json.Unmarshal(data, &cfg); err2 != nil {
			log.Warn().Err(err2).Msg("[counting] bad config, using defaults")
			cfg = defaultConfig()
		}
	}

	mgr = newManager()
	registerAPI()

	if cfg.Running {
		mgr.startAll()
	}

	log.Info().Msg("[counting] ready")
}

func getConfig() Config {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return cfg
}

func saveConfig() error {
	cfgMu.RLock()
	data, _ := json.MarshalIndent(cfg, "", "  ")
	cfgMu.RUnlock()
	return os.WriteFile(configFile, data, 0644)
}
