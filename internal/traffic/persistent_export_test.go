package traffic

import (
	"testing"
	"time"
)

func TestPersistentPointsInRangeFiltersByExactTimestamp(t *testing.T) {
	resetStorageState(t)
	c := Config{Storage: StorageConfig{Enabled: true}}

	inRange := time.Date(2026, 10, 26, 8, 0, 0, 0, vnLocation)
	beforeRange := time.Date(2026, 10, 26, 6, 0, 0, 0, vnLocation)
	afterRange := time.Date(2026, 10, 26, 20, 0, 0, 0, vnLocation)

	persist := []Point{{Lat: 10.858, Lng: 106.71, JamFactor: 8.9}}
	if err := saveScanData(c, beforeRange, nil, nil, persist); err != nil {
		t.Fatalf("saveScanData beforeRange: %v", err)
	}
	if err := saveScanData(c, inRange, nil, nil, persist); err != nil {
		t.Fatalf("saveScanData inRange: %v", err)
	}
	if err := saveScanData(c, afterRange, nil, nil, persist); err != nil {
		t.Fatalf("saveScanData afterRange: %v", err)
	}

	from := time.Date(2026, 10, 26, 7, 0, 0, 0, vnLocation)
	to := time.Date(2026, 10, 26, 9, 0, 0, 0, vnLocation)
	rows, err := PersistentPointsInRange(from, to)
	if err != nil {
		t.Fatalf("PersistentPointsInRange: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 row (only inRange scan), got %d: %+v", len(rows), rows)
	}
	if rows[0].Timestamp != "10/26/2026 08:00:00" {
		t.Errorf("expected timestamp %q, got %q", "10/26/2026 08:00:00", rows[0].Timestamp)
	}
	if rows[0].Lat != 10.858 || rows[0].Lng != 106.71 || rows[0].JamFactor != 8.9 {
		t.Errorf("unexpected row values: %+v", rows[0])
	}
}

func TestPersistentPointsInRangeIsInclusiveAtBothBounds(t *testing.T) {
	resetStorageState(t)
	c := Config{Storage: StorageConfig{Enabled: true}}

	from := time.Date(2026, 10, 26, 7, 0, 0, 0, vnLocation)
	to := time.Date(2026, 10, 26, 9, 0, 0, 0, vnLocation)
	persist := []Point{{Lat: 1, Lng: 1, JamFactor: 5}}

	if err := saveScanData(c, from, nil, nil, persist); err != nil {
		t.Fatalf("saveScanData at from: %v", err)
	}
	if err := saveScanData(c, to, nil, nil, persist); err != nil {
		t.Fatalf("saveScanData at to: %v", err)
	}

	rows, err := PersistentPointsInRange(from, to)
	if err != nil {
		t.Fatalf("PersistentPointsInRange: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected both boundary scans included, got %d: %+v", len(rows), rows)
	}
}

func TestPersistentPointsInRangeSpansMultipleDayFiles(t *testing.T) {
	resetStorageState(t)
	c := Config{Storage: StorageConfig{Enabled: true}}

	day1 := time.Date(2026, 10, 24, 10, 0, 0, 0, vnLocation)
	day2 := time.Date(2026, 10, 25, 10, 0, 0, 0, vnLocation)
	day3 := time.Date(2026, 10, 26, 10, 0, 0, 0, vnLocation)

	persist1 := []Point{{Lat: 1, Lng: 1, JamFactor: 5}}
	persist2 := []Point{{Lat: 2, Lng: 2, JamFactor: 6}, {Lat: 2.5, Lng: 2.5, JamFactor: 6.5}}
	persist3 := []Point{{Lat: 3, Lng: 3, JamFactor: 7}}

	for _, s := range []struct {
		t time.Time
		p []Point
	}{{day1, persist1}, {day2, persist2}, {day3, persist3}} {
		if err := saveScanData(c, s.t, nil, nil, s.p); err != nil {
			t.Fatalf("saveScanData: %v", err)
		}
	}

	// Range covers day1 and day2 only.
	from := time.Date(2026, 10, 24, 0, 0, 0, 0, vnLocation)
	to := time.Date(2026, 10, 25, 23, 59, 59, 0, vnLocation)
	rows, err := PersistentPointsInRange(from, to)
	if err != nil {
		t.Fatalf("PersistentPointsInRange: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows (1 from day1 + 2 from day2), got %d: %+v", len(rows), rows)
	}
	// Chronological: day1's point first, then day2's two points.
	if rows[0].Lat != 1 || rows[1].Lat != 2 || rows[2].Lat != 2.5 {
		t.Fatalf("expected chronological order, got %+v", rows)
	}
	// day3's point must be excluded.
	for _, r := range rows {
		if r.Lat == 3 {
			t.Fatalf("day3's point must not be included in a range that excludes day3: %+v", rows)
		}
	}
}

