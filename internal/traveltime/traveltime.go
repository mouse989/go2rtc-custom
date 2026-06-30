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

// TTITier defines one colour band in the TTI chart.
// MaxTTI is the exclusive upper bound (TTI < MaxTTI → this tier).
// The last tier must have MaxTTI == 0 (catch-all for any remaining values).
type TTITier struct {
	MaxTTI float64 `json:"maxTTI"` // exclusive upper bound; 0 = last tier (no upper limit)
	Color  string  `json:"color"`  // hex colour for chart bars, e.g. "#22c55e"
	Label  string  `json:"label"`  // display label shown in legend and tooltip
}

// TTIAppearance holds the configurable visual settings for the TTI chart.
type TTIAppearance struct {
	Tiers       []TTITier `json:"tiers"`
	NoDataColor string    `json:"noDataColor"` // colour for no-data legend swatch
}

// Config holds the travel-time module configuration.
// The VietMap API key is shared from app Settings (not stored here).
type Config struct {
	GoogleScriptURL string        `json:"googleScriptUrl"` // Optional Google Sheets Apps Script URL
	IntervalMin     int           `json:"intervalMin"`     // Scheduler interval in minutes (≥1)
	Running         bool          `json:"running"`         // Whether the scheduler should auto-start
	Appearance      TTIAppearance `json:"appearance"`      // TTI chart colour thresholds
}

func defaultAppearance() TTIAppearance {
	return TTIAppearance{
		Tiers: []TTITier{
			{MaxTTI: 1.2, Color: "#22c55e", Label: "Thông thoáng"},
			{MaxTTI: 1.35, Color: "#84cc16", Label: "Bình thường"},
			{MaxTTI: 1.5, Color: "#f59e0b", Label: "Chậm"},
			{MaxTTI: 0, Color: "#ef4444", Label: "Tắc nghẽn"},
		},
		NoDataColor: "#334155",
	}
}

var (
	cfgMu      sync.RWMutex
	cfg        Config
	configFile string
)

func defaultConfig() Config {
	return Config{IntervalMin: 15, Appearance: defaultAppearance()}
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
	// Backfill appearance defaults for configs saved before this field existed.
	if len(cfg.Appearance.Tiers) == 0 {
		cfg.Appearance = defaultAppearance()
	}

	routesFile := "travel_routes.json"
	logsDir := "travel_logs"
	holidayFile := "holiday_overrides.json"
	if app.ConfigPath != "" {
		dir := filepath.Dir(app.ConfigPath)
		routesFile = filepath.Join(dir, "travel_routes.json")
		logsDir = filepath.Join(dir, "travel_logs")
		holidayFile = filepath.Join(dir, "holiday_overrides.json")
	}
	initRoutes(routesFile)
	initLogs(logsDir)
	initHolidayStore(holidayFile)

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
