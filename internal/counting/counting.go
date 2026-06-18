package counting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/workers"
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
	YoloURL      string         `json:"yoloUrl"`      // Python YOLO service URL, default "http://localhost:8765"
	YoloConf     float64        `json:"yoloConf"`     // confidence threshold, default 0.35
	YoloModel    string         `json:"yoloModel"`    // YOLO model filename, default "yolo26n.pt"
	Cameras      []CameraConfig `json:"cameras"`
	Storage      StorageConfig  `json:"storage"`
	CameraOrder  []string       `json:"cameraOrder"` // ordered camera IDs for dashboard display
}

// CameraConfig defines per-camera counting settings.
type CameraConfig struct {
	ID         string  `json:"id"`
	StreamName string  `json:"streamName"` // go2rtc stream name
	Name       string  `json:"name"`       // display name
	FPS        float64 `json:"fps"`        // 0 = use Config.DefaultFPS; tier applies multiplier
	LineHPos   float64 `json:"lineHPos"`   // horizontal line Y position 0.01-0.99 (0=disabled)
	LineVPos   float64 `json:"lineVPos"`   // vertical line X position 0.01-0.99 (0=disabled)
	CountDown  bool    `json:"countDown"`  // count objects crossing H-line top→bottom
	CountUp    bool    `json:"countUp"`    // count objects crossing H-line bottom→top
	CountRight bool    `json:"countRight"` // count objects crossing V-line left→right
	CountLeft  bool    `json:"countLeft"`  // count objects crossing V-line right→left
	Enabled    bool    `json:"enabled"`
	// Tier controls processing FPS relative to camera FPS setting:
	//   1 = full FPS (high-traffic road)
	//   2 = half FPS (medium traffic)
	//   3 = quarter FPS (low traffic / backup)
	Tier     int    `json:"tier"`
	WorkerID string `json:"workerId,omitempty"` // remote worker ID; empty = process locally
	RTSPBase string `json:"rtspBase,omitempty"` // RTSP URL override sent to yolo_counter on the worker
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
		YoloURL:      "http://localhost:8765",
		YoloConf:     0.35,
		YoloModel:    "yolo26n.pt",
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
	autoLaunchYolo() // auto-launch if yolo_counter.exe exists next to binary

	// Register event importer so the workers module can push remote slots
	// into this server's counting store without a circular import.
	workers.SetEventImporter(func(workerID, workerName, date string, raw json.RawMessage) error {
		var slots []*Slot5
		if err := json.Unmarshal(raw, &slots); err != nil {
			return err
		}
		prefix := "[" + workerName + "] "
		for _, sl := range slots {
			// Prefix cam ID and name to avoid collisions with local cameras.
			if !strings.HasPrefix(sl.Cam, workerID+":") {
				sl.Cam = workerID + ":" + sl.Cam
			}
			if sl.Name != "" && !strings.HasPrefix(sl.Name, prefix) {
				sl.Name = prefix + sl.Name
			}
			mgr.store.addSlot(date, sl)
		}
		return nil
	})

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
