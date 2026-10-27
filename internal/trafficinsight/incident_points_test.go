package trafficinsight

import (
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/internal/incidents"
)

// createIncidentAt is a local variant of insight_test.go's createIncident
// that also sets a coordinate — the "no coordinate" case is already covered
// by sharing that helper's incidents in the mixed-set test below.
func createIncidentAt(t *testing.T, when time.Time, category string, lat, lng float64) {
	t.Helper()
	setupSources(t)
	in := &incidents.Incident{
		Time:     when,
		Category: category,
		Content:  "test incident with coords",
		Unit:     "TEST UNIT",
		Lat:      lat,
		Lng:      lng,
	}
	if err := incidents.Create(in, "tester"); err != nil {
		t.Fatalf("incidents.Create: %v", err)
	}
}

func TestGetIncidentPointsForDateReturnsOnlyCoordinatedIncidents(t *testing.T) {
	date := "2027-07-10"
	createIncidentAt(t, time.Date(2027, 7, 10, 8, 0, 0, 0, vnLocation), "Va chạm", 10.776, 106.700)
	createIncidentAt(t, time.Date(2027, 7, 10, 9, 0, 0, 0, vnLocation), "Đông xe", 10.8, 106.75)
	createIncident(t, time.Date(2027, 7, 10, 10, 0, 0, 0, vnLocation), "Sự cố khác") // no coordinate — must be excluded

	points, err := GetIncidentPointsForDate(date)
	if err != nil {
		t.Fatalf("GetIncidentPointsForDate: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("expected 2 coordinated incidents, got %d: %+v", len(points), points)
	}
	for _, p := range points {
		if p.Lat == 0 || p.Lng == 0 {
			t.Errorf("expected non-zero coordinate, got %+v", p)
		}
		if p.ID == "" {
			t.Errorf("expected a non-empty ID, got %+v", p)
		}
	}
}

func TestGetIncidentPointsForDateScopesToVNCalendarDay(t *testing.T) {
	date := "2027-07-11"
	createIncidentAt(t, time.Date(2027, 7, 11, 23, 59, 0, 0, vnLocation), "Va chạm", 10.1, 106.1) // last minute of the day — included
	createIncidentAt(t, time.Date(2027, 7, 12, 0, 1, 0, 0, vnLocation), "Va chạm", 10.2, 106.2)   // next day — excluded

	points, err := GetIncidentPointsForDate(date)
	if err != nil {
		t.Fatalf("GetIncidentPointsForDate: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("expected 1 incident within the day, got %d: %+v", len(points), points)
	}
	if points[0].Lat != 10.1 {
		t.Errorf("got the wrong incident: %+v", points[0])
	}
}

func TestGetIncidentPointsForDateEmptyDayReturnsEmptySlice(t *testing.T) {
	setupSources(t)
	points, err := GetIncidentPointsForDate("2027-07-13")
	if err != nil {
		t.Fatalf("GetIncidentPointsForDate: %v", err)
	}
	if len(points) != 0 {
		t.Fatalf("expected no incidents, got %+v", points)
	}
}

func TestGetIncidentPointsForDateInvalidDateReturnsError(t *testing.T) {
	setupSources(t)
	if _, err := GetIncidentPointsForDate("not-a-date"); err == nil {
		t.Error("expected an error for an invalid date, got nil")
	}
}
