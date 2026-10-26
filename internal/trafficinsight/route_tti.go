package trafficinsight

// route_tti.go — backend for the page's "Top 10 tuyến đường" panel: a
// simplified, un-forecast version of dashboard2.html's own per-route TTI
// chart (dashboard2's version also layers trend arrows, abnormal-route
// detection, and a Holt-Winters +10'/+60' forecast on top — out of scope
// here; this returns only what was actually measured on the chosen day).
// Gated by TabTrafficInsight (api.go's requireViewer + its own
// tabExtraPaths entry) rather than TabDashboard/TabDashboard2, so a
// trafficinsight-only viewer can reach it without also needing Dashboard
// access.
//
// Routes are ranked by an AVERAGE over the comparison scope (the whole
// day, or one chosen hour — see hour below), not by the single
// latest-measured reading: the latest reading is whatever the most recent
// scan happened to catch, which is a poor stand-in for "which route was
// actually worst" across a day or an hour. LatestTTI is still reported
// alongside AvgTTI, as separate context, not as the ranking key.

import (
	"sort"
	"time"

	"github.com/AlexxIT/go2rtc/internal/traveltime"
)

const (
	routeMinSlotMinutes = 1  // finest slot width allowed, regardless of config
	routeMaxSlotMinutes = 60 // coarsest slot width allowed, regardless of config
)

// RouteSlotMinutes returns the slot width (minutes) the "Top 10 tuyến
// đường" chart buckets into — the travel-time module's own configured
// collection interval (admin-configurable in Config → Travel Time,
// typically 10 or 15 min), clamped to a sane range. This mirrors
// dashboard2.html's own slot sizing (its SLOT_MIN, driven by the same
// traveltime.IntervalMin()) so the two per-route TTI charts read off the
// same grid instead of a value hardcoded independently of the config.
func RouteSlotMinutes() int {
	m := traveltime.IntervalMin()
	if m < routeMinSlotMinutes {
		return routeMinSlotMinutes
	}
	if m > routeMaxSlotMinutes {
		return routeMaxSlotMinutes
	}
	return m
}

// routeTotalSlots returns how many slots of the given width cover a full
// day, rounded up — so a configured interval that doesn't evenly divide
// 1440 (e.g. 7 min) still gets a trailing partial slot instead of losing
// the last few minutes of the day to integer truncation.
func routeTotalSlots(slotMin int) int {
	return (1440 + slotMin - 1) / slotMin
}

// RouteSlot is one slot's (width given by RouteSlotMinutes) averaged TTI
// for a route. A nil *RouteSlot in RouteTTIRow.Slots means no measurement
// fell in that window that day. Slots always covers the FULL day
// regardless of the hour filter below — only AvgTTI/AvgDelaySec/Samples
// are scoped to it — so the chart keeps showing the whole day for context
// even when ranking by one hour.
type RouteSlot struct {
	TTI      float64 `json:"tti"`
	DelaySec int     `json:"delaySec"`
	Samples  int     `json:"samples"`
}

// RouteTTIRow is one route's TTI data for the comparison scope requested.
type RouteTTIRow struct {
	RouteID   string       `json:"routeId"`
	RouteName string       `json:"routeName"`
	LengthM   int          `json:"lengthM"`
	LatestTTI float64      `json:"latestTTI"` // this day's last-measured TTI (0 if none) — context, not the ranking key
	// AvgTTI/AvgDelaySec/Samples are the comparison-scope average this
	// row was ranked by: the whole day's entries, or just the selected
	// hour's when GetRouteTTIForDate's hour parameter is >= 0.
	AvgTTI      float64      `json:"avgTTI"`
	AvgDelaySec int          `json:"avgDelaySec"`
	Samples     int          `json:"samples"`
	Slots       []*RouteSlot `json:"slots"`
}

