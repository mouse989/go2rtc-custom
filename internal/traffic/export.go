package traffic

import "time"

// DashPoint is a jam cluster point for the dashboard map.
type DashPoint struct {
	Lat       float64 `json:"lat"`
	Lng       float64 `json:"lng"`
	JamFactor float64 `json:"jf"`
	Label     string  `json:"label,omitempty"`
	Area      string  `json:"area,omitempty"`
	Region    string  `json:"region,omitempty"`
}

// TrafficSnapshot holds the data the dashboard needs from the traffic module.
type TrafficSnapshot struct {
	ScannedAt  string      `json:"scannedAt,omitempty"`
	Running    bool        `json:"running"`
	Raw        int         `json:"raw"`
	Filtered   int         `json:"filtered"`
	Persistent int         `json:"persistent"`
	Severe     int         `json:"severe"` // jamFactor >= 8
	Points     []DashPoint `json:"points"` // persistent points for map
}

// LatestSnapshot returns the most recent scan data for the dashboard summary
// and map heatmap APIs. It reads directly from the live in-memory scan
// result (updated at the end of every runScan), not from the on-disk daily
// history — the history file only exists when Storage.Enabled is on and is
// keyed by calendar day, so round-tripping through it made the map lag by
// up to a full scan interval (or serve stale/empty data whenever storage
// was off or a day file was missing), which looked like the layer was stuck
// on cached data. Reading the live scan result removes that indirection
// entirely; the on-disk history is still written and used separately for
// the history/export APIs.
func LatestSnapshot() TrafficSnapshot {
	_, _, rawCount, filtCount, persCount, _, running := getScanState()
	pts, scannedAt := getLastPersistent()

	snap := TrafficSnapshot{
		Running:    running,
		Raw:        rawCount,
		Filtered:   filtCount,
		Persistent: persCount,
	}
	if !scannedAt.IsZero() {
		snap.ScannedAt = scannedAt.Format(time.RFC3339)
	}
	for _, p := range pts {
		if p.JamFactor >= 8 {
			snap.Severe++
		}
		snap.Points = append(snap.Points, DashPoint{
			Lat:       p.Lat,
			Lng:       p.Lng,
			JamFactor: p.JamFactor,
			Label:     p.Label,
			Area:      p.Area,
			Region:    p.RegionName,
		})
	}
	return snap
}
