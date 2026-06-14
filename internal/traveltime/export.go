package traveltime

// DashRouteStats holds the latest measurement for one route (for dashboard).
type DashRouteStats struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Origin         string  `json:"origin"`
	Destination    string  `json:"destination"`
	Waypoints      string  `json:"waypoints,omitempty"`
	LatestTTI      float64 `json:"latestTTI"`
	LatestDelaySec int     `json:"latestDelaySec"`
	LatestLengthM  int     `json:"latestLengthM"`
	Timestamp      string  `json:"timestamp,omitempty"`
}

// TravelTimeSnapshot is the travel time data for the dashboard summary API.
type TravelTimeSnapshot struct {
	Date          string           `json:"date"`
	RouteCount    int              `json:"routeCount"`
	TotalLengthKm float64          `json:"totalLengthKm"`
	AvgTTI        float64          `json:"avgTTI"`
	AvgDelaySec   float64          `json:"avgDelaySec"`
	Routes        []DashRouteStats `json:"routes"`
	TodayLogs     []LogEntry       `json:"todayLogs"` // all today's entries for heatmap
}

// GetLogsForDate returns all log entries for a specific date (for dashboard history comparison).
func GetLogsForDate(date string, limit int) ([]LogEntry, error) {
	return getLogs(date, limit)
}

// LatestSnapshot returns today's travel time data for the dashboard summary API.
func LatestSnapshot() TravelTimeSnapshot {
	entries, _ := getLogs("", 1000)

	latestMap := map[string]LogEntry{}
	for _, e := range entries {
		cur, ok := latestMap[e.RouteID]
		if !ok || e.Timestamp > cur.Timestamp {
			latestMap[e.RouteID] = e
		}
	}

	routes := listRoutes()
	snap := TravelTimeSnapshot{RouteCount: len(routes)}

	var ttiSum, delaySum float64
	var n int

	for _, r := range routes {
		rs := DashRouteStats{
			ID:          r.ID,
			Name:        r.Name,
			Origin:      r.Origin,
			Destination: r.Destination,
			Waypoints:   r.Waypoints,
		}
		if e, ok := latestMap[r.ID]; ok {
			rs.LatestTTI = e.TTI
			rs.LatestDelaySec = e.DelaySec
			rs.LatestLengthM = e.LengthM
			rs.Timestamp = e.Timestamp
			if snap.Date == "" && len(e.Timestamp) >= 10 {
				snap.Date = e.Timestamp[:10]
			}
			if e.TTI > 0 {
				ttiSum += e.TTI
				delaySum += float64(e.DelaySec)
				n++
			}
			snap.TotalLengthKm += float64(e.LengthM) / 1000.0
		}
		snap.Routes = append(snap.Routes, rs)
	}

	if n > 0 {
		snap.AvgTTI = ttiSum / float64(n)
		snap.AvgDelaySec = delaySum / float64(n)
	}
	snap.TodayLogs = entries
	return snap
}
