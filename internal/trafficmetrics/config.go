package trafficmetrics

// config.go — admin-configurable calibration constants and the 6-level LOS
// (level of service, TCVN A–F) table, persisted as trafficmetrics.json next
// to the other module config files. Mirrors internal/traveltime/traveltime.go's
// Config/Init/getConfig/saveConfig shape exactly, and internal/auth's
// jam_color.go for the sort-tolerant, validate-before-save style of a small
// admin-edited table.
//
// LOS A/B split: SPEC-ITS-TRAFFIC-ANALYSIS-01 only defines a merged "A-B"
// band (Z < 45%) — it never says where A ends and B begins. The default
// below picks the midpoint (25%) as a starting point; there is no
// authoritative source for this split, so it is expected to be retuned via
// this same admin config once real data is available to validate against.
import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/rs/zerolog"
)

var log zerolog.Logger

// LOSLevel is one of the 6 Level-of-Service bands (A–F), each a contiguous
// slice of the Z% (capacity utilization) axis starting at ZMinPercent, up to
// (but not including) the next level's ZMinPercent — the last level (F)
// has no upper bound.
type LOSLevel struct {
	Label       string  `json:"label"`       // "A".."F"
	ZMinPercent float64 `json:"zMinPercent"` // inclusive lower bound of Z%, ascending across the table
	Color       string  `json:"color"`       // "#rrggbb"
	Action      string  `json:"action"`      // suggested ITS action text
}

// Config holds the Z/LOS module's calibration constants — all editable via
// Admin Config (PUT /api/trafficmetrics/config, admin only; GET is any
// logged-in user, same convention as /api/jam-color-cfg and
// /api/traveltime/appearance).
type Config struct {
	Alpha              float64    `json:"alpha"`              // BPR model exponent base (spec default 0.50)
	Beta               float64    `json:"beta"`               // BPR model exponent (spec default 2.20)
	FusionWeightTTI    float64    `json:"fusionWeightTTI"`    // w1 — weight on speed-derived TTI (spec default 0.65)
	FusionWeightJF     float64    `json:"fusionWeightJF"`     // w2 — weight on JamFactor-implied TTI (spec default 0.35)
	BottleneckJFSpread float64    `json:"bottleneckJFSpread"` // min JF spread across subSegments to flag a bottleneck (spec default 3.0)
	AnomalyJFDelta     float64    `json:"anomalyJFDelta"`     // max |JF_actual - JF_implied| before flagging inconsistent (spec default 2.5)
	LOSLevels          []LOSLevel `json:"losLevels"`          // 6 bands, A..F, ascending ZMinPercent
}

func defaultLOSLevels() []LOSLevel {
	return []LOSLevel{
		{Label: "A", ZMinPercent: 0, Color: "#00E676", Action: "Duy trì điều khiển thông thường; giữ chu kỳ đèn tối ưu, duy trì làn sóng xanh"},
		{Label: "B", ZMinPercent: 35, Color: "#64DD17", Action: "Giám sát định kỳ; chuẩn bị kịch bản cao điểm"},
		{Label: "C", ZMinPercent: 50, Color: "#8BC34A", Action: "Giám sát điểm dừng đón trả khách; chuẩn bị kịch bản cao điểm"},
		{Label: "D", ZMinPercent: 75, Color: "#FFC107", Action: "Tăng nhẹ pha xanh trục chính; cảnh báo vận tốc chậm trên VMS"},
		{Label: "E", ZMinPercent: 90, Color: "#FF9800", Action: "Ngưỡng tới hạn! Kích hoạt thuật toán thích ứng (Adaptive Green); khuyến cáo lộ trình phụ"},
		{Label: "F", ZMinPercent: 100, Color: "#D50000", Action: "Dòng sụp đổ! Điều độ viên can thiệp; xả nghẽn cưỡng bức; ngăn chặn tràn ngược (spillback)"},
	}
}

func defaultConfig() Config {
	return Config{
		Alpha: 0.50, Beta: 2.20,
		FusionWeightTTI: 0.65, FusionWeightJF: 0.35,
		BottleneckJFSpread: 3.0, AnomalyJFDelta: 2.5,
		LOSLevels: defaultLOSLevels(),
	}
}

var (
	cfgMu      sync.RWMutex
	cfg        Config
	configFile string
)

