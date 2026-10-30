package traffic

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/rs/zerolog"
)

var log zerolog.Logger

// Region is a named polygon area to monitor.
type Region struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Color   string     `json:"color"`
	Coords  [][2]float64 `json:"coords"` // [[lat,lon],...]
	Enabled bool       `json:"enabled"`
}

// TelegramConfig holds Telegram notification settings.
type TelegramConfig struct {
	Enabled          bool   `json:"enabled"`
	Token            string `json:"token"`
	ChatID           string `json:"chatId"`
	SendRaw          bool   `json:"sendRaw"`
	SendRawImg       bool   `json:"sendRawImg"`
	SendFiltered     bool   `json:"sendFiltered"`
	SendFilteredImg  bool   `json:"sendFilteredImg"`
	SendPersistent   bool   `json:"sendPersistent"`
	SendPersistentImg bool  `json:"sendPersistentImg"`
	TplHeader        string `json:"tplHeader"`
	TplRaw           string `json:"tplRaw"`
	TplFiltered      string `json:"tplFiltered"`
	TplPersistent    string `json:"tplPersistent"`
	TplPoint         string `json:"tplPoint"`
}

// SheetsConfig holds Google Sheets webhook settings.
type SheetsConfig struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url"`
}

// StorageConfig holds local on-disk scan history settings.
type StorageConfig struct {
	Enabled       bool `json:"enabled"`
	RetentionDays int  `json:"retentionDays"` // 0 = keep forever
}

// Config is the full traffic module configuration.
type Config struct {
	APIKey        string         `json:"apiKey"`
	IntervalMin   int            `json:"intervalMin"`
	MinJam        float64        `json:"minJam"`
	ClusterRadius float64        `json:"clusterRadius"`
	PersistRadius float64        `json:"persistRadius"`
	Regions       []Region       `json:"regions"`
	Telegram      TelegramConfig `json:"telegram"`
	Sheets        SheetsConfig   `json:"sheets"`
	Storage       StorageConfig  `json:"storage"`
	Running       bool           `json:"running"`
	// FlowCaptureRetentionDays controls how long flow_capture.db keeps
	// captured traffic/flow API detail (every scan, every enabled region,
	// every field — including the raw JSON verbatim — see
	// flow_capture_parse.go/flow_capture_db.go). Capture itself always
	// runs (no on/off switch — www/flowcapture.html's map depends on it
	// having current data, so it's no longer an opt-in debug toggle), but
	// unlike Storage above this defaults to 0 = keep forever: the whole
	// point of this store is being able to go back and compare against
	// older captures later, so nothing is deleted unless an admin
	// deliberately sets a limit here (same "0 = forever" convention
	// StorageConfig.RetentionDays already uses).
	FlowCaptureRetentionDays int `json:"flowCaptureRetentionDays"`
}

var (
	cfgMu      sync.RWMutex
	cfg        Config
	configFile string

	workerMu   sync.Mutex
	stopCh     chan struct{}
)

func defaultConfig() Config {
	return Config{
		IntervalMin:   15,
		MinJam:        7.0,
		ClusterRadius: 300,
		PersistRadius: 500,
		Storage: StorageConfig{
			Enabled:       true,
			RetentionDays: 30,
		},
		Telegram: TelegramConfig{
			TplHeader:      "🚦 Traffic Report {time}",
			TplRaw:         "📍 Raw jams ({count}):",
			TplFiltered:    "🔍 Filtered jams ({count}):",
			TplPersistent:  "⚠️ Persistent jams ({count}):",
			TplPoint:       "{n}. 🔥 {jf} · {label} · {area}",
		},
	}
}

// Init initialises the traffic module.
func Init() {
	log = app.GetLogger("traffic")

	// Resolve config file path
	configFile = "traffic.json"
	if app.ConfigPath != "" {
		configFile = filepath.Join(filepath.Dir(app.ConfigPath), "traffic.json")
	}

	// Load or create default config
	cfg = defaultConfig()
	if data, err := os.ReadFile(configFile); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			log.Warn().Err(err).Msg("[traffic] config parse error, using defaults")
			cfg = defaultConfig()
		}
	}

	// Always initialized regardless of FlowCaptureEnabled, so flipping that
	// toggle at runtime (no restart) takes effect on the next scan.
	if err := initFlowCaptureDB(dataDir()); err != nil {
		log.Warn().Err(err).Msg("[traffic] flow capture DB init failed — capture disabled")
	}

	registerAPIHandlers()
	registerFlowCapturePageHandlers()

	// Auto-start worker if configured
	if cfg.Running {
		startWorker()
	}

	log.Info().Str("config", configFile).Msg("[traffic] ready")
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

func isRunning() bool {
	workerMu.Lock()
	defer workerMu.Unlock()
	return stopCh != nil
}

func startWorker() {
	workerMu.Lock()
	defer workerMu.Unlock()

	if stopCh != nil {
		return // already running
	}
	ch := make(chan struct{})
	stopCh = ch

	go runWorker(ch)
	log.Info().Msg("[traffic] worker started")
}

func stopWorker() {
	workerMu.Lock()
	ch := stopCh
	stopCh = nil
	workerMu.Unlock()

	if ch != nil {
		close(ch)
		log.Info().Msg("[traffic] worker stopped")
	}
}

// initialScanDelay decides whether runWorker's startup scan should run
// immediately or wait. The worker always used to scan immediately on
// start regardless, but every restart (manual, or the service simply
// being restarted) re-ran that immediate scan even when the previous
// process had scanned the same real-world time window moments earlier,
// inflating point counts for that window. lastScanAt (monitor.go) can't
// detect this itself — it's in-memory only and resets to zero on every
// restart — so the stored daily file is checked instead. Returns 0 to
// scan immediately; otherwise the duration to wait, measured from `now`,
// before the first scan.
func initialScanDelay(now time.Time, intervalMin int) time.Duration {
	if intervalMin <= 0 {
		intervalMin = 15
	}
	dur := time.Duration(intervalMin) * time.Minute
	last, ok := lastStoredScanTime(now)
	if !ok {
		return 0
	}
	if elapsed := now.Sub(last); elapsed < dur {
		return dur - elapsed
	}
	return 0
}

func runWorker(stop chan struct{}) {
	cfgMu.RLock()
	startInterval := cfg.IntervalMin
	cfgMu.RUnlock()

	now := time.Now()
	skipWait := initialScanDelay(now, startInterval)

	if skipWait > 0 {
		addLog("info", "skipping immediate scan on start: last stored scan was recent enough")
		setScanNext(now.Add(skipWait))
		select {
		case <-stop:
			return
		case <-time.After(skipWait):
			if err := runScan(); err != nil {
				addLog("error", "scan failed: "+err.Error())
			}
		}
	} else if err := runScan(); err != nil {
		addLog("error", "scan failed: "+err.Error())
	}

	for {
		cfgMu.RLock()
		interval := cfg.IntervalMin
		cfgMu.RUnlock()

		if interval <= 0 {
			interval = 15
		}

		dur := time.Duration(interval) * time.Minute

		setScanNext(time.Now().Add(dur))

		select {
		case <-stop:
			return
		case <-time.After(dur):
			if err := runScan(); err != nil {
				addLog("error", "scan failed: "+err.Error())
			}
		}
	}
}
