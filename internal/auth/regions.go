package auth

// Region ("địa bàn") is a named, admin-drawn territorial area used to grant
// a viewer access to every camera whose location falls inside it, instead
// of hand-picking cameras one at a time. A territory is rarely one neat
// shape, so a region can hold several independent polygons — a camera
// counts as "in" the region if it falls inside ANY one of them.
//
// Membership is resolved LIVE against the current camera-location store,
// not snapshotted at assignment time: place a new camera inside an
// existing region's boundary and every user already assigned that region
// sees it immediately, no re-assignment needed. See UserCanAccessStream in
// middleware.go, which ORs this in alongside the explicit User.Streams
// list — regions are an additional grant, never a replacement for it.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"
)

// Ring is a polygon boundary as [lon, lat] points (GeoJSON coordinate
// order, matching what the map draws and sends) — need not repeat the
// first point as the last; pointInRing handles the wraparound edge itself.
type Ring [][2]float64

// Region groups one or more polygons under one name for permission
// assignment (User.RegionIDs) and map display.
type Region struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Color    string `json:"color,omitempty"` // map display color, e.g. "#3b82f6"
	Polygons []Ring `json:"polygons"`
}

type regionStore struct {
	mu      sync.RWMutex
	path    string
	regions map[string]*Region // by ID
}

var rstore *regionStore

func initRegions(path string) error {
	rstore = &regionStore{path: path, regions: make(map[string]*Region)}
	return rstore.load()
}

func (s *regionStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil // no regions yet
	}
	if err != nil {
		return err
	}
	var list []*Region
	if err = json.Unmarshal(data, &list); err != nil {
		return err
	}
	s.regions = make(map[string]*Region, len(list))
	for _, r := range list {
		s.regions[r.ID] = r
	}
	return nil
}

func (s *regionStore) save() error {
	list := make([]*Region, 0, len(s.regions))
	for _, r := range s.regions {
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}

// ListRegions returns all regions sorted by name (defensive copies).
func ListRegions() []*Region {
	rstore.mu.RLock()
	defer rstore.mu.RUnlock()
	list := make([]*Region, 0, len(rstore.regions))
	for _, r := range rstore.regions {
		cp := *r
		list = append(list, &cp)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

// GetRegion returns a copy of the region with the given ID.
func GetRegion(id string) (*Region, bool) {
	rstore.mu.RLock()
	defer rstore.mu.RUnlock()
	r, ok := rstore.regions[id]
	if !ok {
		return nil, false
	}
	cp := *r
	return &cp, true
}

// CreateRegion adds a new region.
func CreateRegion(r *Region) error {
	stored := *r
	rstore.mu.Lock()
	rstore.regions[stored.ID] = &stored
	rstore.mu.Unlock()
	invalidateRegionCameraCache()
	return rstore.save()
}

// UpdateRegion replaces an existing region (creates if not found).
func UpdateRegion(r *Region) error {
	stored := *r
	rstore.mu.Lock()
	rstore.regions[stored.ID] = &stored
	rstore.mu.Unlock()
	invalidateRegionCameraCache()
	return rstore.save()
}

// DeleteRegion removes a region by ID (no error if not found).
func DeleteRegion(id string) error {
	rstore.mu.Lock()
	delete(rstore.regions, id)
	rstore.mu.Unlock()
	invalidateRegionCameraCache()
	return rstore.save()
}

func newRegionID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ── Point-in-polygon ─────────────────────────────────────────────────

// pointInRing reports whether (lon,lat) is inside ring, via the standard
// even-odd ray-casting test.
func pointInRing(lon, lat float64, ring Ring) bool {
	n := len(ring)
	if n < 3 {
		return false
	}
	inside := false
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		xi, yi := ring[i][0], ring[i][1]
		xj, yj := ring[j][0], ring[j][1]
		if (yi > lat) != (yj > lat) {
			slope := (xj - xi) * (lat - yi) / (yj - yi)
			if lon < xi+slope {
				inside = !inside
			}
		}
	}
	return inside
}

// pointInRegion reports whether (lon,lat) falls inside any of r's polygons.
func pointInRegion(lon, lat float64, r *Region) bool {
	for _, ring := range r.Polygons {
		if pointInRing(lon, lat, ring) {
			return true
		}
	}
	return false
}

// ── Region → camera resolution (cached) ───────────────────────────────
// Recomputing ray-casting against every camera on every stream request
// would be wasteful — cache the whole region→cameras map for a few
// seconds, same tradeoff as stream_order.go's streamFileOrder.

const regionCamerasCacheTTL = 5 * time.Second

var (
	regionCamCacheMu sync.Mutex
	regionCamCache   map[string][]string // region ID → camera names
	regionCamCacheAt time.Time
)

func invalidateRegionCameraCache() {
	regionCamCacheMu.Lock()
	regionCamCache = nil
	regionCamCacheMu.Unlock()
}

// camerasInRegion returns the names of every camera whose stored location
// falls inside the given region's polygon(s) — live off camera_locations,
// not a snapshot.
func camerasInRegion(regionID string) []string {
	return regionCameraMap()[regionID]
}

func regionCameraMap() map[string][]string {
	regionCamCacheMu.Lock()
	if regionCamCache != nil && time.Since(regionCamCacheAt) < regionCamerasCacheTTL {
		m := regionCamCache
		regionCamCacheMu.Unlock()
		return m
	}
	regionCamCacheMu.Unlock()

	regions := ListRegions()
	locs := AllCameraLocations()

	fresh := make(map[string][]string, len(regions))
	for _, r := range regions {
		var names []string
		for _, loc := range locs {
			if pointInRegion(loc.Lon, loc.Lat, r) {
				names = append(names, loc.Name)
			}
		}
		fresh[r.ID] = names
	}

	regionCamCacheMu.Lock()
	regionCamCache = fresh
	regionCamCacheAt = time.Now()
	regionCamCacheMu.Unlock()

	return fresh
}

// userStreamInAnyRegion reports whether streamName falls within any region
// assigned to u (u.RegionIDs) — the region-based half of UserCanAccessStream.
func userStreamInAnyRegion(u *User, streamName string) bool {
	if len(u.RegionIDs) == 0 {
		return false
	}
	m := regionCameraMap()
	for _, rid := range u.RegionIDs {
		for _, name := range m[rid] {
			if name == streamName {
				return true
			}
		}
	}
	return false
}
