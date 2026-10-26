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

	rows, err := GetRouteTTIForDate(date, -1)
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

	// AvgTTI (the ranking key, hour=-1 → whole day) must be the mean of
	// ALL three entries, not just the last one or one slot.
	wantAvg := (1.4 + 1.6 + 2.0) / 3.0
	if diff := r.AvgTTI - wantAvg; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("AvgTTI = %v, want %v (mean of all 3 entries across the day)", r.AvgTTI, wantAvg)
	}
	if r.Samples != 3 {
		t.Errorf("Samples = %d, want 3", r.Samples)
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

	rows, err := GetRouteTTIForDate(date, -1)
	if err != nil {
		t.Fatalf("GetRouteTTIForDate: %v", err)
	}
	if len(rows) != 1 || rows[0].RouteID != "r3" {
		t.Fatalf("expected only route r3 to survive, got %+v", rows)
	}
}

func TestGetRouteTTIForDateSortsByAvgTTINotLatest(t *testing.T) {
	setupSources(t)
	date := "2027-06-12"

	// Route "a": consistently congested all day, but its LAST reading
	// happens to be a brief lull (high average, low latest). Route "b":
	// mostly clear all day, but its LAST reading happens to be a brief
	// spike (low average, high latest). Ranking by AvgTTI must put "a"
	// first even though "b" has the higher LatestTTI — the old
	// LatestTTI-based ranking would have put "b" first instead.
	writeTravelTimeDay(t, date, []traveltime.LogEntry{
		{Timestamp: tsVN(2027, 6, 12, 8, 0), RouteID: "a", RouteName: "mostly congested", TTI: 9.0},
		{Timestamp: tsVN(2027, 6, 12, 9, 0), RouteID: "a", RouteName: "mostly congested", TTI: 9.0},
		{Timestamp: tsVN(2027, 6, 12, 10, 0), RouteID: "a", RouteName: "mostly congested", TTI: 1.0}, // latest reading

		{Timestamp: tsVN(2027, 6, 12, 8, 0), RouteID: "b", RouteName: "mostly clear", TTI: 1.0},
		{Timestamp: tsVN(2027, 6, 12, 9, 0), RouteID: "b", RouteName: "mostly clear", TTI: 1.0},
		{Timestamp: tsVN(2027, 6, 12, 10, 0), RouteID: "b", RouteName: "mostly clear", TTI: 9.0}, // latest reading
	})

	rows, err := GetRouteTTIForDate(date, -1)
	if err != nil {
		t.Fatalf("GetRouteTTIForDate: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(rows))
	}
	wantAvgA := (9.0 + 9.0 + 1.0) / 3.0
	wantAvgB := (1.0 + 1.0 + 9.0) / 3.0
	if diff := wantAvgA - wantAvgB; diff < 1e-9 {
		t.Fatalf("test fixture invariant broken: want a's avg (%v) > b's avg (%v)", wantAvgA, wantAvgB)
	}
	if rows[0].RouteID != "a" {
		t.Fatalf("expected route 'a' ranked first by AvgTTI (%v > b's %v) despite b's higher LatestTTI (9.0 vs a's 1.0) — got order %+v", wantAvgA, wantAvgB, rows)
	}
	if rows[0].LatestTTI != 1.0 {
		t.Errorf("rows[0] ('a') LatestTTI = %v, want 1.0 — proves ranking used AvgTTI, not LatestTTI", rows[0].LatestTTI)
	}
	if rows[1].RouteID != "b" || rows[1].LatestTTI != 9.0 {
		t.Errorf("rows[1] = %+v, want route 'b' with LatestTTI 9.0", rows[1])
	}
}

func TestGetRouteTTIForDateCapsAtTen(t *testing.T) {
	setupSources(t)
	date := "2027-06-14"

	var entries []traveltime.LogEntry
	for i := 0; i < 12; i++ {
		entries = append(entries, traveltime.LogEntry{
			Timestamp: tsVN(2027, 6, 14, 8, 0),
			RouteID:   string(rune('a' + i)),
			RouteName: "route",
			TTI:       float64(i) + 1, // 1..12
		})
	}
	writeTravelTimeDay(t, date, entries)

	rows, err := GetRouteTTIForDate(date, -1)
	if err != nil {
		t.Fatalf("GetRouteTTIForDate: %v", err)
	}
	if len(rows) != 10 {
		t.Fatalf("expected capped at 10 routes, got %d", len(rows))
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].AvgTTI > rows[i-1].AvgTTI {
			t.Fatalf("rows not sorted descending by AvgTTI at index %d: %+v", i, rows)
		}
	}
	if rows[0].AvgTTI != 12 {
		t.Errorf("rows[0].AvgTTI = %v, want 12 (the single highest value)", rows[0].AvgTTI)
	}
}

