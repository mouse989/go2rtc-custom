package trafficinsight

import (
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/internal/traveltime"
)

func tsVN(y, mo, d, h, mi int) string {
	return time.Date(y, time.Month(mo), d, h, mi, 0, 0, vnLocation).UTC().Format(time.RFC3339)
}

func TestGetRouteTTIForDateAggregatesIntoSlots(t *testing.T) {
	setupSources(t)
	date := "2027-06-10"

	writeTravelTimeDay(t, date, []traveltime.LogEntry{
		{Timestamp: tsVN(2027, 6, 10, 8, 2), RouteID: "r1", RouteName: "Tuyến 1", LengthM: 5000, TTI: 1.4, DelaySec: 180},
		{Timestamp: tsVN(2027, 6, 10, 8, 9), RouteID: "r1", RouteName: "Tuyến 1", LengthM: 5000, TTI: 1.6, DelaySec: 240},
		{Timestamp: tsVN(2027, 6, 10, 9, 0), RouteID: "r1", RouteName: "Tuyến 1", LengthM: 5000, TTI: 2.0, DelaySec: 400},
	})

	rows, err := GetRouteTTIForDate(date)
	if err != nil {
		t.Fatalf("GetRouteTTIForDate: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 route, got %d", len(rows))
	}
	r := rows[0]
	if r.RouteID != "r1" || r.RouteName != "Tuyến 1" || r.LengthM != 5000 {
		t.Errorf("unexpected route identity: %+v", r)
	}
	if len(r.Slots) != routeTotalSlots {
		t.Fatalf("expected %d slots, got %d", routeTotalSlots, len(r.Slots))
	}
	// 08:02 and 08:09 fall in the same 15-minute slot (08:00-08:15) and
	// should be averaged together.
	slot8 := 8 * 60 / routeSlotMinutes
	if r.Slots[slot8] == nil {
		t.Fatalf("expected slot %d to have data", slot8)
	}
	wantTTI := (1.4 + 1.6) / 2.0
	if diff := r.Slots[slot8].TTI - wantTTI; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("slot %d TTI = %v, want %v", slot8, r.Slots[slot8].TTI, wantTTI)
	}
	if r.Slots[slot8].Samples != 2 {
		t.Errorf("slot %d Samples = %d, want 2", slot8, r.Slots[slot8].Samples)
	}

	slot9 := 9 * 60 / routeSlotMinutes
	if r.Slots[slot9] == nil || r.Slots[slot9].Samples != 1 {
		t.Fatalf("expected slot %d to have exactly 1 sample, got %+v", slot9, r.Slots[slot9])
	}

	// LatestTTI picks the chronologically-last entry (09:00 → TTI 2.0), not
	// the largest value.
	if r.LatestTTI != 2.0 {
		t.Errorf("LatestTTI = %v, want 2.0 (the chronologically-last entry)", r.LatestTTI)
	}
}

func TestGetRouteTTIForDateIgnoresZeroTTIAndMissingRouteID(t *testing.T) {
	setupSources(t)
	date := "2027-06-11"

	writeTravelTimeDay(t, date, []traveltime.LogEntry{
		{Timestamp: tsVN(2027, 6, 11, 8, 0), RouteID: "", RouteName: "no id", TTI: 1.5},
		{Timestamp: tsVN(2027, 6, 11, 8, 0), RouteID: "r2", RouteName: "zero tti", TTI: 0},
		{Timestamp: tsVN(2027, 6, 11, 8, 0), RouteID: "r3", RouteName: "valid", TTI: 1.2},
	})

	rows, err := GetRouteTTIForDate(date)
	if err != nil {
		t.Fatalf("GetRouteTTIForDate: %v", err)
	}
	if len(rows) != 1 || rows[0].RouteID != "r3" {
		t.Fatalf("expected only route r3 to survive, got %+v", rows)
	}
}

func TestGetRouteTTIForDateSortsByLatestTTIDescAndCapsAtTen(t *testing.T) {
	setupSources(t)
	date := "2027-06-12"

	var entries []traveltime.LogEntry
	for i := 0; i < 12; i++ {
		entries = append(entries, traveltime.LogEntry{
			Timestamp: tsVN(2027, 6, 12, 8, 0),
			RouteID:   string(rune('a' + i)),
			RouteName: "route",
			TTI:       float64(i) + 1, // 1..12, route "l" (last) has the highest TTI
		})
	}
	writeTravelTimeDay(t, date, entries)

	rows, err := GetRouteTTIForDate(date)
	if err != nil {
		t.Fatalf("GetRouteTTIForDate: %v", err)
	}
	if len(rows) != 10 {
		t.Fatalf("expected capped at 10 routes, got %d", len(rows))
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].LatestTTI > rows[i-1].LatestTTI {
			t.Fatalf("rows not sorted descending by LatestTTI at index %d: %+v", i, rows)
		}
	}
	// Highest TTI values (12, 11, ...) must be the ones kept, not the first 10 inserted.
	if rows[0].LatestTTI != 12 {
		t.Errorf("rows[0].LatestTTI = %v, want 12 (the single highest value)", rows[0].LatestTTI)
	}
}

func TestGetRouteTTIForDateEmptyDayReturnsEmptySlice(t *testing.T) {
	setupSources(t)
	rows, err := GetRouteTTIForDate("2027-06-13")
	if err != nil {
		t.Fatalf("GetRouteTTIForDate: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected no routes, got %+v", rows)
	}
}

func TestSlotOfTimestampHandlesInvalidInput(t *testing.T) {
	if got := slotOfTimestamp("not-a-timestamp"); got != -1 {
		t.Errorf("slotOfTimestamp(invalid) = %d, want -1", got)
	}
}