// Init loads (or creates) trafficmetrics.json. Registered as its own module
// in main.go, independent of internal/traffic and internal/traveltime —
// both of those import this package to compute Z/LOS, so this must not
// import either of them (avoids a cycle), and lives as a small standalone
// leaf config, same convention traffic.Config/traveltime.Config each follow
// for config that belongs to one function, not auth's global AppSettings.
func Init() {
	log = app.GetLogger("trafficmetrics")

	configFile = "trafficmetrics.json"
	if app.ConfigPath != "" {
		configFile = filepath.Join(filepath.Dir(app.ConfigPath), "trafficmetrics.json")
	}

	cfg = defaultConfig()
	if data, err := os.ReadFile(configFile); err == nil {
		if err2 := json.Unmarshal(data, &cfg); err2 != nil {
			log.Warn().Err(err2).Msg("[trafficmetrics] config parse error, using defaults")
			cfg = defaultConfig()
		}
	}
	// Backfill: a config saved before LOSLevels existed (or a PUT that only
	// ever carried the other fields) must not leave Z permanently unclassifiable.
	if len(cfg.LOSLevels) == 0 {
		cfg.LOSLevels = defaultLOSLevels()
	}

	registerHandlers()

	log.Info().Str("config", configFile).Msg("[trafficmetrics] ready")
}

// GetConfig returns the current calibration config — safe for concurrent
// use, called from internal/traffic and internal/traveltime's own
// scan/collection paths. Falls back to defaultConfig() when LOSLevels is
// empty rather than assuming Init() already ran: internal/traffic and
// internal/traveltime's own isolated package tests import this package
// without going through main.go's module wiring (no Init() call), and a
// real deployment should never classify against an empty LOS table either
// way — same defensive shape Init() itself uses to backfill a config file
// saved before LOSLevels existed.
func GetConfig() Config {
	cfgMu.RLock()
	c := cfg
	cfgMu.RUnlock()
	if len(c.LOSLevels) == 0 {
		return defaultConfig()
	}
	return c
}

func setConfig(c Config) {
	cfgMu.Lock()
	cfg = c
	cfgMu.Unlock()
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

var hexColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// validateConfig checks what the save needs to stay numerically sane
// (positive alpha/beta so InferZ never divides by zero or roots a negative
// base, non-negative weights/thresholds, at least one LOS level with a
// valid label/color) — tolerant of out-of-order ZMinPercent like
// auth's jam_color.go is for its own stops (sorted before use, not rejected).
func validateConfig(c *Config) error {
	if c.Alpha <= 0 {
		return fmt.Errorf("alpha must be > 0")
	}
	if c.Beta <= 0 {
		return fmt.Errorf("beta must be > 0")
	}
	if c.FusionWeightTTI < 0 || c.FusionWeightJF < 0 {
		return fmt.Errorf("fusion weights must be >= 0")
	}
	if c.BottleneckJFSpread < 0 {
		return fmt.Errorf("bottleneckJFSpread must be >= 0")
	}
	if c.AnomalyJFDelta < 0 {
		return fmt.Errorf("anomalyJFDelta must be >= 0")
	}
	if len(c.LOSLevels) == 0 {
		return fmt.Errorf("need at least 1 LOS level")
	}
	for i, lv := range c.LOSLevels {
		if lv.Label == "" {
			return fmt.Errorf("LOS level %d: label required", i)
		}
		if lv.ZMinPercent < 0 {
			return fmt.Errorf("LOS level %d (%s): zMinPercent must be >= 0", i, lv.Label)
		}
		if !hexColorRe.MatchString(lv.Color) {
			return fmt.Errorf("LOS level %d (%s): color %q must be a #rrggbb hex value", i, lv.Label, lv.Color)
		}
	}
	sort.Slice(c.LOSLevels, func(i, j int) bool { return c.LOSLevels[i].ZMinPercent < c.LOSLevels[j].ZMinPercent })
	return nil
}

// ClassifyLOS returns the LOS level whose ZMinPercent is the largest one
// not exceeding zPercent (levels need not be pre-sorted — sorted
// defensively here, tolerating admin-edited config saved out of order).
// Returns the zero LOSLevel only if levels is empty, which GetConfig()
// never actually returns (Init backfills defaults).
func ClassifyLOS(zPercent float64, levels []LOSLevel) LOSLevel {
	if len(levels) == 0 {
		return LOSLevel{Label: "—"}
	}
	sorted := make([]LOSLevel, len(levels))
	copy(sorted, levels)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ZMinPercent < sorted[j].ZMinPercent })
	result := sorted[0]
	for _, lv := range sorted {
		if zPercent >= lv.ZMinPercent {
			result = lv
		}
	}
	return result
}
