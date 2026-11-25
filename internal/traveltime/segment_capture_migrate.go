package traveltime

// segment_capture_migrate.go — one-time, admin-triggered migration of the
// legacy single-file travel_route_segments.db into the per-month layout
// segment_capture_db.go has used since the split (see that file's own doc
// comment for why: a real deployment reached 300MB in one file with
// retention never actually wired up). Never runs automatically — the
// admin picks when a potentially large migration runs rather than having
// it extend every server startup; see TriggerSegmentCaptureMigration.
//
// Runs in the background and is polled via GetSegmentCaptureMigrationStatus
// rather than blocking the triggering HTTP request, since migrating
// hundreds of thousands of rows can take a while. Safe to re-trigger: a
// successful run renames the legacy file aside (never deletes it — the
// admin can verify the new per-month files and remove it manually once
// satisfied), so a second trigger finds no legacy file and reports
// "nothing to migrate" instead of double-inserting.

import (
	"database/sql"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

type segmentMigrateState struct {
	mu            sync.Mutex
	running       bool
	done          bool
	startedAt     time.Time
	finishedAt    time.Time
	scansTotal    int
	scansDone     int
	monthsCreated []string
	err           string
}

var segMigrate segmentMigrateState

// SegmentCaptureMigrationStatus is the admin UI's poll shape.
type SegmentCaptureMigrationStatus struct {
	LegacyFileExists    bool     `json:"legacyFileExists"`
	AlreadyMigratedOnce bool     `json:"alreadyMigratedOnce"` // a <legacy>.migrated file is present from a prior successful run
	Running             bool     `json:"running"`
	Done                bool     `json:"done"`
	StartedAt           string   `json:"startedAt,omitempty"`
	FinishedAt          string   `json:"finishedAt,omitempty"`
	ScansTotal          int      `json:"scansTotal"`
	ScansDone           int      `json:"scansDone"`
	MonthsCreated       []string `json:"monthsCreated,omitempty"`
	Error               string   `json:"error,omitempty"`
}

func legacySegmentDBExists() bool {
	segmentCaptureMu.Lock()
	dir := segmentCaptureDir
	segmentCaptureMu.Unlock()
	_, err := os.Stat(legacySegmentDBPath(dir))
	return err == nil
}

func legacySegmentDBAlreadyMigratedOnce() bool {
	segmentCaptureMu.Lock()
	dir := segmentCaptureDir
	segmentCaptureMu.Unlock()
	_, err := os.Stat(legacySegmentDBPath(dir) + ".migrated")
	return err == nil
}

// GetSegmentCaptureMigrationStatus reports the current/last migration run
// for the admin UI to poll.
func GetSegmentCaptureMigrationStatus() SegmentCaptureMigrationStatus {
	segMigrate.mu.Lock()
	defer segMigrate.mu.Unlock()
	out := SegmentCaptureMigrationStatus{
		LegacyFileExists:    legacySegmentDBExists(),
		AlreadyMigratedOnce: legacySegmentDBAlreadyMigratedOnce(),
		Running:             segMigrate.running,
		Done:                segMigrate.done,
		ScansTotal:          segMigrate.scansTotal,
		ScansDone:           segMigrate.scansDone,
		MonthsCreated:       append([]string(nil), segMigrate.monthsCreated...),
		Error:               segMigrate.err,
	}
	if !segMigrate.startedAt.IsZero() {
		out.StartedAt = segMigrate.startedAt.Format(time.RFC3339)
	}
	if !segMigrate.finishedAt.IsZero() {
		out.FinishedAt = segMigrate.finishedAt.Format(time.RFC3339)
	}
	return out
}

// TriggerSegmentCaptureMigration starts the migration in the background if
// one isn't already running and a legacy file is actually present. Returns
// immediately — poll GetSegmentCaptureMigrationStatus for progress/result.
func TriggerSegmentCaptureMigration() error {
	segMigrate.mu.Lock()
	if segMigrate.running {
		segMigrate.mu.Unlock()
		return fmt.Errorf("đang di chuyển dữ liệu, vui lòng đợi")
	}
	if !legacySegmentDBExists() {
		segMigrate.mu.Unlock()
		return fmt.Errorf("không tìm thấy file travel_route_segments.db cũ — không có gì để di chuyển")
	}
	segMigrate.running = true
	segMigrate.done = false
	segMigrate.startedAt = time.Now()
	segMigrate.finishedAt = time.Time{}
	segMigrate.scansTotal = 0
	segMigrate.scansDone = 0
	segMigrate.monthsCreated = nil
	segMigrate.err = ""
	segMigrate.mu.Unlock()

	go runSegmentCaptureMigration()
	return nil
}

func runSegmentCaptureMigration() {
	err := doSegmentCaptureMigration()
	segMigrate.mu.Lock()
	segMigrate.running = false
	segMigrate.done = true
	segMigrate.finishedAt = time.Now()
	if err != nil {
		segMigrate.err = err.Error()
		log.Error().Err(err).Msg("[traveltime] segment capture migration failed")
	} else {
		log.Info().Strs("months", segMigrate.monthsCreated).Int("scans", segMigrate.scansDone).
			Msg("[traveltime] segment capture migration finished")
	}
	segMigrate.mu.Unlock()
}

// legMigrationRow mirrors route_segments' own columns, read generically
// enough to survive the table having been created by any binary version
// since the LOS columns were added (the legacy file, being from a running
// deployment, is assumed to already have them — migrateRouteSegmentsMetricsColumns
// isn't re-applied to it since it's opened read-only).
type legMigrationRow struct {
	LegIndex                                int
	FromLat, FromLng, ToLat, ToLng          float64
	LengthM, DurationSec, BaseSec, DelaySec int
	TTI                                     float64
	LOSLabel, LOSColor                      string
}

func doSegmentCaptureMigration() error {
	segmentCaptureMu.Lock()
	dir := segmentCaptureDir
	segmentCaptureMu.Unlock()
	path := legacySegmentDBPath(dir)

	src, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&mode=ro")
	if err != nil {
		return fmt.Errorf("mở file cũ: %w", err)
	}
	defer src.Close()

	var total int
	_ = src.QueryRow(`SELECT COUNT(*) FROM route_segment_scans`).Scan(&total)
	segMigrate.mu.Lock()
	segMigrate.scansTotal = total
	segMigrate.mu.Unlock()

	rows, err := src.Query(`SELECT id, route_id, route_name, scanned_at FROM route_segment_scans ORDER BY scanned_at`)
	if err != nil {
		return fmt.Errorf("đọc scan cũ: %w", err)
	}
	defer rows.Close()

	legStmt, err := src.Prepare(`SELECT leg_index, from_lat, from_lng, to_lat, to_lng, length_m, duration_sec,
		base_sec, delay_sec, tti, los_label, los_color FROM route_segments WHERE scan_id = ? ORDER BY leg_index`)
	if err != nil {
		return fmt.Errorf("chuẩn bị truy vấn leg cũ: %w", err)
	}
	defer legStmt.Close()

	monthsSeen := map[string]bool{}
	for rows.Next() {
		var oldID int64
		var routeID, routeName, scannedAtStr string
		if err := rows.Scan(&oldID, &routeID, &routeName, &scannedAtStr); err != nil {
			return fmt.Errorf("đọc dòng scan cũ: %w", err)
		}
		scannedAt, perr := time.Parse(time.RFC3339, scannedAtStr)
		if perr != nil {
			// Malformed legacy timestamp (shouldn't happen — same code
			// always wrote it) — bucket into the CURRENT month rather than
			// silently drop the row.
			scannedAt = time.Now()
		}
		monthKey := segmentMonthKeyFor(scannedAt)
		monthsSeen[monthKey] = true

		legRows, err := legStmt.Query(oldID)
		if err != nil {
			return fmt.Errorf("đọc leg của scan %d: %w", oldID, err)
		}
		var legs []legMigrationRow
		for legRows.Next() {
			var l legMigrationRow
			if err := legRows.Scan(&l.LegIndex, &l.FromLat, &l.FromLng, &l.ToLat, &l.ToLng,
				&l.LengthM, &l.DurationSec, &l.BaseSec, &l.DelaySec, &l.TTI, &l.LOSLabel, &l.LOSColor); err != nil {
				legRows.Close()
				return fmt.Errorf("đọc leg của scan %d: %w", oldID, err)
			}
			legs = append(legs, l)
		}
		legErr := legRows.Err()
		legRows.Close()
		if legErr != nil {
			return fmt.Errorf("đọc leg của scan %d: %w", oldID, legErr)
		}

		if err := insertMigratedScan(monthKey, routeID, routeName, scannedAt, legs); err != nil {
			return fmt.Errorf("ghi scan đã di chuyển (tuyến %s, %s): %w", routeID, scannedAtStr, err)
		}

		segMigrate.mu.Lock()
		segMigrate.scansDone++
		segMigrate.mu.Unlock()
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("đọc scan cũ: %w", err)
	}

	months := make([]string, 0, len(monthsSeen))
	for mk := range monthsSeen {
		months = append(months, mk)
	}
	sort.Strings(months)
	segMigrate.mu.Lock()
	segMigrate.monthsCreated = months
	segMigrate.mu.Unlock()

	// Preserve the original file instead of deleting it — rename aside so
	// a future migration trigger finds no legacy file (preventing a
	// confusing "migrate again" offer and a double-insert), while leaving
	// it on disk as a safety net the admin can verify against and remove
	// manually once satisfied with the per-month files.
	if err := os.Rename(path, path+".migrated"); err != nil {
		return fmt.Errorf("di chuyển xong nhưng không đổi tên được file gốc (%s): %w — dữ liệu đã an toàn ở các file theo tháng, hãy tự đổi tên/xoá file cũ", path, err)
	}
	return nil
}

func insertMigratedScan(monthKey, routeID, routeName string, scannedAt time.Time, legs []legMigrationRow) error {
	db, err := segmentDBForMonth(monthKey)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`INSERT INTO route_segment_scans (route_id, route_name, scanned_at) VALUES (?, ?, ?)`,
		routeID, routeName, scannedAt.UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	newID, err := res.LastInsertId()
	if err != nil {
		return err
	}

	insertLeg, err := tx.Prepare(`INSERT INTO route_segments
		(scan_id, leg_index, from_lat, from_lng, to_lat, to_lng, length_m, duration_sec, base_sec, delay_sec, tti,
		 los_label, los_color)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insertLeg.Close()

	for _, l := range legs {
		if _, err := insertLeg.Exec(newID, l.LegIndex, l.FromLat, l.FromLng, l.ToLat, l.ToLng,
			l.LengthM, l.DurationSec, l.BaseSec, l.DelaySec, l.TTI, l.LOSLabel, l.LOSColor); err != nil {
			return err
		}
	}
	return tx.Commit()
}
