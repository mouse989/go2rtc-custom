// Package aievent stores incidents pushed in by external AI detection
// systems (OMNIA/FPT VDS today — see docs/omnia-integration-api-spec.md —
// designed so a second/third source later reuses this same storage and
// display layer, only adding its own payload parser).
//
// Deliberately separate from internal/incidents: that package is the
// manually-curated, staff-entered incident log (incidents.html, Excel
// import/export, reports); this one is an unattended ingestion pipeline fed
// by a machine, with its own lifecycle (an event opens, then is explicitly
// terminated by its source — see Ingest) and its own retention policy
// (attachment images are short-lived; the event record itself is not —
// see images.go). The two are never merged automatically; a human deciding
// an AI-sourced event is worth a real Incident re-enters it the normal way.
package aievent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/rs/zerolog"
)

var log zerolog.Logger

// Status is an AIEvent's lifecycle state.
type Status string

const (
	StatusActive     Status = "active"
	StatusTerminated Status = "terminated"
)

// AIEvent is one incident pushed in by an external AI detection system.
// RecordID (the source system's own identifier) is the primary key — see
// Ingest's idempotency behavior.
type AIEvent struct {
	RecordID    string `json:"record_id"`
	SituationID string `json:"situation_id,omitempty"`
	RecordType  string `json:"record_type,omitempty"`
	EventCode   string `json:"event_code,omitempty"`
	// Source names which Integration (internal/auth) sent this — informational.
	Source string `json:"source,omitempty"`

	CategoryTypeID      int    `json:"category_type_id"`
	CategorySubtypeID   int    `json:"category_subtype_id,omitempty"`
	CategoryDescription string `json:"category_description,omitempty"`
	CategoryRule        string `json:"category_rule,omitempty"`

	Description     string `json:"description,omitempty"`
	SourceProcessor string `json:"source_processor,omitempty"` // event.Source.Processor from the push

	// StartTime is the source's own reported event time. EndTime is the zero
	// value until the source reports one (typically alongside termination).
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time,omitempty"`

	// Lat/Lng: 0,0 means "no coordinate" (same zero-value-as-unset
	// convention internal/incidents uses) — such an event is stored but
	// never shown on the map (ListActive filters it out), same tradeoff
	// handleActive makes in internal/incidents.
	Lat float64 `json:"lat,omitempty"`
	Lng float64 `json:"lng,omitempty"`

	// ImagePaths are relative paths under the images directory (images.go).
	// A path here can go stale (file deleted by the retention sweep) without
	// being removed from this list — see images.go's doc comment for why.
	ImagePaths []string `json:"image_paths,omitempty"`

	Status Status `json:"status"`
	// ReceivedAt is OUR server clock at first ingestion — never the
	// source's self-reported StartTime/publishedAt — specifically so
	// ListActive's time window is immune to clock skew on the sending
	// system's side (the exact bug fixed for internal/incidents' Traffic
	// Event layer; same fix applied here from the start).
	ReceivedAt   time.Time `json:"received_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	TerminatedAt time.Time `json:"terminated_at,omitempty"`
}

var (
	mu    sync.RWMutex
	days  = map[string]map[string]*AIEvent{} // dayKey ("2006-01-02") -> RecordID -> *AIEvent
	index = map[string]string{}              // RecordID -> dayKey, ALWAYS fully loaded (unlike days) so
	// Ingest/Terminate can find a record's day file even before that day's
	// events have been lazily loaded, and even across a server restart.
	dataDir string
)

// Init loads the record index and prepares storage — called once at
// startup (see main.go's module list), same no-argument convention as
// internal/incidents.Init: resolves its own data directory next to the
// config file and its own logger, rather than taking them as parameters.
func Init() {
	log = app.GetLogger("aievent")

	dataDir = "aievent_data"
	if app.ConfigPath != "" {
		dataDir = filepath.Join(filepath.Dir(app.ConfigPath), "aievent_data")
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Error().Err(err).Msg("[aievent] cannot create data dir")
	}
	if err := loadIndexLocked(); err != nil {
		log.Warn().Err(err).Msg("[aievent] record index load failed (continuing)")
	}
	if err := initCategories(dataDir); err != nil {
		log.Warn().Err(err).Msg("[aievent] category store load failed (continuing)")
	}

	RegisterHandlers()
	startImageRetentionSweeper()

	log.Info().Str("dir", dataDir).Msg("[aievent] ready")
}

func indexFile() string { return filepath.Join(dataDir, "record_index.json") }
func dayFile(dayKey string) string {
	return filepath.Join(dataDir, "events_"+dayKey+".json")
}

func loadIndexLocked() error {
	mu.Lock()
	defer mu.Unlock()
	data, err := os.ReadFile(indexFile())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &index)
}

func saveIndexLocked() error {
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(indexFile(), data, 0644)
}

func loadDayLocked(dayKey string) error {
	if _, ok := days[dayKey]; ok {
		return nil
	}
	m := map[string]*AIEvent{}
	data, err := os.ReadFile(dayFile(dayKey))
	if os.IsNotExist(err) {
		days[dayKey] = m
		return nil
	}
	if err != nil {
		days[dayKey] = m
		return err
	}
	var list []*AIEvent
	if err := json.Unmarshal(data, &list); err != nil {
		days[dayKey] = m
		return err
	}
	for _, ev := range list {
		m[ev.RecordID] = ev
	}
	days[dayKey] = m
	return nil
}

func saveDayLocked(dayKey string) error {
	m := days[dayKey]
	list := make([]*AIEvent, 0, len(m))
	for _, ev := range m {
		list = append(list, ev)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ReceivedAt.After(list[j].ReceivedAt) })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dayFile(dayKey), data, 0644)
}

// dayKeyFor formats t (converted to Vietnam time, so the day boundary
// matches local midnight rather than UTC midnight) as "2006-01-02".
func dayKeyFor(t time.Time) string {
	return t.In(vnLocation).Format("2006-01-02")
}

// Ingest creates or updates an event, keyed by ev.RecordID:
//
//   - RecordID never seen before: a new record is created. ReceivedAt is
//     set to now (not ev.StartTime — see AIEvent.ReceivedAt's doc comment),
//     bucketed into today's day-file. Status is StatusTerminated if
//     terminated==true (the "terminated push arrived before any created
//     push" case the API spec documents — stored already-closed, no error),
//     otherwise StatusActive.
//   - RecordID already indexed: the existing record is updated in place —
//     every field in ev overwrites the stored value except RecordID itself
//     and ReceivedAt (preserved: it marks first contact, not last update).
//     terminated==true moves it to StatusTerminated (closing it even if it
//     was previously reopened); terminated==false moves it to StatusActive
//     (reopening a previously-terminated record is accepted, not rejected —
//     see the API spec's idempotency section).
//
// Returns created=true only for the brand-new-RecordID case.
func Ingest(ev *AIEvent, terminated bool) (created bool, err error) {
	now := time.Now()
	mu.Lock()
	defer mu.Unlock()

	dayKey, existed := index[ev.RecordID]
	if !existed {
		dayKey = dayKeyFor(now)
	}
	if err := loadDayLocked(dayKey); err != nil {
		return false, err
	}

	stored, ok := days[dayKey][ev.RecordID]
	if !ok {
		// Index said this RecordID lives on dayKey but the day file doesn't
		// have it (shouldn't normally happen outside manual file edits) —
		// treat as not-yet-existing rather than erroring.
		ok = false
	}

	cp := *ev
	if ok {
		cp.ReceivedAt = stored.ReceivedAt
	} else {
		cp.ReceivedAt = now
	}
	cp.UpdatedAt = now
	if terminated {
		cp.Status = StatusTerminated
		cp.TerminatedAt = now
	} else {
		cp.Status = StatusActive
		cp.TerminatedAt = time.Time{}
	}

	days[dayKey][ev.RecordID] = &cp
	index[ev.RecordID] = dayKey

	if err := saveDayLocked(dayKey); err != nil {
		return false, err
	}
	if err := saveIndexLocked(); err != nil {
		return false, err
	}
	return !ok, nil
}

// aiEventWindowGrace mirrors internal/incidents' activeWindowGrace — absorbs
// clock skew (here: between our server and whatever clock an admin reads
// "now" by) with the same couple-of-minutes bound. ReceivedAt is already
// OUR clock (immune to the source system's skew, unlike incidents' Time),
// so this is a smaller concern here, but kept for symmetry/robustness at
// negligible cost.
const aiEventWindowGrace = 2 * time.Minute

// ListActive returns events that are still StatusActive, have a coordinate,
// and were ReceivedAt within the last windowMinutes (<=0 → 10-minute
// default) — the Map page's "🤖 AI Event" layer's data source. A terminated
// event is excluded immediately regardless of how recent it is — see
// Terminate.
func ListActive(windowMinutes int) ([]*AIEvent, error) {
	if windowMinutes <= 0 {
		windowMinutes = 10
	}
	now := time.Now()
	from := now.Add(-time.Duration(windowMinutes)*time.Minute - aiEventWindowGrace)

	mu.Lock() // upgradeable to RLock if loadDayLocked's lazy-load wasn't needed; simplest to just take mu
	defer mu.Unlock()

	dayKeys := map[string]bool{dayKeyFor(now): true, dayKeyFor(from): true}
	out := make([]*AIEvent, 0, 16)
	for dk := range dayKeys {
		if err := loadDayLocked(dk); err != nil {
			continue
		}
		for _, ev := range days[dk] {
			if ev.Status != StatusActive {
				continue
			}
			if ev.Lat == 0 && ev.Lng == 0 {
				continue
			}
			if ev.ReceivedAt.Before(from) || ev.ReceivedAt.After(now.Add(aiEventWindowGrace)) {
				continue
			}
			cp := *ev
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ReceivedAt.After(out[j].ReceivedAt) })
	return out, nil
}

// Get returns a copy of the event with the given RecordID, loading its day
// file via the index if not already in memory.
func Get(recordID string) (*AIEvent, bool) {
	mu.Lock()
	defer mu.Unlock()
	dayKey, ok := index[recordID]
	if !ok {
		return nil, false
	}
	if err := loadDayLocked(dayKey); err != nil {
		return nil, false
	}
	ev, ok := days[dayKey][recordID]
	if !ok {
		return nil, false
	}
	cp := *ev
	return &cp, true
}

// Exists reports whether recordID is already indexed — used by the push
// handler's dry_run mode to report the would-be action ("created" vs.
// "updated"/"terminated") without actually writing anything.
func Exists(recordID string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := index[recordID]
	return ok
}

var sanitizeIDRe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// SanitizeRecordID replaces anything outside [A-Za-z0-9_-] so a RecordID
// (attacker-controlled input from the push payload) can never be used to
// escape its intended directory when building an on-disk image path
// (images.go) — defense against a RecordID like "../../etc/passwd".
func SanitizeRecordID(id string) string {
	id = sanitizeIDRe.ReplaceAllString(id, "_")
	if id == "" {
		id = "unknown"
	}
	return id
}

func randSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// vnLocation anchors day-bucketing to Vietnam time regardless of where
// go2rtc happens to be deployed/run — same convention and same fallback
// (minimal containers without IANA tzdata) as internal/incidents'
// vnLocation.
var vnLocation = loadVNLocation()

func loadVNLocation() *time.Location {
	if loc, err := time.LoadLocation("Asia/Ho_Chi_Minh"); err == nil {
		return loc
	}
	return time.FixedZone("ICT", 7*3600)
}
