package traffic

// flow_capture_parse.go — full-detail parsing of the traffic.genfast.vn
// /api/traffic/flow response, confirmed against real production samples
// (captured 2026-10-30): it is HERE Traffic Flow API-shaped
// ({"sourceUpdated":...,"results":[{"location":{...},"currentFlow":{...}}]}),
// the same shape monitor.go's parseHERE already defends against — but that
// function only ever extracts JamFactor+Speed+a merged Shape per LINK, for
// extractPoints' map-point clustering. Real responses carry much more:
// street name, per-link length/road class, free-flow speed, a confidence
// score, and — most importantly — subSegments: when congestion varies along
// one named road, the API already splits it into pieces with their own
// speed/jamFactor (e.g. one real sample road was 1054m with 3 subSegments:
// 172m at JF 9.1, a 673m relatively clear 673m at JF 3.9, then 209m back up
// to JF 9.1 — exactly "which part of this road is jammed" with zero
// geometry-matching needed).
//
// This file is a deliberately SEPARATE, read-only path from monitor.go's
// existing segment/parseHERE/extractPoints pipeline — it changes nothing
// about how the live jam-point map already works. It exists purely to feed
// flow_capture_db.go's research storage (see that file), gated by
// Config.FlowCaptureEnabled so it never runs unless an admin opts in.
//
// Important confirmed unit note: currentFlow.speed/speedUncapped/freeFlow
// are in METRES PER SECOND, not km/h as an existing (unrelated, unused)
// comment on traffic.Point.Speed elsewhere in this package claims —
// confirmed by the real samples: a speed/freeFlow pair of 1.94/7.78 read as
// km/h would mean a "free-flow" street speed of under 8 km/h, implausible
// for any real road; read as m/s (×3.6 → ~7/~28 km/h) it matches a busy
// street under heavy jam exactly as its jamFactor (8.7) says. Every Mps
// field below is stored in that same raw unit; FlowSegmentRow (in
// flow_capture_db.go) exposes the ×3.6 km/h conversion for convenience.

import (
	"encoding/json"
	"fmt"
)

// flowSubSegment is one finer slice of a flowResult's own length, each with
// its own speed/jamFactor — present only when a result's congestion isn't
// uniform along its length. See this file's own doc comment for a real
// example.
type flowSubSegment struct {
	LengthM          float64
	SpeedMps         float64
	SpeedUncappedMps float64
	FreeFlowMps      float64
	JamFactor        float64
	HasJamTendency   bool
	JamTendency      float64
	Confidence       float64
	Traversability   string
}

// flowLink is one piece of a flowResult's own polyline (location.shape.links[]
// in the raw response) — unlike monitor.go's segment.Shape (every link's
// points flattened into one slice), each link keeps its own length and
// functionalClass (road class) so flow_capture_db.go can store them.
type flowLink struct {
	Points          [][2]float64 // [lat,lng] pairs
	LengthM         float64
	FunctionalClass int
}

// flowResult is one "results[]" entry — one named road (or unnamed segment)
// within the scanned bbox, with its own flow stats and optionally
// subSegments where congestion isn't uniform along it.
type flowResult struct {
	Description      string
	LengthM          float64
	Links            []flowLink
	SpeedMps         float64
	SpeedUncappedMps float64
	FreeFlowMps      float64
	JamFactor        float64
	HasJamTendency   bool
	JamTendency      float64
	Confidence       float64
	Traversability   string
	SubSegments      []flowSubSegment
}

// parseFlowResults parses the full traffic/flow response. A response that
// isn't valid JSON at all is an error; one that's valid JSON but not in
// this confirmed shape (e.g. one of the 3 legacy formats
// monitor.go's normalizeTrafficData defends against, never actually
// confirmed in production) simply yields (nil, "", nil) — capture skips
// that scan rather than failing it, since the live jam-point map (the
// feature that actually matters in production) uses a separate parse path
// and is unaffected either way.
func parseFlowResults(data []byte) (results []flowResult, sourceUpdated string, err error) {
	var env struct {
		SourceUpdated string `json:"sourceUpdated"`
		Results       []struct {
			Location struct {
				Description string  `json:"description"`
				Length      float64 `json:"length"`
				Shape       struct {
					Links []struct {
						Points []struct {
							Lat float64 `json:"lat"`
							Lng float64 `json:"lng"`
						} `json:"points"`
						Length          float64 `json:"length"`
						FunctionalClass int     `json:"functionalClass"`
					} `json:"links"`
				} `json:"shape"`
			} `json:"location"`
			CurrentFlow struct {
				Speed          float64  `json:"speed"`
				SpeedUncapped  float64  `json:"speedUncapped"`
				FreeFlow       float64  `json:"freeFlow"`
				JamFactor      float64  `json:"jamFactor"`
				JamTendency    *float64 `json:"jamTendency"`
				Confidence     float64  `json:"confidence"`
				Traversability string   `json:"traversability"`
				SubSegments    []struct {
					Length         float64  `json:"length"`
					Speed          float64  `json:"speed"`
					SpeedUncapped  float64  `json:"speedUncapped"`
					FreeFlow       float64  `json:"freeFlow"`
					JamFactor      float64  `json:"jamFactor"`
					JamTendency    *float64 `json:"jamTendency"`
					Confidence     float64  `json:"confidence"`
					Traversability string   `json:"traversability"`
				} `json:"subSegments"`
			} `json:"currentFlow"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, "", fmt.Errorf("parse flow response: %w", err)
	}
	if len(env.Results) == 0 {
		return nil, env.SourceUpdated, nil
	}

	for _, r := range env.Results {
		cf := r.CurrentFlow
		fr := flowResult{
			Description:      r.Location.Description,
			LengthM:          r.Location.Length,
			SpeedMps:         cf.Speed,
			SpeedUncappedMps: cf.SpeedUncapped,
			FreeFlowMps:      cf.FreeFlow,
			JamFactor:        cf.JamFactor,
			Confidence:       cf.Confidence,
			Traversability:   cf.Traversability,
		}
		if cf.JamTendency != nil {
			fr.HasJamTendency, fr.JamTendency = true, *cf.JamTendency
		}
		for _, lk := range r.Location.Shape.Links {
			fl := flowLink{LengthM: lk.Length, FunctionalClass: lk.FunctionalClass}
			for _, p := range lk.Points {
				fl.Points = append(fl.Points, [2]float64{p.Lat, p.Lng})
			}
			fr.Links = append(fr.Links, fl)
		}
		for _, ss := range cf.SubSegments {
			sub := flowSubSegment{
				LengthM: ss.Length, SpeedMps: ss.Speed, SpeedUncappedMps: ss.SpeedUncapped,
				FreeFlowMps: ss.FreeFlow, JamFactor: ss.JamFactor,
				Confidence: ss.Confidence, Traversability: ss.Traversability,
			}
			if ss.JamTendency != nil {
				sub.HasJamTendency, sub.JamTendency = true, *ss.JamTendency
			}
			fr.SubSegments = append(fr.SubSegments, sub)
		}
		results = append(results, fr)
	}
	return results, env.SourceUpdated, nil
}
