package traffic

import "encoding/json"

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

// LatestSnapshot returns the most recent scan data for the dashboard summary API.
func LatestSnapshot() TrafficSnapshot {
	_, _, rawCount, filtCount, persCount, _, running := getScanState()
	snap := TrafficSnapshot{
		Running:    running,
		Raw:        rawCount,
		Filtered:   filtCount,
		Persistent: persCount,
	}

	files, err := listScanFiles(1)
	if err != nil || len(files) == 0 {
		return snap
	}
	data, err := readScanFile(files[0].Name)
	if err != nil {
		return snap
	}
	var rec dailyRecord
	if err := json.Unmarshal(data, &rec); err != nil || len(rec.Scans) == 0 {
		return snap
	}
	last := rec.Scans[len(rec.Scans)-1]
	snap.ScannedAt = last.ScannedAt
	snap.Raw = len(last.Raw)
	snap.Filtered = len(last.Filtered)
	snap.Persistent = len(last.Persistent)
	for _, p := range last.Persistent {
		if p.JamFactor >= 8 {
			snap.Severe++
		}
		snap.Points = append(snap.Points, DashPoint{
			Lat:    p.Lat,
			Lng:    p.Lng,
			JamFactor: p.JamFactor,
			Label:  p.Label,
			Area:   p.Area,
			Region: p.Region,
		})
	}
	return snap
}
