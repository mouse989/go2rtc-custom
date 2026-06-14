package traveltime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	_ "time/tzdata" // embed timezone database for Windows

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/rs/zerolog"
)

var log zerolog.Logger

// Config holds the travel-time module configuration.
// The VietMap API key is shared from app Settings (not stored here).
type Config struct {
	GoogleScriptURL string `json:"googleScriptUrl"` // Optional Google Sheets Apps Script URL
	IntervalMin     int    `json:"intervalMin"`     // Scheduler interval in minutes (≥1)
	Running         bool   `json:"running"`         // Whether the scheduler should auto-start
}

var (
	cfgMu      sync.RWMutex
	cfg        Config
	configFile string
)

func defaultConfig() Config {
	return Config{IntervalMin: 15}
}

// Init initialises the travel-time module.
func Init() {
	log = app.GetLogger("traveltime")

	configFile = "traveltime.json"
	if app.ConfigPath != "" {
		configFile = filepath.Join(filepath.Dir(app.ConfigPath), "traveltime.json")
	}

	cfg = defaultConfig()
	if data, err := os.ReadFile(configFile); err == nil {
		if err2 := json.Unmarshal(data, &cfg); err2 != nil {
			log.Warn().Err(err2).Msg("[traveltime] config parse error, using defaults")
			cfg = defaultConfig()
		}
	}

	routesFile := "travel_routes.json"
	logsDir := "travel_logs"
	if app.ConfigPath != "" {
		dir := filepath.Dir(app.ConfigPath)
		routesFile = filepath.Join(dir, "travel_routes.json")
		logsDir = filepath.Join(dir, "travel_logs")
	}
	initRoutes(routesFile)
	initLogs(logsDir)

	registerHandlers()
	registerVietmapHandlers()

	if cfg.Running {
		startScheduler()
	}

	log.Info().Str("config", configFile).Msg("[traveltime] ready")
}

func getConfig() Config {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return cfg
}

func saveConfig() error {
	cfgMu.RLock()
	c := cfg
	cfgMu.RUnlock()
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configFile, data, 0644)
}
