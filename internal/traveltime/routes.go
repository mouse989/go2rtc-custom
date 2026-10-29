package traveltime

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Route describes a named road segment to monitor.
type Route struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Origin      string `json:"origin"`              // "lat,lng"
	Destination string `json:"destination"`         // "lat,lng"
	Waypoints   string `json:"waypoints,omitempty"` // "lat,lng|lat,lng|..."
	// Region/RoadType are optional classification tags — "" means chưa
	// phân loại, same as every route created before these fields existed.
	// Region must be one of ListRegions() (see regions.go), RoadType one
	// of RoadTypes — validated in the HTTP handler (api.go), not here, so
	// createRoute/updateRoute themselves stay agnostic to what's "valid"
	// the same way they already are about every other field.
	Region   string `json:"region,omitempty"`
	RoadType string `json:"roadType,omitempty"`
}

type routeStore struct {
	mu     sync.RWMutex
	path   string
	routes []*Route
}

var rstore *routeStore

func initRoutes(path string) {
	rstore = &routeStore{path: path}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &rstore.routes)
	}
	if rstore.routes == nil {
		rstore.routes = []*Route{}
	}
}

func listRoutes() []*Route {
	rstore.mu.RLock()
	defer rstore.mu.RUnlock()
	out := make([]*Route, len(rstore.routes))
	for i, r := range rstore.routes {
		cp := *r
		out[i] = &cp
	}
	return out
}

// ListRoutes is listRoutes' exported form — for other packages
// (internal/trafficinsight) that need each route's Region/RoadType to
// filter/group the city-wide TTI trend by it, called directly rather than
// through this package's own (admin-only) HTTP endpoint, same principle
// trafficinsight already follows for traffic.PersistentPointsInRange/
// incidents.List.
func ListRoutes() []*Route {
	return listRoutes()
}

// CreateRoute is createRoute's exported form — currently only used by
// internal/trafficinsight's own tests, which need routes with known
// IDs/Region/RoadType actually present in this package's store to verify
// the Region/RoadType filter end-to-end against ListRoutes() above
// (writeTravelTimeDay there writes raw log fixtures directly and has no
// other way to seed a matching route).
func CreateRoute(r *Route) (*Route, error) {
	return createRoute(r)
}

func createRoute(r *Route) (*Route, error) {
	rstore.mu.Lock()
	defer rstore.mu.Unlock()
	cp := *r
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	cp.ID = "r_" + hex.EncodeToString(b)
	rstore.routes = append(rstore.routes, &cp)
	if err := rstore.saveLocked(); err != nil {
		return nil, err
	}
	ret := cp
	return &ret, nil
}

func updateRoute(r *Route) error {
	rstore.mu.Lock()
	defer rstore.mu.Unlock()
	for i, e := range rstore.routes {
		if e.ID == r.ID {
			cp := *r
			rstore.routes[i] = &cp
			return rstore.saveLocked()
		}
	}
	return fmt.Errorf("route %q not found", r.ID)
}

func reorderRoutes(ids []string) error {
	rstore.mu.Lock()
	defer rstore.mu.Unlock()
	idx := make(map[string]*Route, len(rstore.routes))
	for _, r := range rstore.routes {
		idx[r.ID] = r
	}
	seen := make(map[string]bool)
	ordered := make([]*Route, 0, len(rstore.routes))
	for _, id := range ids {
		if r, ok := idx[id]; ok && !seen[id] {
			ordered = append(ordered, r)
			seen[id] = true
		}
	}
	for _, r := range rstore.routes {
		if !seen[r.ID] {
			ordered = append(ordered, r)
		}
	}
	rstore.routes = ordered
	return rstore.saveLocked()
}

func deleteRoute(id string) error {
	rstore.mu.Lock()
	defer rstore.mu.Unlock()
	for i, r := range rstore.routes {
		if r.ID == id {
			rstore.routes = append(rstore.routes[:i], rstore.routes[i+1:]...)
			return rstore.saveLocked()
		}
	}
	return fmt.Errorf("route %q not found", id)
}

func (s *routeStore) saveLocked() error {
	data, err := json.MarshalIndent(s.routes, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}
