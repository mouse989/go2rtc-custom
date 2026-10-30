package trafficinsight

import "github.com/AlexxIT/go2rtc/internal/traveltime"

// NetworkStatusTier is one TTI severity band's current route count — same
// tiers/colors/labels as Config → Travel Time → Appearance (shared with
// dashboard.html/dashboard2.html's own TTI chart legend), just counted
// across every monitored route's LATEST scan instead of drawn as a chart
// bar. Mirrors dashboard2.html's updateRouteStatus() status-chip row.
type NetworkStatusTier struct {
	Label string `json:"label"`
	Color string `json:"color"`
	Count int    `json:"count"`
}

// NetworkStatusForecast is one AI-forecast pill: how many routes are
// expected to reach, or worsen into, a tier at a given horizon.
type NetworkStatusForecast struct {
	MinutesAhead int    `json:"minutesAhead"`
	Count        int    `json:"count"`
	TierLabel    string `json:"tierLabel"`
	TierColor    string `json:"tierColor"`
}

// NetworkStatus is "Trạng thái mạng lưới" — the Chỉ số giao thông hôm nay
// module's own route-status summary. Always unfiltered (same as
// handleToday/handleAverages): it's a live, whole-network snapshot, not one
// of the 4 Region/RoadType-filterable trend modules (route_filter.go).
type NetworkStatus struct {
	Tiers       []NetworkStatusTier `json:"tiers"`
	NoDataCount int                 `json:"noDataCount"`
	NoDataColor string              `json:"noDataColor"`
	// Forecast10/Forecast60 are nil when nothing qualifies (no forecast
	// history yet, or the count is zero) — same "don't show an empty chip"
	// rule dashboard2.html's own forecast pills follow.
	Forecast10 *NetworkStatusForecast `json:"forecast10,omitempty"`
	Forecast60 *NetworkStatusForecast `json:"forecast60,omitempty"`
}

// tierIndex classifies a TTI value into its appearance-tier index, the same
// rule every ttiTier()/tiTtiTier() implementation across the JS pages uses:
// the first tier whose MaxTTI exceeds v, else the last (catch-all,
// MaxTTI==0) tier. Returns -1 for "no data" (v <= 0).
func tierIndex(v float64, tiers []traveltime.TTITier) int {
	if v <= 0 || len(tiers) == 0 {
		return -1
	}
	for i := 0; i < len(tiers)-1; i++ {
		if v < tiers[i].MaxTTI {
			return i
		}
	}
	return len(tiers) - 1
}

const (
	netStatusForecastNearMin = 10
	netStatusForecastFarMin  = 60
)

// classifyForecasts turns raw per-route forecasts into the near/far
// network-status counts GetNetworkStatus's own doc comment describes.
// Split out so it can be unit-tested with handcrafted
// traveltime.RouteForecast fixtures instead of needing enough real
// historical log data for traveltime.Forecast's seasonal model to produce
// any points at all.
func classifyForecasts(forecasts []traveltime.RouteForecast, curTier map[string]int, tiers []traveltime.TTITier) (degradeNear, worstNearIdx, jamFar int) {
	worstNearIdx = -1
	if len(tiers) == 0 {
		return
	}
	worstIdx := len(tiers) - 1
	for _, rf := range forecasts {
		ci, ok := curTier[rf.RouteID]
		if !ok {
			continue
		}
		var pNear, pFar *traveltime.ForecastPoint
		for i := range rf.Points {
			switch rf.Points[i].MinutesAhead {
			case netStatusForecastNearMin:
				pNear = &rf.Points[i]
			case netStatusForecastFarMin:
				pFar = &rf.Points[i]
			}
		}
		if pNear != nil {
			if fi := tierIndex(pNear.TTI, tiers); fi > ci {
				degradeNear++
				if fi > worstNearIdx {
					worstNearIdx = fi
				}
			}
		}
		if pFar != nil && tierIndex(pFar.TTI, tiers) == worstIdx {
			jamFar++
		}
	}
	return
}

// GetNetworkStatus computes "Trạng thái mạng lưới": how many monitored
// routes currently sit in each TTI tier (from traveltime.LatestSnapshot,
// each route's latest scan — the same source trang Dashboard's own "TTI
// TB" uses), plus two AI-forecast pills built from traveltime.Forecast:
//   - +10′: routes whose forecast TTI at +10′ sits in a WORSE tier than
//     right now (an early warning — "about to worsen"), labelled with the
//     worst tier any of them reaches.
//   - +60′: routes whose forecast TTI at +60′ lands in the single worst
//     tier (Tắc nghẽn) — "will be jammed", regardless of their current tier.
//
// Both mirror dashboard2.html's own updateRouteStatus() forecast chips
// (degrade-to-worst-reached at the near horizon, reach-worst-tier at the
// far one) — same two-horizon shape, fixed at 10′/60′ here instead of
// dashboard2's SLOT_MIN-dependent near horizon.
func GetNetworkStatus() NetworkStatus {
	appearance := traveltime.GetAppearance()
	tiers := appearance.Tiers
	snap := traveltime.LatestSnapshot()

	counts := make([]int, len(tiers))
	noData := 0
	curTier := make(map[string]int, len(snap.Routes))
	for _, rs := range snap.Routes {
		idx := tierIndex(rs.LatestTTI, tiers)
		if idx < 0 {
			noData++
			continue
		}
		counts[idx]++
		curTier[rs.ID] = idx
	}

	out := NetworkStatus{NoDataCount: noData, NoDataColor: appearance.NoDataColor}
	for i, t := range tiers {
		out.Tiers = append(out.Tiers, NetworkStatusTier{Label: t.Label, Color: t.Color, Count: counts[i]})
	}
	if len(tiers) == 0 {
		return out
	}
	worstIdx := len(tiers) - 1

	forecasts := traveltime.Forecast(netStatusForecastNearMin, netStatusForecastFarMin)
	degradeNear, worstNearIdx, jamFar := classifyForecasts(forecasts, curTier, tiers)

	if degradeNear > 0 {
		out.Forecast10 = &NetworkStatusForecast{
			MinutesAhead: netStatusForecastNearMin, Count: degradeNear,
			TierLabel: tiers[worstNearIdx].Label, TierColor: tiers[worstNearIdx].Color,
		}
	}
	if jamFar > 0 {
		out.Forecast60 = &NetworkStatusForecast{
			MinutesAhead: netStatusForecastFarMin, Count: jamFar,
			TierLabel: tiers[worstIdx].Label, TierColor: tiers[worstIdx].Color,
		}
	}
	return out
}
