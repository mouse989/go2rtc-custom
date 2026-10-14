package trafficflow

// migrate.go — one-time best-effort import of this package's earlier
// flat-JSON-files storage (sensors.json, observed_sensors.json,
// flow_index.json, flow_<source_uuid>_<day>.json) into the SQLite database
// introduced in db.go (see db.go's doc comment for why that move
// happened). Safe to call on every Init(): each legacy file is renamed
// with a ".migrated" suffix immediately after a successful import, so a
// second startup finds nothing left to do. A fresh install (no legacy
// files at all — the common case for anyone not already running the
// earlier JSON-based version) is a fast no-op.

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// migrateJSONData imports any legacy JSON files found in dir into the
// already-initialized SQLite database. Each step is independent and
// best-effort — a failure importing one file type is logged and does not
// block the others or prevent the server from starting.
func migrateJSONData(dir string) {
	migrateSensorsJSON(dir)
	migrateObservedSensorsJSON(dir)
	migrateFlowDayFiles(dir)
}

func migrateSensorsJSON(dir string) {
	path := filepath.Join(dir, "sensors.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		log.Warn().Err(err).Msg("[trafficflow] migrate: cannot read sensors.json")
		return
	}
	var list []Sensor
	if err := json.Unmarshal(data, &list); err != nil {
		log.Warn().Err(err).Msg("[trafficflow] migrate: cannot parse sensors.json")
		return
	}
	n := 0
	for _, sn := range list {
		_, err := db.Exec(`
			INSERT OR IGNORE INTO sensors (id, source_uuid, hardware_uuid, label, lat, lon, enabled, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, sn.ID, sn.SourceUUID, sn.HardwareUUID, sn.Label, sn.Lat, sn.Lon, boolInt(sn.Enabled), sn.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"))
		if err != nil {
			log.Warn().Err(err).Str("source_uuid", sn.SourceUUID).Msg("[trafficflow] migrate: cannot import sensor")
			continue
		}
		n++
	}
	if err := os.Rename(path, path+".migrated"); err != nil {
		log.Warn().Err(err).Msg("[trafficflow] migrate: imported sensors.json but could not rename it")
	}
	log.Info().Int("count", n).Msg("[trafficflow] migrate: imported sensors.json into SQLite")
}

func migrateObservedSensorsJSON(dir string) {
	path := filepath.Join(dir, "observed_sensors.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		log.Warn().Err(err).Msg("[trafficflow] migrate: cannot read observed_sensors.json")
		return
	}
	var list []ObservedSensor
	if err := json.Unmarshal(data, &list); err != nil {
		log.Warn().Err(err).Msg("[trafficflow] migrate: cannot parse observed_sensors.json")
		return
	}
	n := 0
	for _, o := range list {
		_, err := db.Exec(`
			INSERT OR IGNORE INTO observed_sensors (source_uuid, hardware_uuid, reported_lat, reported_lng, count, first_seen, last_seen)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, o.SourceUUID, o.HardwareUUID, nullFloat(o.ReportedLat), nullFloat(o.ReportedLng), o.Count,
			o.FirstSeen.Format("2006-01-02T15:04:05.999999999Z07:00"), o.LastSeen.Format("2006-01-02T15:04:05.999999999Z07:00"))
		if err != nil {
			log.Warn().Err(err).Str("source_uuid", o.SourceUUID).Msg("[trafficflow] migrate: cannot import observed sensor")
			continue
		}
		n++
	}
	if err := os.Rename(path, path+".migrated"); err != nil {
		log.Warn().Err(err).Msg("[trafficflow] migrate: imported observed_sensors.json but could not rename it")
	}
	log.Info().Int("count", n).Msg("[trafficflow] migrate: imported observed_sensors.json into SQLite")
}

// migrateFlowDayFiles imports every flow_<source_uuid>_<day>.json file —
// each one already a self-contained list of FlowRecord, so no need to
// consult flow_index.json at all; insertFlowRecord preserves each record's
// original ReceivedAt rather than stamping "now" (see its doc comment).
// flow_index.json itself is renamed alongside the day files once they're
// all processed — it's superseded by the table's own PRIMARY KEY, nothing
// reads it anymore.
func migrateFlowDayFiles(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	totalRecords, totalFiles := 0, 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == "flow_index.json" || !isFlowDayFile(name) {
			continue
		}
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			log.Warn().Err(err).Str("file", name).Msg("[trafficflow] migrate: cannot read day file")
			continue
		}
		var list []*FlowRecord
		if err := json.Unmarshal(data, &list); err != nil {
			log.Warn().Err(err).Str("file", name).Msg("[trafficflow] migrate: cannot parse day file")
			continue
		}
		for _, rec := range list {
			if _, err := insertFlowRecord(rec); err != nil {
				log.Warn().Err(err).Int64("event_id", rec.EventID).Msg("[trafficflow] migrate: cannot import flow record")
				continue
			}
			totalRecords++
		}
		if err := os.Rename(path, path+".migrated"); err != nil {
			log.Warn().Err(err).Str("file", name).Msg("[trafficflow] migrate: imported day file but could not rename it")
		}
		totalFiles++
	}

	if totalFiles > 0 {
		log.Info().Int("files", totalFiles).Int("records", totalRecords).Msg("[trafficflow] migrate: imported flow day files into SQLite")
	}

	// flow_index.json has no further use once the day files it referenced
	// are imported (or if there were none left to import) — rename it out
	// of the way so a future directory listing isn't confused by it.
	indexPath := filepath.Join(dir, "flow_index.json")
	if _, err := os.Stat(indexPath); err == nil {
		if err := os.Rename(indexPath, indexPath+".migrated"); err != nil {
			log.Warn().Err(err).Msg("[trafficflow] migrate: could not rename flow_index.json")
		}
	}
}

// isFlowDayFile matches the legacy "flow_<anything>_<YYYY-MM-DD>.json"
// pattern (sanitizeSourceUUID could have produced any [A-Za-z0-9_-]
// content for the middle part, so this only anchors the fixed prefix/
// suffix shape rather than the exact legacy sanitizer).
func isFlowDayFile(name string) bool {
	return len(name) >= len("flow_x_2006-01-02.json") &&
		name[:5] == "flow_" &&
		name[len(name)-5:] == ".json"
}