func TestPersistentPointsInRangeEmptyWhenNoDataDir(t *testing.T) {
	resetStorageState(t)
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, vnLocation)
	to := time.Date(2026, 12, 31, 0, 0, 0, 0, vnLocation)
	rows, err := PersistentPointsInRange(from, to)
	if err != nil {
		t.Fatalf("PersistentPointsInRange: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected empty result, got %+v", rows)
	}
}

func TestPersistentPointsInRangeExcludesRawAndFilteredOnly(t *testing.T) {
	resetStorageState(t)
	c := Config{Storage: StorageConfig{Enabled: true}}
	scannedAt := time.Date(2026, 10, 26, 10, 0, 0, 0, vnLocation)

	raw := []Point{{Lat: 9, Lng: 9, JamFactor: 1}}
	filtered := []Point{{Lat: 8, Lng: 8, JamFactor: 2}}
	persistent := []Point{{Lat: 7, Lng: 7, JamFactor: 3}}
	if err := saveScanData(c, scannedAt, raw, filtered, persistent); err != nil {
		t.Fatalf("saveScanData: %v", err)
	}

	rows, err := PersistentPointsInRange(scannedAt.Add(-time.Hour), scannedAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("PersistentPointsInRange: %v", err)
	}
	if len(rows) != 1 || rows[0].Lat != 7 {
		t.Fatalf("expected only the persistent point, got %+v", rows)
	}
}

func TestPersistentPointsInRangeDedupsScansInSameSlot(t *testing.T) {
	resetStorageState(t)
	c := Config{Storage: StorageConfig{Enabled: true}}

	// Simulates a service restart: 2 scans a few minutes apart, both
	// landing in the same (default 15-min) slot.
	scan1 := time.Date(2026, 10, 27, 10, 1, 0, 0, vnLocation)
	scan2 := time.Date(2026, 10, 27, 10, 9, 0, 0, vnLocation)
	if err := saveScanData(c, scan1, nil, nil, []Point{{Lat: 1, Lng: 1, JamFactor: 5}}); err != nil {
		t.Fatalf("saveScanData #1: %v", err)
	}
	if err := saveScanData(c, scan2, nil, nil, []Point{{Lat: 2, Lng: 2, JamFactor: 6}}); err != nil {
		t.Fatalf("saveScanData #2: %v", err)
	}

	rows, err := PersistentPointsInRange(scan1.Add(-time.Hour), scan2.Add(time.Hour))
	if err != nil {
		t.Fatalf("PersistentPointsInRange: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row (both scans deduped to the same slot), got %d: %+v", len(rows), rows)
	}
	if rows[0].Lat != 2 {
		t.Fatalf("expected the LATER scan's point to be kept, got %+v", rows[0])
	}
}

func TestPersistentPointsForDateDedupsScansInSameSlot(t *testing.T) {
	resetStorageState(t)
	c := Config{Storage: StorageConfig{Enabled: true}}

	scan1 := time.Date(2026, 10, 27, 10, 1, 0, 0, vnLocation)
	scan2 := time.Date(2026, 10, 27, 10, 9, 0, 0, vnLocation)
	if err := saveScanData(c, scan1, nil, nil, []Point{{Lat: 1, Lng: 1, JamFactor: 5}}); err != nil {
		t.Fatalf("saveScanData #1: %v", err)
	}
	if err := saveScanData(c, scan2, nil, nil, []Point{{Lat: 2, Lng: 2, JamFactor: 6}}); err != nil {
		t.Fatalf("saveScanData #2: %v", err)
	}

	pts, err := PersistentPointsForDate("2026-10-27")
	if err != nil {
		t.Fatalf("PersistentPointsForDate: %v", err)
	}
	if len(pts) != 1 || pts[0].Lat != 2 {
		t.Fatalf("expected 1 deduped point (the later scan), got %+v", pts)
	}
}
