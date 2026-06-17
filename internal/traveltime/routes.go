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
