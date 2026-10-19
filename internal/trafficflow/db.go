package trafficflow

// db.go — SQLite storage for this package, replacing the earlier
// day-bucketed-JSON-files design. Motivation (see
// docs/data-backup-plan.md §8): the original design kept a single global
// dedup index (flow_index.json) that was rewritten IN FULL on every single
// incoming push — fine at first, but with ~500 cameras pushing every
// ~5 minutes and the user's decision to retain this data indefinitely
// (no more 30-day cutoff), that index would have grown to tens of millions
// of entries within a year, each push getting slower as the whole file had
// to be re-marshaled and rewritten. A single embedded SQLite file (still
// just one file on disk — no separate DB server, no change to the
// "no extra infra" shape of this app) replaces that with an indexed
// INSERT: dedup is now a UNIQUE constraint on event_id, O(log n) per
// write, never a full-file rewrite.
//
// Driver: modernc.org/sqlite — pure Go (no cgo), so the build/deploy story
// stays exactly as simple as it was with plain files.

import (
	"database/sql"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite"
)

var db *sql.DB

const dbSchema = `
CREATE TABLE IF NOT EXISTS sensors (
	id            TEXT PRIMARY KEY,
	source_uuid   TEXT NOT NULL UNIQUE,
	hardware_uuid TEXT NOT NULL DEFAULT '',
	label         TEXT NOT NULL,
	lat           REAL NOT NULL,
	lon           REAL NOT NULL,
	enabled       INTEGER NOT NULL,
	created_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS observed_sensors (
	source_uuid   TEXT PRIMARY KEY,
	hardware_uuid TEXT NOT NULL DEFAULT '',
	reported_lat  REAL,
	reported_lng  REAL,
	count         INTEGER NOT NULL,
	first_seen    TEXT NOT NULL,
	last_seen     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS flow_records (
	event_id        INTEGER PRIMARY KEY,
	event_id_string TEXT NOT NULL DEFAULT '',
	source_uuid     TEXT NOT NULL,
	trigger_code    TEXT NOT NULL DEFAULT '',
	severity_level  INTEGER NOT NULL,
	event_state     INTEGER,
	start_time      TEXT NOT NULL,
	end_time        TEXT NOT NULL,
	traffic_status  TEXT,
	avg_occupancy   REAL NOT NULL,
	avg_speed       REAL NOT NULL,
	total           INTEGER NOT NULL,
	vehicle_counts  TEXT,
	lanes           TEXT,
	frame_url       TEXT,
	reported_lat    REAL,
	reported_lng    REAL,
	received_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_flow_records_source_received ON flow_records(source_uuid, received_at DESC);
CREATE INDEX IF NOT EXISTS idx_flow_records_received ON flow_records(received_at);

CREATE TABLE IF NOT EXISTS push_log (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	received_at     TEXT NOT NULL,
	remote_ip       TEXT NOT NULL DEFAULT '',
	auth_result     TEXT NOT NULL,
	integration     TEXT NOT NULL DEFAULT '',
	http_status     INTEGER NOT NULL,
	body_valid_json INTEGER,
	parse_error     TEXT NOT NULL DEFAULT '',
	body            TEXT NOT NULL DEFAULT '',
	body_truncated  INTEGER NOT NULL DEFAULT 0
);
`

// initDB opens (creating if absent) trafficflow_data/trafficflow.db and
// applies the schema above (idempotent — CREATE TABLE/INDEX IF NOT EXISTS).
// WAL mode lets the push handler's writes and the map layer's reads proceed
// without blocking each other, which the single-mutex-plus-whole-file-
// rewrite design never offered.
func initDB(dir string) error {
	path := filepath.Join(dir, "trafficflow.db")
	var err error
	db, err = sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		return fmt.Errorf("enable foreign_keys: %w", err)
	}
	if _, err := db.Exec(dbSchema); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}
