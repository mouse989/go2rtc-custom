package traffic

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func resetStorageState(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	configFile = filepath.Join(dir, "traffic.json")

	summaryMu.Lock()
	summaryCache = map[string]scanFileInfo{}
	summaryMu.Unlock()
}

func TestListScanFilesServesFromCacheWithoutRereadingDisk(t *testing.T) {
	resetStorageState(t)

	c := Config{Storage: StorageConfig{Enabled: true}}
	scannedAt := time.Date(2026, 1, 15, 10, 0, 0, 0, vnLocation)
	raw := []Point{{Lat: 1, Lng: 1, JamFactor: 5}, {Lat: 2, Lng: 2, JamFactor: 6}}
	filtered := []Point{{Lat: 1, Lng: 1, JamFactor: 5}}
	if err := saveScanData(c, scannedAt, raw, filtered, nil); err != nil {
		t.Fatalf("saveScanData: %v", err)
	}

	// Corrupt the on-disk file — any code path that still reads it would
	// fail to parse, proving listScanFiles is served from the cache.
	path := filepath.Join(dataDir(), "2026-01-15.json")
	if err := os.WriteFile(path, []byte("not json"), 0644); err != nil {
		t.Fatalf("corrupt file: %v", err)
	}

	files, err := listScanFiles(100)
	if err != nil {
		t.Fatalf("listScanFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d: %+v", len(files), files)
	}
	f := files[0]
	if f.Name != "2026-01-15.json" || f.Scans != 1 || f.Raw != 2 || f.Filtered != 1 {
		t.Fatalf("unexpected cached summary: %+v", f)
	}
}

func TestListScanFilesSeedsCacheOnColdMiss(t *testing.T) {
	resetStorageState(t)

	if err := os.MkdirAll(dataDir(), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A file written by a previous process run — never went through
	// saveScanData in this process, so the cache starts empty for it.
	raw := `{"date":"2026-02-01","scans":[
		{"scannedAt":"t1","raw":[{"lat":1,"lng":1,"jamFactor":5}],"filtered":[],"persistent":[]},
		{"scannedAt":"t2","raw":[{"lat":1,"lng":1,"jamFactor":5},{"lat":2,"lng":2,"jamFactor":6}],"filtered":[{"lat":1,"lng":1,"jamFactor":5}],"persistent":[]}
	]}`
	if err := os.WriteFile(filepath.Join(dataDir(), "2026-02-01.json"), []byte(raw), 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	files, err := listScanFiles(100)
	if err != nil {
		t.Fatalf("listScanFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	f := files[0]
	if f.Scans != 2 || f.Raw != 3 || f.Filtered != 1 {
		t.Fatalf("unexpected parsed summary on cold miss: %+v", f)
	}

	// Second call must now be served from the cache the first call seeded —
	// corrupt the file and confirm the (already-correct) result is unchanged.
	if err := os.WriteFile(filepath.Join(dataDir(), "2026-02-01.json"), []byte("garbage"), 0644); err != nil {
		t.Fatalf("corrupt file: %v", err)
	}
	files2, err := listScanFiles(100)
	if err != nil {
		t.Fatalf("listScanFiles (2nd): %v", err)
	}
	if len(files2) != 1 || files2[0].Scans != 2 || files2[0].Raw != 3 {
		t.Fatalf("expected cold-miss result to have been cached, got %+v", files2)
	}
}

func TestSaveScanDataAccumulatesSameDayCounts(t *testing.T) {
	resetStorageState(t)

	c := Config{Storage: StorageConfig{Enabled: true}}
	scannedAt := time.Date(2026, 3, 1, 8, 0, 0, 0, vnLocation)

	if err := saveScanData(c, scannedAt, []Point{{Lat: 1, Lng: 1, JamFactor: 5}}, nil, nil); err != nil {
		t.Fatalf("saveScanData #1: %v", err)
	}
	scannedAt2 := scannedAt.Add(15 * time.Minute)
	if err := saveScanData(c, scannedAt2, []Point{{Lat: 2, Lng: 2, JamFactor: 6}, {Lat: 3, Lng: 3, JamFactor: 7}}, nil, nil); err != nil {
		t.Fatalf("saveScanData #2: %v", err)
	}

	files, err := listScanFiles(100)
	if err != nil {
		t.Fatalf("listScanFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file (same day), got %d", len(files))
	}
	if files[0].Scans != 2 || files[0].Raw != 3 {
		t.Fatalf("expected 2 scans / 3 raw points accumulated same-day, got %+v", files[0])
	}
}
