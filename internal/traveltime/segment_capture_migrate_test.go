package traveltime

import (
	"database/sql"
	"os"
	"testing"
	"time"
)

// writeLegacySegmentDB creates a single-file travel_route_segments.db with
// the OLD (pre-split) schema and a handful of rows spanning two different
// months — exactly the shape doSegmentCaptureMigration must read from.
func writeLegacySegmentDB(t *testing.T, dir string) {
	t.Helper()
	path := legacySegmentDBPath(dir)
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(segmentCaptureSchema); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	// The real legacy production DB already has these columns (the old
	// code applied this same migration on every startup) — add them here
	// too so this fixture matches what doSegmentCaptureMigration actually
	// reads from in production.
	if err := migrateRouteSegmentsMetricsColumns(db); err != nil {
		t.Fatalf("migrate legacy LOS columns: %v", err)
	}

	insertScan := func(routeID string, scannedAt time.Time) int64 {
		res, err := db.Exec(`INSERT INTO route_segment_scans (route_id, route_name, scanned_at) VALUES (?, ?, ?)`,
			routeID, "Route "+routeID, scannedAt.UTC().Format(time.RFC3339))
		if err != nil {
			t.Fatalf("insert legacy scan: %v", err)
		}
		id, _ := res.LastInsertId()
		if _, err := db.Exec(`INSERT INTO route_segments
			(scan_id, leg_index, from_lat, from_lng, to_lat, to_lng, length_m, duration_sec, base_sec, delay_sec, tti, los_label, los_color)
			VALUES (?, 0, 10, 106, 10.1, 106.1, 1000, 150, 100, 50, 1.5, 'C', '#f00')`, id); err != nil {
			t.Fatalf("insert legacy leg: %v", err)
		}
		return id
	}

	now := time.Now()
	firstOfThisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	prevMonth := firstOfThisMonth.AddDate(0, 0, -1)

	insertScan("r1", now)       // current month
	insertScan("r1", prevMonth) // previous month
	insertScan("r2", now)       // current month, different route
}

func resetSegmentMigrateState(t *testing.T) {
	t.Helper()
	segMigrate.mu.Lock()
	defer segMigrate.mu.Unlock()
	segMigrate.running = false
	segMigrate.done = false
	segMigrate.startedAt = time.Time{}
	segMigrate.finishedAt = time.Time{}
	segMigrate.scansTotal = 0
	segMigrate.scansDone = 0
	segMigrate.monthsCreated = nil
	segMigrate.err = ""
}

func waitForMigrationDone(t *testing.T) SegmentCaptureMigrationStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st := GetSegmentCaptureMigrationStatus()
		if st.Done {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("migration did not finish within 5s")
	return SegmentCaptureMigrationStatus{}
}

func TestSegmentCaptureMigrationMovesLegacyDataIntoMonthFiles(t *testing.T) {
	dir := t.TempDir()
	writeLegacySegmentDB(t, dir)
	if err := initSegmentCaptureDB(dir); err != nil {
		t.Fatalf("initSegmentCaptureDB: %v", err)
	}
	t.Cleanup(func() {
		segmentCaptureMu.Lock()
		for k, db := range segmentDBs {
			db.Close()
			delete(segmentDBs, k)
		}
		segmentCaptureDir = ""
		segmentCaptureMu.Unlock()
	})
	resetSegmentMigrateState(t)

	status := GetSegmentCaptureMigrationStatus()
	if !status.LegacyFileExists {
		t.Fatal("expected LegacyFileExists=true before migrating")
	}

	if err := TriggerSegmentCaptureMigration(); err != nil {
		t.Fatalf("TriggerSegmentCaptureMigration: %v", err)
	}
	final := waitForMigrationDone(t)
	if final.Error != "" {
		t.Fatalf("migration reported an error: %s", final.Error)
	}
	if final.ScansTotal != 3 || final.ScansDone != 3 {
		t.Errorf("ScansTotal/ScansDone = %d/%d, want 3/3", final.ScansTotal, final.ScansDone)
	}
	if len(final.MonthsCreated) != 2 {
		t.Errorf("MonthsCreated = %v, want 2 distinct months", final.MonthsCreated)
	}

	// The legacy file must be renamed aside, never deleted.
	if _, err := os.Stat(legacySegmentDBPath(dir)); !os.IsNotExist(err) {
		t.Errorf("expected the legacy file to no longer exist at its original path, stat err = %v", err)
	}
	if _, err := os.Stat(legacySegmentDBPath(dir) + ".migrated"); err != nil {
		t.Errorf("expected the legacy file preserved as .migrated: %v", err)
	}

	// Current month's data must be queryable through the normal read path.
	all, err := LatestRouteSegments()
	if err != nil {
		t.Fatalf("LatestRouteSegments: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d routes, want 2 (r1 and r2)", len(all))
	}

	// A second trigger must find nothing left to migrate.
	status2 := GetSegmentCaptureMigrationStatus()
	if status2.LegacyFileExists {
		t.Error("expected LegacyFileExists=false after a successful migration")
	}
	if !status2.AlreadyMigratedOnce {
		t.Error("expected AlreadyMigratedOnce=true after a successful migration")
	}
	if err := TriggerSegmentCaptureMigration(); err == nil {
		t.Error("expected re-triggering after a successful migration to report nothing-to-migrate")
	}
}

func TestTriggerSegmentCaptureMigrationErrorsWithNoLegacyFile(t *testing.T) {
	dir := t.TempDir()
	if err := initSegmentCaptureDB(dir); err != nil {
		t.Fatalf("initSegmentCaptureDB: %v", err)
	}
	t.Cleanup(func() {
		segmentCaptureMu.Lock()
		for k, db := range segmentDBs {
			db.Close()
			delete(segmentDBs, k)
		}
		segmentCaptureDir = ""
		segmentCaptureMu.Unlock()
	})
	resetSegmentMigrateState(t)

	if err := TriggerSegmentCaptureMigration(); err == nil {
		t.Error("expected an error when no legacy file exists")
	}
}

func TestTriggerSegmentCaptureMigrationRejectsConcurrentRun(t *testing.T) {
	dir := t.TempDir()
	writeLegacySegmentDB(t, dir)
	if err := initSegmentCaptureDB(dir); err != nil {
		t.Fatalf("initSegmentCaptureDB: %v", err)
	}
	t.Cleanup(func() {
		segmentCaptureMu.Lock()
		for k, db := range segmentDBs {
			db.Close()
			delete(segmentDBs, k)
		}
		segmentCaptureDir = ""
		segmentCaptureMu.Unlock()
	})
	resetSegmentMigrateState(t)

	if err := TriggerSegmentCaptureMigration(); err != nil {
		t.Fatalf("first trigger: %v", err)
	}
	if err := TriggerSegmentCaptureMigration(); err == nil {
		t.Error("expected a second trigger while one is already running to be rejected")
	}
	waitForMigrationDone(t)
}