// slotOfTimestamp converts a log entry's RFC3339 (UTC) Timestamp into a
// Vietnam-local slot index of the given width (minutes), or -1 if it
// doesn't parse.
func slotOfTimestamp(ts string, slotMin int) int {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return -1
	}
	local := t.In(vnLocation)
	return (local.Hour()*60 + local.Minute()) / slotMin
}

// hourOfTimestamp returns the Vietnam-local hour (0-23) of an RFC3339
// timestamp, or -1 if it doesn't parse.
func hourOfTimestamp(ts string) int {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return -1
	}
	return t.In(vnLocation).Hour()
}

// GetRouteTTIForDate returns one row per route with at least one valid
// (TTI > 0) measurement within the comparison scope — the full Vietnam-
// local calendar day ("YYYY-MM-DD") when hour < 0, or just that single
// Vietnam-local hour (0-23) when hour >= 0 — sorted by that scope's
// average TTI descending (most congested first) and capped at the top 10.
// A route with no measurement anywhere in the scope is dropped entirely:
// there is nothing to compare it on.
func GetRouteTTIForDate(date string, hour int) ([]RouteTTIRow, error) {
	entries, err := traveltime.GetLogsForDate(date, 0)
	if err != nil {
		return nil, err
	}

	slotMin := RouteSlotMinutes()
	totalSlots := routeTotalSlots(slotMin)

	type slotAcc struct {
		ttiSum   float64
		delaySum int
		n        int
	}
	type routeAcc struct {
		name      string
		lengthM   int
		slots     []slotAcc
		latestTS  string
		latestTTI float64
		// scopeTTISum/scopeDelaySum/scopeN: the comparison-scope totals
		// (whole day, or just the selected hour) — separate from the
		// per-slot totals above, since Slots always covers the full day.
		scopeTTISum   float64
		scopeDelaySum int
		scopeN        int
	}
	byID := map[string]*routeAcc{}
	var order []string

	for _, e := range entries {
		if e.RouteID == "" || e.TTI <= 0 {
			continue
		}
		a, ok := byID[e.RouteID]
		if !ok {
			a = &routeAcc{name: e.RouteName, lengthM: e.LengthM, slots: make([]slotAcc, totalSlots)}
			byID[e.RouteID] = a
			order = append(order, e.RouteID)
		}
		if si := slotOfTimestamp(e.Timestamp, slotMin); si >= 0 && si < totalSlots {
			s := &a.slots[si]
			s.ttiSum += e.TTI
			s.delaySum += e.DelaySec
			s.n++
		}
		if e.Timestamp > a.latestTS {
			a.latestTS = e.Timestamp
			a.latestTTI = e.TTI
		}
		if hour < 0 || hourOfTimestamp(e.Timestamp) == hour {
			a.scopeTTISum += e.TTI
			a.scopeDelaySum += e.DelaySec
			a.scopeN++
		}
	}

	rows := make([]RouteTTIRow, 0, len(order))
	for _, id := range order {
		a := byID[id]
		if a.scopeN == 0 {
			continue // nothing in the requested scope — exclude from the comparison
		}
		row := RouteTTIRow{
			RouteID: id, RouteName: a.name, LengthM: a.lengthM, LatestTTI: a.latestTTI,
			AvgTTI:      a.scopeTTISum / float64(a.scopeN),
			AvgDelaySec: a.scopeDelaySum / a.scopeN,
			Samples:     a.scopeN,
		}
		row.Slots = make([]*RouteSlot, totalSlots)
		for i := range a.slots {
			if a.slots[i].n > 0 {
				row.Slots[i] = &RouteSlot{
					TTI:      a.slots[i].ttiSum / float64(a.slots[i].n),
					DelaySec: a.slots[i].delaySum / a.slots[i].n,
					Samples:  a.slots[i].n,
				}
			}
		}
		rows = append(rows, row)
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].AvgTTI > rows[j].AvgTTI })
	if len(rows) > 10 {
		rows = rows[:10]
	}
	return rows, nil
}
