package trafficinsight

import (
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/internal/incidents"
)

func createIncidentFull(t *testing.T, when time.Time, category string, severity int, congestionRisk bool) {
	t.Helper()
	setupSources(t)
	in := &incidents.Incident{
		Time:                when,
		Category:            category,
		Content:             "test incident",
		Unit:                "TEST UNIT",
		Severity:            severity,
		CongestionRiskPoint: congestionRisk,
	}
	if err := incidents.Create(in, "tester"); err != nil {
		t.Fatalf("incidents.Create: %v", err)
	}
}

func TestGetIncidentsForRangeFiltersByDateBounds(t *testing.T) {
	createIncidentFull(t, time.Date(2027, 8, 1, 8, 0, 0, 0, vnLocation), "Va chạm", 2, false)
	createIncidentFull(t, time.Date(2027, 8, 3, 10, 0, 0, 0, vnLocation), "Đông xe", 0, true)
	createIncidentFull(t, time.Date(2027, 8, 10, 10, 0, 0, 0, vnLocation), "Đông xe", 0, false) // outside range — excluded

	got, err := GetIncidentsForRange("2027-08-01", "2027-08-05")
	if err != nil {
		t.Fatalf("GetIncidentsForRange: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 incidents in range, got %d: %+v", len(got), got)
	}
	// Sorted oldest first.
	if got[0].Category != "Va chạm" || got[1].Category != "Đông xe" {
		t.Errorf("unexpected order: %+v", got)
	}
	if !got[1].CongestionRiskPoint {
		t.Errorf("expected second incident to carry CongestionRiskPoint=true, got %+v", got[1])
	}
}

func TestGetIncidentsForRangeInclusiveBothEnds(t *testing.T) {
	date := "2027-08-20"
	createIncidentFull(t, time.Date(2027, 8, 20, 0, 0, 1, 0, vnLocation), "Ngập nước", 1, false)    // first second of the day
	createIncidentFull(t, time.Date(2027, 8, 20, 23, 59, 59, 0, vnLocation), "Ngập nước", 1, false) // last second of the day

	got, err := GetIncidentsForRange(date, date)
	if err != nil {
		t.Fatalf("GetIncidentsForRange: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 incidents on the single day (inclusive both ends), got %d: %+v", len(got), got)
	}
}

func TestGetIncidentsForRangeSpansYearBoundary(t *testing.T) {
	createIncidentFull(t, time.Date(2027, 12, 30, 10, 0, 0, 0, vnLocation), "Va chạm", 0, false)
	createIncidentFull(t, time.Date(2028, 1, 2, 10, 0, 0, 0, vnLocation), "Va chạm", 0, false)

	got, err := GetIncidentsForRange("2027-12-29", "2028-01-03")
	if err != nil {
		t.Fatalf("GetIncidentsForRange: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 incidents spanning the year boundary, got %d: %+v", len(got), got)
	}
}

func TestGetIncidentsForRangeInvalidDateReturnsError(t *testing.T) {
	setupSources(t)
	if _, err := GetIncidentsForRange("not-a-date", "2027-08-01"); err == nil {
		t.Error("expected an error for an invalid 'from' date, got nil")
	}
	if _, err := GetIncidentsForRange("2027-08-01", "not-a-date"); err == nil {
		t.Error("expected an error for an invalid 'to' date, got nil")
	}
}

func TestGetIncidentsForRangeEmptyRangeReturnsEmptySlice(t *testing.T) {
	setupSources(t)
	got, err := GetIncidentsForRange("2027-08-25", "2027-08-26")
	if err != nil {
		t.Fatalf("GetIncidentsForRange: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no incidents, got %+v", got)
	}
}
