package auth

// locations.go — camera GPS location store
//
// Saved to camera_locations.json (same directory as users.json).
// Viewers can read locations for cameras they have access to.
// Only admins can write.

import (
	"encoding/json"
	"math"
	"net/http"
	"os"
	"sync"
)

// CameraLocation holds GPS coordinates for a stream.
type CameraLocation struct {
	Name  string  `json:"name"`
	Lat   float64 `json:"lat"`
	Lon   float64 `json:"lon"`
	Label string  `json:"label,omitempty"`
}

var (
	locMu   sync.RWMutex
	locMap  = map[string]*CameraLocation{}
	locFile string
)

func initLocations(path string) error {
	locFile = path
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil // first run — no file yet
	}
	if err != nil {
		return err
	}
	var locs []*CameraLocation
	if err := json.Unmarshal(data, &locs); err != nil {
		return err
	}
	locMu.Lock()
	for _, loc := range locs {
		if loc.Name != "" {
			locMap[loc.Name] = loc
		}
	}
	locMu.Unlock()
	return nil
}

func saveLocations() error {
	locMu.RLock()
	locs := make([]*CameraLocation, 0, len(locMap))
	for _, loc := range locMap {
		locs = append(locs, loc)
	}
	locMu.RUnlock()

	data, err := json.MarshalIndent(locs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(locFile, data, 0644)
}

func registerLocationHandlers() {
	http.HandleFunc("/api/camera-locations", locationHandler)
}

// locationHandler  GET/POST/DELETE  /api/camera-locations
func locationHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	// ── GET ──────────────────────────────────────────────────────────
	case http.MethodGet:
		locMu.RLock()
		type entry struct {
			ID    string  `json:"id"`
			Lat   float64 `json:"lat"`
			Lon   float64 `json:"lon"`
			Label string  `json:"label,omitempty"`
			Name  string  `json:"name,omitempty"` // admin only
		}
		result := make([]entry, 0, len(locMap))
		for _, loc := range locMap {
			if user.Role != RoleAdmin && !contains(user.Streams, loc.Name) {
				continue
			}
			e := entry{
				ID:    StreamID(loc.Name),
				Lat:   loc.Lat,
				Lon:   loc.Lon,
				Label: loc.Label,
			}
			if user.Role == RoleAdmin {
				e.Name = loc.Name
			}
			result = append(result, e)
		}
		locMu.RUnlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)

	// ── POST / PUT ────────────────────────────────────────────────────
	case http.MethodPost, http.MethodPut:
		if user.Role != RoleAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var loc CameraLocation
		if err := json.NewDecoder(r.Body).Decode(&loc); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if loc.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		locMu.Lock()
		locMap[loc.Name] = &loc
		locMu.Unlock()
		if err := saveLocations(); err != nil {
			http.Error(w, "save failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":  StreamID(loc.Name),
			"lat": loc.Lat, "lon": loc.Lon, "label": loc.Label,
			"name": loc.Name,
		})

	// ── DELETE ────────────────────────────────────────────────────────
	case http.MethodDelete:
		if user.Role != RoleAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		locMu.Lock()
		delete(locMap, name)
		locMu.Unlock()
		_ = saveLocations()
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HaversineKm returns the great-circle distance in km between two points.
func HaversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}
