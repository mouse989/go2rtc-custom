package trafficflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMigrateJSONDataImportsLegacyFiles writes the exact on-disk shapes the
// earlier JSON-files version of this package produced, then checks
// migrateJSONData recovers everything into SQLite and renames the legacy
// files out of the way (so a second startup is a no-op, not a re-import).
func TestMigrateJSONDataImportsLegacyFiles(t *testing.T) {
	dir := t.TempDir()
	if db != nil {
		_ = db.Close()
		db = nil
	}
	if err := initDB(dir); err != nil {
		t.Fatalf("initDB: %v", err)
	}
	t.Cleanup(func() {
		if db != nil {
			_ = db.Close()
			db = nil
		}
	})

	createdAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	sensorsJSON, err := json.Marshal([]Sensor{
		{ID: "hws-1", SourceUUID: "cam-1", Label: "Ngã tư A", Lat: 10.1, Lon: 106.1, Enabled: true, CreatedAt: createdAt},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sensors.json"), sensorsJSON, 0644); err != nil {
		t.Fatal(err)
	}

	lat, lng := 10.2, 106.2
	observedJSON, err := json.Marshal([]ObservedSensor{
		{SourceUUID: "cam-2", ReportedLat: &lat, ReportedLng: &lng, Count: 3, FirstSeen: createdAt, LastSeen: createdAt},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "observed_sensors.json"), observedJSON, 0644); err != nil {
		t.Fatal(err)
	}

	receivedAt := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	dayFileJSON, err := json.Marshal([]*FlowRecord{
		{EventID: 101, SourceUUID: "cam-1", Total: 42, ReceivedAt: receivedAt},
		{EventID: 102, SourceUUID: "cam-1", Total: 55, ReceivedAt: receivedAt.Add(5 * time.Minute)},
	})
	if err != nil {
		t.Fatal(err)
	}
	dayFilePath := filepath.Join(dir, "flow_cam-1_2026-09-01.json")
	if err := os.WriteFile(dayFilePath, dayFileJSON, 0644); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(dir, "flow_index.json")
	if err := os.WriteFile(indexPath, []byte(`{"101":{"source_uuid":"cam-1","day_key":"2026-09-01"}}`), 0644); err != nil {
		t.Fatal(err)
	}

	migrateJSONData(dir)

	sensors := ListSensors()
	if len(sensors) != 1 || sensors[0].SourceUUID != "cam-1" || sensors[0].Label != "Ngã tư A" {
		t.Errorf("expected the migrated sensor, got %+v", sensors)
	}

	observed := ListObservedSensors()
	if len(observed) != 1 || observed[0].SourceUUID != "cam-2" || observed[0].Count != 3 {
		t.Errorf("expected the migrated observed sensor, got %+v", observed)
	}

	if !Exists(101) || !Exists(102) {
		t.Error("expected both migrated flow records to exist")
	}
	latest, ok := GetLatestForSensor("cam-1")
	if !ok || latest.Total != 55 {
		t.Errorf("expected the later record (Total=55) to be latest, got %+v (ok=%v)", latest, ok)
	}
	if !latest.ReceivedAt.Equal(receivedAt.Add(5 * time.Minute)) {
		t.Errorf("expected migration to preserve the original ReceivedAt, got %v", latest.ReceivedAt)
	}

	for _, name := range []string{"sensors.json", "observed_sensors.json", "flow_cam-1_2026-09-01.json", "flow_index.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("expected %s to be renamed away after migration", name)
		}
		if _, err := os.Stat(filepath.Join(dir, name+".migrated")); err != nil {
			t.Errorf("expected %s.migrated to exist: %v", name, err)
		}
	}
}

// TestMigrateJSONDataNoopWithoutLegacyFiles confirms a fresh install (no
// legacy files at all) is a harmless no-op, not an error.
func TestMigrateJSONDataNoopWithoutLegacyFiles(t *testing.T) {
	setupTestDB(t)
	dir := t.TempDir()
	migrateJSONData(dir) // must not panic or error on an empty directory
	if len(ListSensors()) != 0 || len(ListObservedSensors()) != 0 {
		t.Error("expected no data to appear from migrating an empty directory")
	}
}
