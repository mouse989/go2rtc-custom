package traffic

import (
	"testing"
	"time"
)

func TestInitialScanDelayScansImmediatelyWhenNoStoredScanToday(t *testing.T) {
	resetStorageState(t)
	now := time.Date(2026, 6, 1, 10, 5, 0, 0, vnLocation)

	if got := initialScanDelay(now, 15); got != 0 {
		t.Fatalf("expected 0 (scan immediately) with no stored scans, got %v", got)
	}
}

func TestInitialScanDelayWaitsRemainderWhenLastScanWasRecent(t *testing.T) {
	resetStorageState(t)
	c := Config{Storage: StorageConfig{Enabled: true}}

	last := time.Date(2026, 6, 1, 10, 0, 0, 0, vnLocation)
	if err := saveScanData(c, last, nil, nil, nil); err != nil {
		t.Fatalf("saveScanData: %v", err)
	}

	// Restart happens 4 minutes after the last stored scan, interval = 15min
	// → should wait the remaining 11 minutes, not scan immediately.
	now := last.Add(4 * time.Minute)
	got := initialScanDelay(now, 15)
	want := 11 * time.Minute
	if got != want {
		t.Fatalf("expected wait of %v, got %v", want, got)
	}
}

func TestInitialScanDelayScansImmediatelyWhenLastScanIsOlderThanInterval(t *testing.T) {
	resetStorageState(t)
	c := Config{Storage: StorageConfig{Enabled: true}}

	last := time.Date(2026, 6, 1, 10, 0, 0, 0, vnLocation)
	if err := saveScanData(c, last, nil, nil, nil); err != nil {
		t.Fatalf("saveScanData: %v", err)
	}

	// Restart happens 20 minutes after the last stored scan, interval = 15min
	// → already past due for the next scan, so scan immediately.
	now := last.Add(20 * time.Minute)
	if got := initialScanDelay(now, 15); got != 0 {
		t.Fatalf("expected 0 (scan immediately), got %v", got)
	}
}

func TestInitialScanDelayDefaultsIntervalWhenUnset(t *testing.T) {
	resetStorageState(t)
	c := Config{Storage: StorageConfig{Enabled: true}}

	last := time.Date(2026, 6, 1, 10, 0, 0, 0, vnLocation)
	if err := saveScanData(c, last, nil, nil, nil); err != nil {
		t.Fatalf("saveScanData: %v", err)
	}

	now := last.Add(5 * time.Minute)
	got := initialScanDelay(now, 0) // intervalMin <= 0 → defaults to 15
	want := 10 * time.Minute
	if got != want {
		t.Fatalf("expected default-interval wait of %v, got %v", want, got)
	}
}
