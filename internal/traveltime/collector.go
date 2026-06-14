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

func getRouteStats(ctx context.Context, client *http.Client, apiKey string, r *Route) (*RouteStats, error) {
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
	for i := 0; i < len(points)-1; i++ {
		seg, err := fetchSegment(ctx, client, apiKey, points[i], points[i+1])
		if err != nil {
			return nil, fmt.Errorf("segment %d→%d: %w", i, i+1, err)
		}
		stats.LengthM += seg.Distance.Value
		stats.DurationSec += seg.Duration.Value
		stats.BaseSec += seg.BaseDuration.Value
		stats.DelaySec += seg.TrafficDelay.Value
	}
	if stats.BaseSec > 0 {
		stats.TTI = float64(stats.DurationSec) / float64(stats.BaseSec)
		// Round to 2 decimal places
		stats.TTI = float64(int(stats.TTI*100+0.5)) / 100
	} else {
		stats.TTI = 1.0
	}
	return stats, nil
}
