package trafficflow

// records.go — storage for FlowRecord, one Hawkwise push cycle per camera.
// Bucketed by (source_uuid, day) rather than one shared daily file — see
// docs/hawkwise-flow-integration-plan.md §4.2 on why: at ~500 cameras ×
// 288 cycles/day, a single shared file would grow far larger than
// internal/aievent's equivalent (a handful of events/day) ever does.
// Bucketing by source_uuid (Hawkwise's own stable camera key) rather than
// by the internal Sensor.ID means history survives a sensor being remapped,
// renamed, or not yet mapped at all.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// VehicleCount is one entry of details.density_detail.data[] (or a lane's
// own data[]) — label is Hawkwise's own free-text string, never validated
// against a fixed enum, per docs/hawkwise-flow-integration-plan.md §2.4.
type VehicleCount struct {
	Label    string  `json:"label"`
	Volume   int     `json:"volume"`
	AvgSpeed float64 `json:"avg_speed,omitempty"`
}

// LaneFlow is one entry of details.density_detail.partial_data[]. ID is the
// drawing-order number Hawkwise assigned when the camera was configured —
// NOT a standard "numbered from the median" lane convention, and carries no
// turn-direction/movement field (Hawkwise doesn't send one).
type LaneFlow struct {
	ID        int            `json:"id"`
	Occupancy float64        `json:"occupancy"`
	AvgSpeed  *float64       `json:"avg_speed,omitempty"`
	Volume    int            `json:"volume"`
	Data      []VehicleCount `json:"data,omitempty"`
}

