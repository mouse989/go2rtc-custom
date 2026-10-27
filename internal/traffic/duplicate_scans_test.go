package traffic

import (
	"testing"
	"time"
)

func TestDetectDuplicateScansFindsSameSlotScans(t *testing.T) {
	resetStorageState(t)
	c := Config{Storage: StorageConfig{Enabled: true}}

	scan1 := time.Date(2026, 10, 27, 10, 1, 0, 0, vnLocation)
	scan2 := time.Date(2026, 10, 27, 10, 9, 0, 0, vnLocation)
	scan3 := time.Date(2026, 10, 27, 10, 16, 0, 0, vnLocation) // different slot, no dup

	if err := saveScanData(c, scan1, nil, nil, nil); err != nil {
		t.Fatalf("saveScanData #1: %v", err)
	}
	if err := saveScanData(c, scan2, nil, nil, nil); err != nil {
		t.Fatalf("saveScanData #2: %v", err)
	}
	if err := saveScanData(c, scan3, nil, nil, nil); err != nil {
		t.Fatalf("saveScanData #3: %v", err)
	}

	report, err := DetectDuplicateScans("2026-10-27", "2026-10-27")
	if err != nil {
		t.Fatalf("DetectDuplicateScans: %v", err)
	}
	if report.DaysScanned != 1 || report.TotalScans != 3 {
		t.Fatalf("expected 1 day / 3 total scans, got %+v", report)
	}
	if len(report.Groups) != 1 {
		t.Fatalf("expected exactly 1 duplicate group, got %d: %+v", len(report.Groups), report.Groups)
	}
	group := report.Groups[0]
	if len(group.Scans) != 2 {
		t.Fatalf("expected 2 scans in the duplicate group, got %+v", group.Scans)
	}
	if report.DuplicateScans != 1 {
		t.Fatalf("expected 1 duplicate scan counted (2 in group - 1 kept), got %d", report.DuplicateScans)
	}

	keptCount := 0
	for _, s := range group.Scans {
		if s.Kept {
			keptCount++
			if s.ScannedAt != scan2.UTC().Format(time.RFC3339) {
				t.Errorf("expected the LATEST scan to be marked kept, got %+v", s)
			}
		}
	}
	if keptCount != 1 {
		t.Fatalf("expected exactly 1 scan marked kept, got %d", keptCount)
	}
}

func TestDetectDuplicateScansNoDuplicatesReturnsEmptyGroups(t *testing.T) {
	resetStorageState(t)
	c := Config{Storage: StorageConfig{Enabled: true}}

	scan1 := time.Date(2026, 10, 27, 10, 0, 0, 0, vnLocation)
	scan2 := time.Date(2026, 10, 27, 10, 15, 0, 0, vnLocation)
	if err := saveScanData(c, scan1, nil, nil, nil); err != nil {
		t.Fatalf("saveScanData #1: %v", err)
	}
	if err := saveScanData(c, scan2, nil, nil, nil); err != nil {
		t.Fatalf("saveScanData #2: %v", err)
	}

	report, err := DetectDuplicateScans("2026-10-27", "2026-10-27")
	if err != nil {
		t.Fatalf("DetectDuplicateScans: %v", err)
	}
	if len(report.Groups) != 0 {
		t.Fatalf("expected no duplicate groups, got %+v", report.Groups)
	}
	if report.TotalScans != 2 || report.DuplicateScans != 0 {
		t.Fatalf("expected 2 total scans / 0 duplicates, got %+v", report)
	}
}

func TestDetectDuplicateScansSpansMultipleDays(t *testing.T) {
	resetStorageState(t)
	c := Config{Storage: StorageConfig{Enabled: true}}

	day1scan1 := time.Date(2026, 10, 25, 10, 1, 0, 0, vnLocation)
	day1scan2 := time.Date(2026, 10, 25, 10, 9, 0, 0, vnLocation)
	day2scan := time.Date(2026, 10, 26, 10, 0, 0, 0, vnLocation)
	for _, s := range []time.Time{day1scan1, day1scan2, day2scan} {
		if err := saveScanData(c, s, nil, nil, nil); err != nil {
			t.Fatalf("saveScanData: %v", err)
		}
	}

	report, err := DetectDuplicateScans("2026-10-25", "2026-10-26")
	if err != nil {
		t.Fatalf("DetectDuplicateScans: %v", err)
	}
	if report.DaysScanned != 2 || report.TotalScans != 3 {
		t.Fatalf("expected 2 days / 3 total scans, got %+v", report)
	}
	if len(report.Groups) != 1 || report.Groups[0].Date != "2026-10-25" {
		t.Fatalf("expected 1 duplicate group on day1 only, got %+v", report.Groups)
	}
}

func TestDetectDuplicateScansInvalidDateReturnsError(t *testing.T) {
	resetStorageState(t)
	if _, err := DetectDuplicateScans("not-a-date", "2026-10-27"); err == nil {
		t.Fatal("expected error for invalid from date")
	}
	if _, err := DetectDuplicateScans("2026-10-27", "not-a-date"); err == nil {
		t.Fatal("expected error for invalid to date")
	}
}

func TestDetectDuplicateScansSwapsReversedBounds(t *testing.T) {
	resetStorageState(t)
	c := Config{Storage: StorageConfig{Enabled: true}}
	scan := time.Date(2026, 10, 27, 10, 0, 0, 0, vnLocation)
	if err := saveScanData(c, scan, nil, nil, nil); err != nil {
		t.Fatalf("saveScanData: %v", err)
	}

	report, err := DetectDuplicateScans("2026-10-27", "2026-10-25") // reversed
	if err != nil {
		t.Fatalf("DetectDuplicateScans: %v", err)
	}
	if report.DaysScanned != 1 {
		t.Fatalf("expected the reversed bounds to be swapped and still find the day, got %+v", report)
	}
}

func TestDetectDuplicateScansNoStoredFilesReturnsEmptyReport(t *testing.T) {
	resetStorageState(t)
	report, err := DetectDuplicateScans("2026-10-25", "2026-10-27")
	if err != nil {
		t.Fatalf("DetectDuplicateScans: %v", err)
	}
	if report.DaysScanned != 0 || report.TotalScans != 0 || len(report.Groups) != 0 {
		t.Fatalf("expected an empty report with no stored files, got %+v", report)
	}
}
