package gis

import "math"

// This file ports the exact proximity math www/map.html already uses for
// the "Search area" (circle) and "🛣 Theo tuyến" (route corridor) camera
// tools — haversine(), toXYkm(), projectOnSegmentKm(), nearestOnPolyline()
// — so "is this incident near a congestion-risk point/segment" behaves
// identically to those existing, already-tuned features, per the admin's
// explicit request to reuse that same definition of "gần".

// haversineKm returns the great-circle distance between two lat/lng points,
// in kilometers. Mirrors map.html's haversine() exactly.
func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Pow(math.Sin(dLat/2), 2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*math.Pow(math.Sin(dLon/2), 2)
	return r * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// toXYkm projects (lat,lng) to a local flat equirectangular (x,y) in km
// around refLat — mirrors map.html's toXYkm() exactly (same flat-earth
// approximation, accurate at road/corridor scale).
func toXYkm(lat, lng, refLat float64) (x, y float64) {
	return lng * 111.32 * math.Cos(refLat*math.Pi/180), lat * 111.32
}

// projectOnSegmentKm returns the perpendicular distance (km) from point
// (px,py) to segment [(ax,ay),(bx,by)], all in local km coordinates.
// Mirrors map.html's projectOnSegmentKm() exactly.
func projectOnSegmentKm(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	len2 := dx*dx + dy*dy
	t := 0.0
	if len2 > 0 {
		t = ((px-ax)*dx + (py-ay)*dy) / len2
	}
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}

// nearestOnPolylineKm returns the perpendicular distance (km) from
// (lat,lng) to the closest point on the [lng,lat] polyline line. Mirrors
// map.html's nearestOnPolyline() (distance component only — the along-
// route ordering it also computes has no use server-side).
func nearestOnPolylineKm(lat, lng float64, line [][2]float64) (float64, bool) {
	if len(line) < 2 {
		return 0, false
	}
	refLat := line[0][1]
	px, py := toXYkm(lat, lng, refLat)
	best := math.Inf(1)
	for i := 0; i < len(line)-1; i++ {
		lng1, lat1 := line[i][0], line[i][1]
		lng2, lat2 := line[i+1][0], line[i+1][1]
		ax, ay := toXYkm(lat1, lng1, refLat)
		bx, by := toXYkm(lat2, lng2, refLat)
		dist := projectOnSegmentKm(px, py, ax, ay, bx, by)
		if dist < best {
			best = dist
		}
	}
	return best, true
}

// Match is one Feature found within its own RadiusM of a queried point.
type Match struct {
	Feature   *Feature
	Layer     *Layer
	DistanceM float64
}

// MatchNear returns every feature, across every layer, whose RadiusM
// contains (lat,lng) — point features via haversineKm, segment features via
// nearestOnPolylineKm, exactly like the existing camera radius/route tools.
func MatchNear(lat, lng float64) []Match {
	var out []Match
	for _, f := range ListFeatures("") {
		if f.RadiusM <= 0 {
			continue
		}
		var distKm float64
		switch f.Kind {
		case FeatureKindPoint:
			distKm = haversineKm(lat, lng, f.Lat, f.Lng)
		case FeatureKindSegment:
			d, ok := nearestOnPolylineKm(lat, lng, f.Line)
			if !ok {
				continue
			}
			distKm = d
		default:
			continue
		}
		distM := distKm * 1000
		if distM > f.RadiusM {
			continue
		}
		l, ok := GetLayer(f.LayerID)
		if !ok {
			continue
		}
		out = append(out, Match{Feature: f, Layer: l, DistanceM: distM})
	}
	return out
}

// CongestionRiskMatch returns the first feature (across every layer, in
// MatchNear's order) whose layer has FlagsIncidentCongestionRisk set, for
// (lat,lng) — the server-side source of truth behind Incident.
// CongestionRiskPoint auto-detection. map.html's incident-add form calls
// the /api/gis/congestion-check endpoint built on this (see api.go) instead
// of running this check against its own client-side _gisLayers, since
// _gisLayers only holds whatever GIS layers the CURRENT user happens to
// have view permission for — whether an incident sits near a "28 điểm nguy
// cơ ùn tắc" point/segment is meant to be the same answer for every user,
// not something that silently stops working for a user who (now, or under
// a future per-layer permission model) can't see that specific layer.
func CongestionRiskMatch(lat, lng float64) (Match, bool) {
	for _, m := range MatchNear(lat, lng) {
		if m.Layer.FlagsIncidentCongestionRisk {
			return m, true
		}
	}
	return Match{}, false
}

// MatchesCongestionRisk reports whether (lat,lng) falls within any feature
// belonging to a layer with FlagsIncidentCongestionRisk set.
func MatchesCongestionRisk(lat, lng float64) bool {
	_, ok := CongestionRiskMatch(lat, lng)
	return ok
}
