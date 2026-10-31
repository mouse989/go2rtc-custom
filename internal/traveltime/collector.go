package traveltime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const vietmapTrafficURL = "http://traffic.genfast.vn/api/routing"

type vietmapSegResp struct {
	Distance struct {
		Value int `json:"value"`
	} `json:"distance"`
	Duration struct {
		Value int `json:"value"`
	} `json:"duration"`
	BaseDuration struct {
		Value int `json:"value"`
	} `json:"baseDuration"`
	TrafficDelay struct {
		Value int `json:"value"`
	} `json:"trafficDelay"`
}

// RouteStats holds aggregated travel-time statistics for a route.
type RouteStats struct {
	LengthM     int
	DurationSec int
	BaseSec     int
	DelaySec    int
	TTI         float64 // DurationSec / BaseSec
}

// RouteLegStat is one leg's own stats — point[i]→point[i+1] of a route's
// Origin→Waypoints→Destination chain. getRouteStats already fetches one of
// these per leg to build its aggregate RouteStats; this exposes each leg's
// own numbers too, for per-segment TTI display (see segment_capture_db.go)
// — "collect TTI per segment by adding waypoints" needs exactly this, and
// costs no extra API calls beyond what a route with waypoints already made.
type RouteLegStat struct {
	LegIndex    int
	FromLat     float64
	FromLng     float64
	ToLat       float64
	ToLng       float64
	LengthM     int
	DurationSec int
	BaseSec     int
	DelaySec    int
	TTI         float64
}

// parseLatLng parses a "lat,lng" string as stored on Route.Origin/
// Destination/Waypoints. Returns (0,0) on malformed input rather than an
// error — a leg whose endpoint fails to parse just gets plotted at the
// origin on the map, not dropped entirely (its TTI/delay numbers are still
// correct and usable even if its map position is wrong).
func parseLatLng(s string) (lat, lng float64) {
	parts := strings.SplitN(strings.TrimSpace(s), ",", 2)
	if len(parts) != 2 {
		return 0, 0
	}
	fmt.Sscanf(strings.TrimSpace(parts[0]), "%f", &lat)
	fmt.Sscanf(strings.TrimSpace(parts[1]), "%f", &lng)
	return lat, lng
}

func fetchSegment(ctx context.Context, client *http.Client, apiKey, origin, destination string) (*vietmapSegResp, error) {
	url := fmt.Sprintf("%s?origin=%s&destination=%s", vietmapTrafficURL, origin, destination)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Apikey", apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vietmap API %d: %s", resp.StatusCode, body)
	}
	var v vietmapSegResp
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return &v, nil
}

// getRouteStats fetches one route's travel-time stats, both the aggregate
// (RouteStats, unchanged shape/meaning — everything that already consumes
// this keeps working exactly as before) and, alongside it, every leg's own
// stats ([]RouteLegStat) — point[i]→point[i+1] of Origin→Waypoints→
// Destination. A route with no waypoints still returns exactly one leg
// (Origin→Destination), identical to its own aggregate.
func getRouteStats(ctx context.Context, client *http.Client, apiKey string, r *Route) (*RouteStats, []RouteLegStat, error) {
	points := []string{r.Origin}
	if r.Waypoints != "" {
		for _, wp := range strings.Split(r.Waypoints, "|") {
			if wp = strings.TrimSpace(wp); wp != "" {
				points = append(points, wp)
			}
		}
	}
	points = append(points, r.Destination)

	stats := &RouteStats{}
	legs := make([]RouteLegStat, 0, len(points)-1)
	for i := 0; i < len(points)-1; i++ {
		seg, err := fetchSegment(ctx, client, apiKey, points[i], points[i+1])
		if err != nil {
			return nil, nil, fmt.Errorf("segment %d→%d: %w", i, i+1, err)
		}
		stats.LengthM += seg.Distance.Value
		stats.DurationSec += seg.Duration.Value
		stats.BaseSec += seg.BaseDuration.Value
		stats.DelaySec += seg.TrafficDelay.Value

		leg := RouteLegStat{
			LegIndex: i, LengthM: seg.Distance.Value, DurationSec: seg.Duration.Value,
			BaseSec: seg.BaseDuration.Value, DelaySec: seg.TrafficDelay.Value,
		}
		leg.FromLat, leg.FromLng = parseLatLng(points[i])
		leg.ToLat, leg.ToLng = parseLatLng(points[i+1])
		if leg.BaseSec > 0 {
			leg.TTI = roundTTI(float64(leg.DurationSec) / float64(leg.BaseSec))
		} else {
			leg.TTI = 1.0
		}
		legs = append(legs, leg)
	}
	if stats.BaseSec > 0 {
		stats.TTI = roundTTI(float64(stats.DurationSec) / float64(stats.BaseSec))
	} else {
		stats.TTI = 1.0
	}
	return stats, legs, nil
}

func roundTTI(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}