func TestGetRouteTTIForDateHourFilterScopesAverageAndExcludesRoutesOutsideIt(t *testing.T) {
	setupSources(t)
	date := "2027-06-15"

	writeTravelTimeDay(t, date, []traveltime.LogEntry{
		// Route r1: entries in both hour 7 and hour 8.
		{Timestamp: tsVN(2027, 6, 15, 7, 10), RouteID: "r1", RouteName: "R1", TTI: 1.0},
		{Timestamp: tsVN(2027, 6, 15, 7, 40), RouteID: "r1", RouteName: "R1", TTI: 3.0},
		{Timestamp: tsVN(2027, 6, 15, 8, 0), RouteID: "r1", RouteName: "R1", TTI: 9.0},
		// Route r2: only has data in hour 7.
		{Timestamp: tsVN(2027, 6, 15, 7, 20), RouteID: "r2", RouteName: "R2", TTI: 5.0},
	})

	rows, err := GetRouteTTIForDate(date, 7)
	if err != nil {
		t.Fatalf("GetRouteTTIForDate hour=7: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected both routes present for hour 7 (each has data then), got %+v", rows)
	}
	byID := map[string]RouteTTIRow{}
	for _, r := range rows {
		byID[r.RouteID] = r
	}
	r1 := byID["r1"]
	wantR1Avg := (1.0 + 3.0) / 2.0 // hour 8's 9.0 reading must be excluded
	if diff := r1.AvgTTI - wantR1Avg; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("r1 AvgTTI (hour=7) = %v, want %v (hour 8 reading excluded)", r1.AvgTTI, wantR1Avg)
	}
	if r1.Samples != 2 {
		t.Errorf("r1 Samples (hour=7) = %d, want 2", r1.Samples)
	}
	// LatestTTI stays the day's overall last reading (9.0, from hour 8)
	// regardless of the hour filter — it's day-wide context, not scoped.
	if r1.LatestTTI != 9.0 {
		t.Errorf("r1 LatestTTI = %v, want 9.0 (day-wide, not hour-scoped)", r1.LatestTTI)
	}

	r2 := byID["r2"]
	if r2.AvgTTI != 5.0 || r2.Samples != 1 {
		t.Errorf("r2 unexpected for hour=7: %+v", r2)
	}

	// Now filter to hour 8: only r1 has data there, r2 must be dropped
	// entirely (nothing to compare it on).
	rowsH8, err := GetRouteTTIForDate(date, 8)
	if err != nil {
		t.Fatalf("GetRouteTTIForDate hour=8: %v", err)
	}
	if len(rowsH8) != 1 || rowsH8[0].RouteID != "r1" {
		t.Fatalf("expected only r1 for hour=8, got %+v", rowsH8)
	}
	if rowsH8[0].AvgTTI != 9.0 {
		t.Errorf("r1 AvgTTI (hour=8) = %v, want 9.0", rowsH8[0].AvgTTI)
	}

	// Slots always cover the whole day regardless of the hour filter.
	if len(rowsH8[0].Slots) != routeTotalSlots {
		t.Errorf("expected full-day slots even with hour filter, got %d", len(rowsH8[0].Slots))
	}
	slot7 := 7 * 60 / routeSlotMinutes
	if rowsH8[0].Slots[slot7] == nil {
		t.Error("expected hour-7 slot data to still be present in Slots even when ranking by hour=8")
	}
}

func TestGetRouteTTIForDateEmptyDayReturnsEmptySlice(t *testing.T) {
	setupSources(t)
	rows, err := GetRouteTTIForDate("2027-06-13", -1)
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

func TestHourOfTimestampHandlesInvalidInput(t *testing.T) {
	if got := hourOfTimestamp("not-a-timestamp"); got != -1 {
		t.Errorf("hourOfTimestamp(invalid) = %d, want -1", got)
	}
	if got := hourOfTimestamp(tsVN(2027, 6, 15, 14, 30)); got != 14 {
		t.Errorf("hourOfTimestamp = %d, want 14", got)
	}
}
