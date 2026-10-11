package incidents

import (
	"net/http/httptest"
	"testing"
	"time"
)

// TestParseListFilterUsesVietnamDayBoundaries is a regression test: the
// "from"/"to" query params are Vietnam calendar dates from the date-picker
// UI, so an incident timestamped early morning on the filtered day (e.g.
// 06:05 ICT) must still fall inside that day's [00:00, 24:00) ICT window.
// Parsing the date strings in UTC instead of vnLocation shifted the window
// 7 hours later and wrongly excluded such incidents.
func TestParseListFilterUsesVietnamDayBoundaries(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/incidents?year=2026&from=2026-10-11&to=2026-10-11", nil)
	f := parseListFilter(r)

	incidentTime := time.Date(2026, 10, 11, 6, 5, 0, 0, vnLocation)

	if f.From.After(incidentTime) {
		t.Fatalf("From bound %v is after a 06:05 ICT incident on the same day %v — would wrongly exclude it", f.From, incidentTime)
	}
	if f.To.Before(incidentTime) {
		t.Fatalf("To bound %v is before a 06:05 ICT incident on the same day %v — would wrongly exclude it", f.To, incidentTime)
	}

	wantFrom := time.Date(2026, 10, 11, 0, 0, 0, 0, vnLocation)
	if !f.From.Equal(wantFrom) {
		t.Fatalf("From = %v, want midnight ICT %v", f.From, wantFrom)
	}
	wantTo := time.Date(2026, 10, 11, 23, 59, 59, 0, vnLocation)
	if !f.To.Equal(wantTo) {
		t.Fatalf("To = %v, want end-of-day ICT %v", f.To, wantTo)
	}
}
