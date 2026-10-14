package trafficflow

import "testing"

// setupTestDB opens a fresh SQLite database in a per-test temp directory,
// shared by records_test.go and sensors_test.go. db is a package-level
// *sql.DB, so each test must start from a clean one rather than reusing
// whatever a previous test left behind.
func setupTestDB(t *testing.T) {
	t.Helper()
	if db != nil {
		_ = db.Close()
		db = nil
	}
	dir := t.TempDir()
	if err := initDB(dir); err != nil {
		t.Fatalf("initDB: %v", err)
	}
	t.Cleanup(func() {
		if db != nil {
			_ = db.Close()
			db = nil
		}
	})
}
