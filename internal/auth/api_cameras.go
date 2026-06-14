package auth

// api_cameras.go — public camera list endpoint
//
// GET /api/cameras
//   Returns cameras the caller has access to, with GPS coordinates.
//   No stream URLs are included — safe to expose to map integrations.
//
// Query params:
//   token   = JWT token (alternative to Authorization header)
//   all     = "true" → also include cameras that have no coordinates yet
//   format  = "geojson" → return GeoJSON FeatureCollection instead of plain JSON array
//
// Response (default JSON array):
//   [
//     {
//       "id":    "a3f9c12d8e4b",   // masked camera ID (stable per-server)
//       "label": "Ngã tư An Sương", // human-readable label (may be empty)
//       "lat":   10.8231,
//       "lon":   106.6297
//     },
//     ...
//   ]
//   Admin also receives: "name" (real stream name)
//   Cameras without coordinates are omitted unless ?all=true.
//
// Response (GeoJSON — ?format=geojson):
//   {
//     "type": "FeatureCollection",
//     "features": [
//       {
//         "type": "Feature",
//         "geometry": { "type": "Point", "coordinates": [106.6297, 10.8231] },
//         "properties": { "id": "a3f9c12d8e4b", "label": "Ngã tư An Sương" }
//       }
//     ]
//   }

import (
	"encoding/json"
	"net/http"
	"sort"
)

func registerCamerasHandler() {
	http.HandleFunc("/api/cameras", camerasHandler)
}

// camerasHandler  GET /api/cameras
func camerasHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	includeAll := r.URL.Query().Get("all") == "true"
	isAdmin := user.Role == RoleAdmin

	// ── Build lookup: streamName → location ──────────────────────────
	locMu.RLock()
	locByName := make(map[string]*CameraLocation, len(locMap))
	for name, loc := range locMap {
		locByName[name] = loc
	}
	locMu.RUnlock()

	// ── Collect all stream names the user can access ──────────────────
	// We read from streamIDMap (populated by RegisterStreamID) which contains
	// every stream known to the server.
	streamIDMu.RLock()
	allStreams := make([]struct{ id, name string }, 0, len(streamIDMap))
	for id, name := range streamIDMap {
		allStreams = append(allStreams, struct{ id, name string }{id, name})
	}
	streamIDMu.RUnlock()

	// ── Camera response item ──────────────────────────────────────────
	type cameraItem struct {
		ID    string   `json:"id"`
		Name  string   `json:"name,omitempty"`  // admin only
		Label string   `json:"label,omitempty"`
		Lat   *float64 `json:"lat"`             // null when no coordinates
		Lon   *float64 `json:"lon"`             // null when no coordinates
	}

	result := make([]cameraItem, 0, len(allStreams))
	for _, s := range allStreams {
		// Permission check
		if !isAdmin && !contains(user.Streams, s.name) {
			continue
		}

		loc := locByName[s.name]
		if loc == nil && !includeAll {
			continue // skip cameras without coordinates unless ?all=true
		}

		item := cameraItem{ID: s.id}
		if isAdmin {
			item.Name = s.name
		}
		if loc != nil {
			lat, lon := loc.Lat, loc.Lon
			item.Lat = &lat
			item.Lon = &lon
			item.Label = loc.Label
		}
		result = append(result, item)
	}

	// Stable sort: cameras with coordinates first (sorted by label/name),
	// then cameras without coordinates.
	sort.SliceStable(result, func(i, j int) bool {
		hasI := result[i].Lat != nil
		hasJ := result[j].Lat != nil
		if hasI != hasJ {
			return hasI // located cameras first
		}
		labelI := result[i].Label
		if labelI == "" {
			labelI = result[i].Name
		}
		labelJ := result[j].Label
		if labelJ == "" {
			labelJ = result[j].Name
		}
		return labelI < labelJ
	})

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")

	// ── GeoJSON format ────────────────────────────────────────────────
	if r.URL.Query().Get("format") == "geojson" {
		type geometry struct {
			Type        string    `json:"type"`
			Coordinates []float64 `json:"coordinates"`
		}
		type properties struct {
			ID    string `json:"id"`
			Name  string `json:"name,omitempty"`
			Label string `json:"label,omitempty"`
		}
		type feature struct {
			Type       string     `json:"type"`
			Geometry   *geometry  `json:"geometry"`
			Properties properties `json:"properties"`
		}
		type featureCollection struct {
			Type     string    `json:"type"`
			Features []feature `json:"features"`
		}

		fc := featureCollection{Type: "FeatureCollection", Features: []feature{}}
		for _, cam := range result {
			var geom *geometry
			if cam.Lat != nil && cam.Lon != nil {
				geom = &geometry{
					Type:        "Point",
					Coordinates: []float64{*cam.Lon, *cam.Lat}, // GeoJSON: [lon, lat]
				}
			}
			fc.Features = append(fc.Features, feature{
				Type:     "Feature",
				Geometry: geom,
				Properties: properties{
					ID:    cam.ID,
					Name:  cam.Name,
					Label: cam.Label,
				},
			})
		}
		_ = json.NewEncoder(w).Encode(fc)
		return
	}

	// ── Default JSON array ────────────────────────────────────────────
	if result == nil {
		result = []cameraItem{}
	}
	_ = json.NewEncoder(w).Encode(result)
}
