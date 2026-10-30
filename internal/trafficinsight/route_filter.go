package trafficinsight

// route_filter.go — "khu vực"/"loại đường" filtering, shared by every TTI
// aggregation in this package (daily/hourly trend, weekday matrix, Top 10
// tuyến đường): the admin-managed Route.Region/Route.RoadType tags (see
// internal/traveltime/routes.go, regions.go) let an operator split the
// city-wide TTI trend by district or road classification instead of
// always seeing one blended number across every monitored route.
// Deliberately NOT applied to jam/incident metrics on the same charts —
// those are point-based (traffic persistent points, incident locations),
// not tied to any route, so a route filter has nothing to act on for
// them; computeDailyMetrics/GetHourlySeries only apply it to the TTI/
// delay portion.

import "github.com/AlexxIT/go2rtc/internal/traveltime"

// RouteFilter narrows which routes' TTI entries count toward an
// aggregate. An empty field means "no constraint on that dimension" — the
// zero value RouteFilter{} matches every route, same as not filtering at
// all (every existing caller that doesn't care about this keeps passing
// RouteFilter{}).
type RouteFilter struct {
	Region   string
	RoadType string
}

// Empty reports whether f constrains nothing — callers use this to skip
// building the route lookup entirely when there's nothing to filter.
func (f RouteFilter) Empty() bool {
	return f.Region == "" && f.RoadType == ""
}

// routeTagLookup maps a route ID to its Region/RoadType, built once per
// call from traveltime.ListRoutes() — the exported cross-package
// accessor, never traveltime's own HTTP endpoint (admin-gated, would 403
// a TabTrafficInsight-only viewer — same principle this whole package
// already follows for traffic.PersistentPointsInRange/incidents.List).
type routeTagLookup map[string]traveltime.Route

func buildRouteTagLookup() routeTagLookup {
	routes := traveltime.ListRoutes()
	out := make(routeTagLookup, len(routes))
	for _, r := range routes {
		out[r.ID] = *r
	}
	return out
}

// matches reports whether routeID passes f, using lookup to find that
// route's tags. A route ID with no entry in lookup (route since deleted,
// or lookup is nil because f.Empty()) only matches an empty filter.
func (f RouteFilter) matches(routeID string, lookup routeTagLookup) bool {
	if f.Empty() {
		return true
	}
	rt, ok := lookup[routeID]
	if !ok {
		return false
	}
	if f.Region != "" && rt.Region != f.Region {
		return false
	}
	if f.RoadType != "" && rt.RoadType != f.RoadType {
		return false
	}
	return true
}
