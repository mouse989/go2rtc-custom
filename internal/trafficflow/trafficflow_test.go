package trafficflow

import "testing"

// flowRetentionDays resolves AppSettings.TrafficFlowRetentionDays (0 →
// default, -1 → no limit, N → N days) — see trafficflow.go's doc comment.
// auth.GetSettings() returns a zero-value AppSettings when nothing has
// been saved yet (as in this test), which exercises exactly the "0 →
// default" branch without needing a real settings file.
func TestFlowRetentionDaysDefaultsWhenUnset(t *testing.T) {
	if got := flowRetentionDays(); got != defaultFlowRetentionDays {
		t.Errorf("expected default %d when unset, got %d", defaultFlowRetentionDays, got)
	}
}
