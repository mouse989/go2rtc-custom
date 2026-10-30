package traveltime

// regions.go — the admin-managed list of "khu vực" (region/district) names
// a route can be tagged with (Route.Region, see routes.go), e.g. "Quận 1",
// "TP. Thủ Đức". A plain name list, not polygons — unlike
// internal/traffic's Config.Regions (drawn map areas used to tag jam
// points), this is just a controlled vocabulary so a route's Region field
// can't fragment into near-duplicate free-text spellings, which is the
// whole point of letting internal/trafficinsight later filter/group the
// city-wide TTI trend by region reliably.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// RoadTypes is the fixed set of road-type values a route may be tagged
// with (Route.RoadType) — deliberately NOT admin-configurable, unlike
// Region above: there are few of these, they rarely change, and keeping
// them fixed means every page that groups/colors by road type can rely on
// the same small set rather than handling an arbitrary admin-grown list.
var RoadTypes = []string{"Đường đô thị", "Đường trục chính", "Đường vành đai", "Quốc lộ", "Cao tốc"}

// IsValidRoadType reports whether v is one of RoadTypes, or empty
// ("chưa phân loại" — always allowed, every route starts this way).
func IsValidRoadType(v string) bool {
	if v == "" {
		return true
	}
	for _, t := range RoadTypes {
		if t == v {
			return true
		}
	}
	return false
}

type regionStore struct {
	mu      sync.RWMutex
	path    string
	regions []string
}

var rgstore *regionStore

func initRegions(path string) {
	rgstore = &regionStore{path: path}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &rgstore.regions)
	}
	if rgstore.regions == nil {
		rgstore.regions = []string{}
	}
}

// ListRegions returns the current admin-managed region list, in the order
// they were saved.
func ListRegions() []string {
	rgstore.mu.RLock()
	defer rgstore.mu.RUnlock()
	out := make([]string, len(rgstore.regions))
	copy(out, rgstore.regions)
	return out
}

// IsValidRegion reports whether v is one of ListRegions(), or empty
// ("chưa phân loại" — always allowed).
func IsValidRegion(v string) bool {
	if v == "" {
		return true
	}
	rgstore.mu.RLock()
	defer rgstore.mu.RUnlock()
	for _, r := range rgstore.regions {
		if r == v {
			return true
		}
	}
	return false
}

// SetRegions replaces the whole region list — trims whitespace, drops
// empties and duplicates (first occurrence wins, order otherwise kept),
// same "edit the whole list at once" convention as the rest of this
// codebase's small admin-managed lists (e.g. internal/incidents' category
// icon map). Existing routes already tagged with a name this call drops
// keep that value (Route.Region isn't touched here) — they just read as
// "khu vực đã xoá" in the UI rather than losing the route's own data.
func SetRegions(names []string) error {
	rgstore.mu.Lock()
	defer rgstore.mu.Unlock()

	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(rgstore.path, data, 0644); err != nil {
		return fmt.Errorf("write region list: %w", err)
	}
	rgstore.regions = out
	return nil
}
