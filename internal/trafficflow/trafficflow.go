// Package trafficflow stores vehicle-flow data pushed in by Hawkwise's
// Advanced Traffic Density product (see
// docs/hawkwise-flow-integration-plan.md) and the admin-managed mapping
// between Hawkwise's own camera key (source_uuid) and a map point.
//
// Deliberately a separate package from internal/counting (that module's
// Slot5/Station model has no occupancy/traffic_status/per-lane fields and
// has never accepted an external push) and from internal/aievent (that
// module's "event opens, then is explicitly terminated" lifecycle doesn't
// fit a continuous time-series feed). This package borrows conventions from
// both: the vnLocation + day-bucketed-JSON pattern from internal/aievent,
// and the simple pin-on-a-map Sensor/Station shape from internal/counting —
// per the user's explicit instruction, this stays its own subsystem and is
// never merged into counting's Station table.
package trafficflow

import (
	"os"
	"path/filepath"
	"time"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/rs/zerolog"
)

var log zerolog.Logger

var dataDir string

// Init loads sensor mappings, the observed-sensor registry, and the flow
// record index, then registers HTTP handlers — called once at startup (see
// main.go's module list), same no-argument convention as
// internal/aievent.Init.
func Init() {
	log = app.GetLogger("trafficflow")

	dataDir = "trafficflow_data"
	if app.ConfigPath != "" {
		dataDir = filepath.Join(filepath.Dir(app.ConfigPath), "trafficflow_data")
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Error().Err(err).Msg("[trafficflow] cannot create data dir")
	}

	if err := initSensors(dataDir); err != nil {
		log.Warn().Err(err).Msg("[trafficflow] sensor store load failed (continuing)")
	}
	if err := initRecords(dataDir); err != nil {
		log.Warn().Err(err).Msg("[trafficflow] flow record index load failed (continuing)")
	}
	primeLatestFromDisk()

	RegisterHandlers()
	startRetentionSweeper()

	log.Info().Str("dir", dataDir).Msg("[trafficflow] ready")
}

// flowRetentionDays mirrors internal/counting's default retention (30 days)
// per docs/hawkwise-flow-integration-plan.md §5.1 — not yet configurable via
// AppSettings, since the user hasn't asked for a different value.
const flowRetentionDays = 30

func startRetentionSweeper() {
	go func() {
		for range time.Tick(6 * time.Hour) {
			n := deleteRecordFilesOlderThan(flowRetentionDays)
			if n > 0 {
				log.Info().Int("deleted_files", n).Msg("[trafficflow] retention sweep")
			}
		}
	}()
}

// vnLocation anchors day-bucketing and Hawkwise's local timestamp strings to
// Vietnam time — same convention and fallback (minimal containers without
// IANA tzdata) as internal/aievent/internal/incidents' own vnLocation.
var vnLocation = loadVNLocation()

func loadVNLocation() *time.Location {
	if loc, err := time.LoadLocation("Asia/Ho_Chi_Minh"); err == nil {
		return loc
	}
	return time.FixedZone("ICT", 7*3600)
}

// parseHawkwiseTime parses Hawkwise's "yyyy-MM-dd HH:mm:ss" local-time
// strings (no zone suffix — GMT+7 implied) using vnLocation, NOT
// time.Parse's UTC default — see docs/hawkwise-flow-integration-plan.md
// §1.3's timestamp row for why this distinction matters.
func parseHawkwiseTime(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02 15:04:05", s, vnLocation)
}

// dayKeyFor formats t (converted to Vietnam time) as "2006-01-02".
func dayKeyFor(t time.Time) string {
	return t.In(vnLocation).Format("2006-01-02")
}