// FlowRecord is one Hawkwise Advanced Traffic Density push cycle, normalized
// from the raw wire struct (hawkwisePush in api.go) to the fields this
// system actually stores/displays.
type FlowRecord struct {
	EventID       int64  `json:"event_id"` // dedup key, per Hawkwise doc §6.1
	EventIDString string `json:"event_id_string,omitempty"`
	SourceUUID    string `json:"source_uuid"`

	TriggerCode   string `json:"trigger_code,omitempty"`
	SeverityLevel int    `json:"severity_level"`
	EventState    *int   `json:"event_state,omitempty"` // null | 3 (jam ongoing) | 5 (jam ended)

	// StartTime/EndTime are Hawkwise's own reported window, parsed from its
	// local "yyyy-MM-dd HH:mm:ss" strings via parseHawkwiseTime.
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`

	TrafficStatus *string `json:"traffic_status,omitempty"` // FreeFlow | HeavyTraffic | CongestedFlow | null
	// AvgOccupancy/AvgSpeed units are unconfirmed even in Hawkwise's own
	// document (flagged "CẦN XÁC NHẬN") — stored as-is, see plan §5.2.
	AvgOccupancy float64 `json:"avg_occupancy"`
	AvgSpeed     float64 `json:"avg_speed"`
	Total        int     `json:"total"`

	VehicleCounts []VehicleCount `json:"vehicle_counts,omitempty"`
	Lanes         []LaneFlow     `json:"lanes,omitempty"`

	FrameURL *string `json:"frame_url,omitempty"`

	// ReportedLat/ReportedLng are Hawkwise's own per-push coordinates, kept
	// for reference only (e.g. prefilling a new Sensor mapping) — never used
	// directly for map placement, per the user's explicit instruction. See
	// Sensor.Lat/Lon.
	ReportedLat *float64 `json:"reported_lat,omitempty"`
	ReportedLng *float64 `json:"reported_lng,omitempty"`

	// ReceivedAt is our server clock at first ingestion — immune to clock
	// skew on Hawkwise's side, same convention as internal/aievent.AIEvent.ReceivedAt.
	ReceivedAt time.Time `json:"received_at"`
}

type flowIndexEntry struct {
	SourceUUID string `json:"source_uuid"`
	DayKey     string `json:"day_key"`
}

var (
	recMu sync.Mutex
	// days: "sanitized(source_uuid)|dayKey" -> eventID -> *FlowRecord.
	days = map[string]map[int64]*FlowRecord{}
	// index: eventID -> {source_uuid, day_key}, ALWAYS fully loaded (unlike
	// days) so Exists/dedup works across a server restart without needing
	// every day file loaded into RAM.
	index = map[int64]flowIndexEntry{}
	// latest: source_uuid -> most recent FlowRecord, an in-memory cache
	// rebuilt at startup (primeLatestFromDisk) and kept current by Ingest.
	// This is the Map layer's data source (GET /api/trafficflow/latest) —
	// querying it never touches disk.
	latest = map[string]*FlowRecord{}
)

func indexFile() string { return filepath.Join(dataDir, "flow_index.json") }

var sanitizeUUIDRe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// sanitizeSourceUUID replaces anything outside [A-Za-z0-9_-] so a
// source_uuid (attacker-controlled input from the push payload) can never
// escape its intended directory when building an on-disk file path —
// defense against a source_uuid like "../../etc/passwd", same convention as
// internal/aievent.SanitizeRecordID.
func sanitizeSourceUUID(id string) string {
	id = sanitizeUUIDRe.ReplaceAllString(id, "_")
	if id == "" {
		id = "unknown"
	}
	return id
}

func dayFile(sourceUUID, dayKey string) string {
	return filepath.Join(dataDir, "flow_"+sanitizeSourceUUID(sourceUUID)+"_"+dayKey+".json")
}

func bucketKey(sourceUUID, dayKey string) string { return sourceUUID + "|" + dayKey }

func initRecords(dir string) error {
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

func loadDayLocked(sourceUUID, dayKey string) error {
	bk := bucketKey(sourceUUID, dayKey)
	if _, ok := days[bk]; ok {
		return nil
	}
	m := map[int64]*FlowRecord{}
	data, err := os.ReadFile(dayFile(sourceUUID, dayKey))
	if os.IsNotExist(err) {
		days[bk] = m
		return nil
	}
	if err != nil {
		days[bk] = m
		return err
	}
	var list []*FlowRecord
	if err := json.Unmarshal(data, &list); err != nil {
		days[bk] = m
		return err
	}
	for _, rec := range list {
		m[rec.EventID] = rec
	}
	days[bk] = m
	return nil
}

func saveDayLocked(sourceUUID, dayKey string) error {
	bk := bucketKey(sourceUUID, dayKey)
	m := days[bk]
	list := make([]*FlowRecord, 0, len(m))
	for _, rec := range m {
		list = append(list, rec)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ReceivedAt.After(list[j].ReceivedAt) })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dayFile(sourceUUID, dayKey), data, 0644)
}

// Exists reports whether eventID has already been ingested — used by the
// push handler's dry_run mode and dedup fast path.
func Exists(eventID int64) bool {
	recMu.Lock()
	defer recMu.Unlock()
	_, ok := index[eventID]
	return ok
}

// Ingest stores a new FlowRecord, keyed by rec.EventID. A duplicate
// EventID (Hawkwise retried the same push cycle) is a no-op — per Hawkwise
// doc §6.1, "nhận trùng → không cộng dồn, vẫn trả 2xx" — returns
// created=false without touching storage.
func Ingest(rec *FlowRecord) (created bool, err error) {
	recMu.Lock()
	defer recMu.Unlock()

	if _, dup := index[rec.EventID]; dup {
		return false, nil
	}

	now := time.Now()
	cp := *rec
	cp.ReceivedAt = now
	dayKey := dayKeyFor(now)

	if err := loadDayLocked(rec.SourceUUID, dayKey); err != nil {
		return false, err
	}
	bk := bucketKey(rec.SourceUUID, dayKey)
	days[bk][cp.EventID] = &cp
	index[cp.EventID] = flowIndexEntry{SourceUUID: rec.SourceUUID, DayKey: dayKey}

	if err := saveDayLocked(rec.SourceUUID, dayKey); err != nil {
		return false, err
	}
	if err := saveIndexLocked(); err != nil {
		return false, err
	}

	latestCp := cp
	latest[rec.SourceUUID] = &latestCp
	return true, nil
}

// GetLatestForSensor returns the most recent FlowRecord for sourceUUID, if
// any has ever been ingested (in this process's lifetime, or recovered at
// startup by primeLatestFromDisk).
func GetLatestForSensor(sourceUUID string) (*FlowRecord, bool) {
	recMu.Lock()
	defer recMu.Unlock()
	rec, ok := latest[sourceUUID]
	if !ok {
		return nil, false
	}
	cp := *rec
	return &cp, true
}

// ListLatest returns the most recent FlowRecord for every source_uuid ever
// seen, most recently received first.
func ListLatest() []*FlowRecord {
	recMu.Lock()
	defer recMu.Unlock()
	out := make([]*FlowRecord, 0, len(latest))
	for _, rec := range latest {
		cp := *rec
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ReceivedAt.After(out[j].ReceivedAt) })
	return out
}

// primeLatestFromDisk rebuilds the in-memory `latest` cache after a restart
// by reading today's (then, if empty, yesterday's) day file for every
// source_uuid this server has ever seen — from both mapped Sensors and the
// ObservedSensor registry, so the map layer and the admin's
// "chưa mapping" list both have data immediately rather than waiting for
// the next live push. Best-effort: a read failure for one sensor never
// blocks the others.
func primeLatestFromDisk() {
	seen := map[string]bool{}
	for _, sn := range ListSensors() {
		seen[sn.SourceUUID] = true
	}
	for _, o := range ListObservedSensors() {
		seen[o.SourceUUID] = true
	}
	if len(seen) == 0 {
		return
	}

	now := time.Now()
	todayKey := dayKeyFor(now)
	yesterdayKey := dayKeyFor(now.Add(-24 * time.Hour))

	recMu.Lock()
	defer recMu.Unlock()
	for sourceUUID := range seen {
		var best *FlowRecord
		for _, dk := range []string{todayKey, yesterdayKey} {
			if err := loadDayLocked(sourceUUID, dk); err != nil {
				continue
			}
			for _, rec := range days[bucketKey(sourceUUID, dk)] {
				if best == nil || rec.ReceivedAt.After(best.ReceivedAt) {
					cp := *rec
					best = &cp
				}
			}
		}
		if best != nil {
			latest[sourceUUID] = best
		}
	}
}

// deleteRecordFilesOlderThan removes day files whose date is more than days
// old, scanning dataDir's flow_*.json files directly (there is no
// in-memory list of every source_uuid's file set to iterate instead) —
// same approach as internal/counting's dailyStore.deleteOlderThan. Also
// prunes matching entries from the index and days caches so both stay
// bounded instead of growing forever across a long-running server.
func deleteRecordFilesOlderThan(retentionDays int) int {
	cutoff := time.Now().AddDate(0, 0, -retentionDays).Format("2006-01-02")
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return 0
	}

	recMu.Lock()
	defer recMu.Unlock()
	n := 0
	for _, e := range entries {
		name := e.Name()
		if len(name) < len("flow_YYYY-MM-DD.json") || name[:5] != "flow_" || name[len(name)-5:] != ".json" {
			continue
		}
		dateStr := name[len(name)-15 : len(name)-5] // "YYYY-MM-DD" just before ".json"
		if len(dateStr) != 10 || dateStr >= cutoff {
			continue
		}
		if err := os.Remove(filepath.Join(dataDir, name)); err != nil {
			continue
		}
		n++
		for eventID, entry := range index {
			if entry.DayKey == dateStr {
				delete(index, eventID)
			}
		}
		for bk := range days {
			if strings.HasSuffix(bk, "|"+dateStr) {
				delete(days, bk)
			}
		}
	}
	if n > 0 {
		_ = saveIndexLocked() // best-effort — a failed write here just means the index trims on the next sweep instead
	}
	return n
}
