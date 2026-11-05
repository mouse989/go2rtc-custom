package trafficmetrics

// config.go — admin-configurable calibration constants and the 6-level LOS
// (level of service, A–F) table, persisted as trafficmetrics.json next to
// the other module config files. Mirrors internal/traveltime/traveltime.go's
// Config/Init/getConfig/saveConfig shape exactly, and internal/auth's
// jam_color.go for the sort-tolerant, validate-before-save style of a small
// admin-edited table.
//
// LOS is classified directly from TTI, per HCM (Highway Capacity Manual,
// AASHTO/TRB)'s Urban Streets methodology: LOS there is defined on average
// travel speed as a percentage of free-flow speed (= 1/TTI), NOT on a BPR
// volume-delay function — HCM explicitly uses speed ratio instead of BPR
// for signalized arterials because BPR (designed for uninterrupted highway
// flow) is documented to understate delay once signals dominate travel
// time. The 6 bands below are HCM's own A–F speed-ratio breakpoints
// (≥85/67/51/39/29% of free-flow speed) converted to TTI = 1/ratio. This
// replaced an earlier BPR-inverse "Z" (volume-to-capacity) estimate that
// had no real traffic-volume data to calibrate against and, taken as
// originally specified, misclassified TTI≈1.5 (moderate, ~27km/h on a
// typical VN arterial) as the worst LOS tier — removed entirely, not just
// hidden, per explicit decision.
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
// slice of the TTI axis starting at TTIMin, up to (but not including) the
// next level's TTIMin — the last level (F) has no upper bound.
type LOSLevel struct {
	Label  string  `json:"label"`  // "A".."F"
	TTIMin float64 `json:"ttiMin"` // inclusive lower bound of TTI, ascending across the table
	Color  string  `json:"color"`  // "#rrggbb"
	Action string  `json:"action"` // suggested ITS action text
}

// Config holds the LOS module's calibration constants — all editable via
// Admin Config (PUT /api/trafficmetrics/config, admin only; GET is any
// logged-in user, same convention as /api/jam-color-cfg and
// /api/traveltime/appearance).
type Config struct {
	FusionWeightTTI    float64    `json:"fusionWeightTTI"`    // w1 — weight on speed-derived TTI when fusing with JF
	FusionWeightJF     float64    `json:"fusionWeightJF"`     // w2 — weight on JamFactor-implied TTI
	BottleneckJFSpread float64    `json:"bottleneckJFSpread"` // min JF spread across subSegments to flag a bottleneck
	AnomalyJFDelta     float64    `json:"anomalyJFDelta"`     // max |JF_actual - JF_implied| before flagging inconsistent
	LOSLevels          []LOSLevel `json:"losLevels"`          // 6 bands, A..F, ascending TTIMin
}

// defaultLOSLevels is HCM Exhibit 15-2 (Urban Streets LOS, speed-ratio
// bands) converted to TTI = 1/ratio: A ≥85% FFS → TTI<1.18, B 67–85% →
// 1.18–1.49, C 51–67% → 1.49–1.96, D 39–51% → 1.96–2.56, E 29–39% →
// 2.56–3.45, F <29% → TTI>3.45.
func defaultLOSLevels() []LOSLevel {
	return []LOSLevel{
		{Label: "A", TTIMin: 1.00, Color: "#00E676", Action: "Duy trì điều khiển thông thường; giữ chu kỳ đèn tối ưu, duy trì làn sóng xanh"},
		{Label: "B", TTIMin: 1.18, Color: "#64DD17", Action: "Giám sát định kỳ; chuẩn bị kịch bản cao điểm"},
		{Label: "C", TTIMin: 1.49, Color: "#8BC34A", Action: "Giám sát điểm dừng đón trả khách; chuẩn bị kịch bản cao điểm"},
		{Label: "D", TTIMin: 1.96, Color: "#FFC107", Action: "Tăng nhẹ pha xanh trục chính; cảnh báo vận tốc chậm trên VMS"},
		{Label: "E", TTIMin: 2.56, Color: "#FF9800", Action: "Ngưỡng tới hạn! Kích hoạt thuật toán thích ứng (Adaptive Green); khuyến cáo lộ trình phụ"},
		{Label: "F", TTIMin: 3.45, Color: "#D50000", Action: "Dòng sụp đổ! Điều độ viên can thiệp; xả nghẽn cưỡng bức; ngăn chặn tràn ngược (spillback)"},
	}
}

func defaultConfig() Config {
	return Config{
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
// both of those import this package to compute LOS, so this must not
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
	// ever carried the other fields) must not leave readings permanently
	// unclassifiable. Also covers upgrading from the old Z-based config
	// shape (its "zMinPercent" field is simply ignored by json.Unmarshal,
	// leaving ttiMin at its zero value for every level) — if every level's
	// TTIMin is still 0, nothing meaningful was loaded, so fall back to the
	// real TTI defaults rather than leaving 6 bands that all start at 0.
	allZero := true
	for _, lv := range cfg.LOSLevels {
		if lv.TTIMin != 0 {
			allZero = false
			break
		}
	}
	if len(cfg.LOSLevels) == 0 || allZero {
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
// (non-negative weights/thresholds, at least one LOS level with a valid
// label/color/TTIMin) — tolerant of out-of-order TTIMin like auth's
// jam_color.go is for its own stops (sorted before use, not rejected).
func validateConfig(c *Config) error {
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
		if lv.TTIMin < 0 {
			return fmt.Errorf("LOS level %d (%s): ttiMin must be >= 0", i, lv.Label)
		}
		if !hexColorRe.MatchString(lv.Color) {
			return fmt.Errorf("LOS level %d (%s): color %q must be a #rrggbb hex value", i, lv.Label, lv.Color)
		}
	}
	sort.Slice(c.LOSLevels, func(i, j int) bool { return c.LOSLevels[i].TTIMin < c.LOSLevels[j].TTIMin })
	return nil
}

// ClassifyLOS returns the LOS level whose TTIMin is the largest one not
// exceeding tti (levels need not be pre-sorted — sorted defensively here,
// tolerating admin-edited config saved out of order). Returns the zero
// LOSLevel only if levels is empty, which GetConfig() never actually
// returns (Init backfills defaults).
func ClassifyLOS(tti float64, levels []LOSLevel) LOSLevel {
	if len(levels) == 0 {
		return LOSLevel{Label: "—"}
	}
	sorted := make([]LOSLevel, len(levels))
	copy(sorted, levels)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].TTIMin < sorted[j].TTIMin })
	result := sorted[0]
	for _, lv := range sorted {
		if tti >= lv.TTIMin {
			result = lv
		}
	}
	return result
}
