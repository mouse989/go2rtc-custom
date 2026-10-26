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

import (
	"sort"
	"time"

	"github.com/AlexxIT/go2rtc/internal/traveltime"
)

const (
	routeSlotMinutes = 15
	routeTotalSlots  = 1440 / routeSlotMinutes // 96 fifteen-minute slots/day
)

// RouteSlot is one 15-minute bucket's averaged TTI for a route. A nil
// *RouteSlot in RouteTTIRow.Slots means no measurement fell in that
// window that day.
type RouteSlot struct {
	TTI      float64 `json:"tti"`
	DelaySec int     `json:"delaySec"`
	Samples  int     `json:"samples"`
}

// RouteTTIRow is one route's full day of slotted TTI data. Slots always
// has exactly routeTotalSlots entries.
type RouteTTIRow struct {
	RouteID   string       `json:"routeId"`
	RouteName string       `json:"routeName"`
	LengthM   int          `json:"lengthM"`
	LatestTTI float64      `json:"latestTTI"` // this day's last-measured TTI (0 if none)
	Slots     []*RouteSlot `json:"slots"`
}

// slotOfTimestamp converts a log entry's RFC3339 (UTC) Timestamp into a
// Vietnam-local 15-minute slot index, or -1 if it doesn't parse.
func slotOfTimestamp(ts string) int {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return -1
	}
	local := t.In(vnLocation)
	return (local.Hour()*60 + local.Minute()) / routeSlotMinutes
}

// GetRouteTTIForDate returns one row per route with at least one valid
// (TTI > 0) measurement in traveltime's log for the given Vietnam-local
// calendar day ("YYYY-MM-DD"), sorted by that day's latest-measured TTI
// descending (most congested first — same ordering dashboard2.html's
// panel uses) and capped at the top 10.
func GetRouteTTIForDate(date string) ([]RouteTTIRow, error) {
	entries, err := traveltime.GetLogsForDate(date, 0)
	if err != nil {
		return nil, err
	}

	type accSlot struct {
		ttiSum   float64
		delaySum int
		n        int
	}
	type routeAcc struct {
		name      string
		lengthM   int
		slots     [routeTotalSlots]accSlot
		latestTS  string
		latestTTI float64
	}
	byID := map[string]*routeAcc{}
	var order []string

	for _, e := range entries {
		if e.RouteID == "" || e.TTI <= 0 {
			continue
		}
		a, ok := byID[e.RouteID]
		if !ok {
			a = &routeAcc{name: e.RouteName, lengthM: e.LengthM}
			byID[e.RouteID] = a
			order = append(order, e.RouteID)
		}
		if si := slotOfTimestamp(e.Timestamp); si >= 0 && si < routeTotalSlots {
			s := &a.slots[si]
			s.ttiSum += e.TTI
			s.delaySum += e.DelaySec
			s.n++
		}
		if e.Timestamp > a.latestTS {
			a.latestTS = e.Timestamp
			a.latestTTI = e.TTI
		}
	}

	rows := make([]RouteTTIRow, 0, len(order))
	for _, id := range order {
		a := byID[id]
		row := RouteTTIRow{RouteID: id, RouteName: a.name, LengthM: a.lengthM, LatestTTI: a.latestTTI}
		row.Slots = make([]*RouteSlot, routeTotalSlots)
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

	sort.Slice(rows, func(i, j int) bool { return rows[i].LatestTTI > rows[j].LatestTTI })
	if len(rows) > 10 {
		rows = rows[:10]
	}
	return rows, nil
}
