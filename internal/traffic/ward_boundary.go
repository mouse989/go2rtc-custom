package traffic

// ward_boundary.go — admin-imported administrative boundary polygons for
// Ho Chi Minh City's 168 phường/xã (wards/communes, as of the 1/7/2025
// province/ward merger) — www/tti-suggest.html's "Lọc theo ranh giới"
// filter: restrict the hotspot list to only candidates whose own
// representative point falls inside one or more selected wards, instead
// of always scanning across whatever area Flow Capture happens to cover.
//
// Imported wholesale from an admin-supplied KML file, parsed CLIENT-SIDE
// in tti-suggest.html's own JS — same convention internal/auth's Region
// KML importer already uses in map.html (a browser's DOMParser handles an
// 8MB+ KML/168-Placemark file fine, and this avoids writing a second Go
// KML/Polygon parser next to internal/gis/kml.go's existing
// Point/LineString-only one). The whole parsed list is replaced in one
// bulk request (see ward_boundary_api.go) — there is no incremental
// per-ward add/edit here, only "re-import the whole dataset".
//
// Point-in-polygon testing reuses monitor.go's existing pointInPolygon
// (ray-casting, coords[i] = [lat, lon]) — Ring below uses that SAME
// [lat,lng] convention, so no coordinate-order conversion happens
// anywhere along this path (the client-side KML parser is the one place
// that flips KML's own lng,lat order to match it before sending here).

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Ring is one closed boundary ring, [lat,lng] pairs, open (first point
// not repeated at the end) — same shape pointInPolygon already expects.
type Ring [][2]float64

// WardBoundary is one phường/xã's own administrative boundary. Rings has
// more than one entry when the KML source described the ward as a
// MultiGeometry (e.g. a mainland piece plus a small river island) — a
// point counts as inside the ward if it falls within ANY one of them.
//
// minLat/maxLat/minLng/maxLng is this ward's own bounding box, filled in
// by computeBBox right after load/import — pointInWards' own cheap
// reject before the expensive ray-casting test (see its doc comment for
// why this matters once "Toàn TP.HCM (168 đơn vị)" selects every ward at
// once). Unexported, so encoding/json silently skips it on both
// Marshal/Unmarshal — recomputed from Rings every time, never persisted.
type WardBoundary struct {
	ID    string `json:"id"`
	Name  string `json:"name"` // "An Đông"
	Type  string `json:"type"` // "Phường" | "Xã" | "" if unknown
	Rings []Ring `json:"rings"`

	minLat, maxLat, minLng, maxLng float64
}

// computeBBox fills in w's own bounding box from its current Rings —
// called once right after a ward is loaded from disk or freshly
// imported, never per point-in-wards query.
func (w *WardBoundary) computeBBox() {
	w.minLat, w.minLng = math.Inf(1), math.Inf(1)
	w.maxLat, w.maxLng = math.Inf(-1), math.Inf(-1)
	for _, ring := range w.Rings {
		for _, p := range ring {
			lat, lng := p[0], p[1]
			if lat < w.minLat {
				w.minLat = lat
			}
			if lat > w.maxLat {
				w.maxLat = lat
			}
			if lng < w.minLng {
				w.minLng = lng
			}
			if lng > w.maxLng {
				w.maxLng = lng
			}
		}
	}
}

var (
	wardMu    sync.RWMutex
	wards     []*WardBoundary
	wardsFile string
)

func initWardBoundaries(dir string) {
	wardsFile = filepath.Join(dir, "ward_boundaries.json")

	wardMu.Lock()
	defer wardMu.Unlock()
	data, err := os.ReadFile(wardsFile)
	if err != nil {
		wards = []*WardBoundary{}
		return
	}
	var loaded []*WardBoundary
	if err := json.Unmarshal(data, &loaded); err != nil {
		log.Warn().Err(err).Msg("[traffic] ward_boundaries.json parse error, starting empty")
		wards = []*WardBoundary{}
		return
	}
	for _, w := range loaded {
		w.computeBBox()
	}
	wards = loaded
}

// listWardBoundaries returns every imported ward (ID/Name/Type — not the
// potentially large Rings data), for the filter UI's picker list.
func listWardBoundaries() []*WardBoundary {
	wardMu.RLock()
	defer wardMu.RUnlock()
	out := make([]*WardBoundary, len(wards))
	copy(out, wards)
	return out
}

// replaceWardBoundaries wholesale-replaces the stored ward list (one KML
// import) and persists it to disk. IDs are assigned here from each
// ward's own Name (stable across re-imports of the same dataset, unlike
// an index-based ID, so a filter selection saved in a browser/elsewhere
// by ward ID keeps meaning the same ward after a re-import) — a
// duplicate or empty Name falls back to a positional ID so the import
// never silently drops a ward.
func replaceWardBoundaries(next []*WardBoundary) error {
	seen := map[string]bool{}
	for i, w := range next {
		id := wardSlug(w.Name)
		if id == "" || seen[id] {
			id = fmt.Sprintf("w%03d", i)
		}
		seen[id] = true
		w.ID = id
		w.computeBBox()
	}

	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal ward boundaries: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(wardsFile), 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(wardsFile), err)
	}
	if err := os.WriteFile(wardsFile, data, 0644); err != nil {
		return fmt.Errorf("write %s: %w", wardsFile, err)
	}

	wardMu.Lock()
	wards = next
	wardMu.Unlock()
	return nil
}

// stripDiacritics removes Vietnamese accent marks, mapping each letter to
// its unaccented Latin base ("Đông" -> "Dong"). NFD decomposition peels
// off every COMBINING accent (the circumflex/breve/horn marks on
// ă/â/ê/ô/ơ/ư and the tone marks), but Vietnamese "đ"/"Đ" is its own
// precomposed codepoint rather than "d"/"D" plus a combining mark, so it
// survives NFD untouched — handled with an explicit replacement first.
func stripDiacritics(s string) string {
	s = strings.NewReplacer("đ", "d", "Đ", "D").Replace(s)
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	result, _, err := transform.String(t, s)
	if err != nil {
		return s
	}
	return result
}

// wardSlug turns a ward's display name into a short, stable, URL/JSON-
// value-safe ID — lowercased, Vietnamese diacritics stripped to their
// base Latin letter, anything else collapsed to '-'. Good enough for a
// stable key; not meant to be a full-fidelity transliteration.
func wardSlug(name string) string {
	s := stripDiacritics(name)
	out := make([]byte, 0, len(s))
	lastDash := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			out = append(out, c)
			lastDash = false
		case c >= 'A' && c <= 'Z':
			out = append(out, c-'A'+'a')
			lastDash = false
		default:
			if !lastDash && len(out) > 0 {
				out = append(out, '-')
				lastDash = true
			}
		}
	}
	for len(out) > 0 && out[len(out)-1] == '-' {
		out = out[:len(out)-1]
	}
	return string(out)
}

// wardsByIDs returns the currently-imported wards whose ID is in ids, in
// no particular order. A caller passing a nil/empty ids gets nil back
// (no wards selected) — the caller is the one that decides what "no
// selection" means (see pointInWards' own doc comment: it treats an
// EMPTY selected-wards slice as "no filter", which only makes sense when
// the frontend's "Không giới hạn" option is what produced that empty
// list — the frontend's "Toàn TP.HCM" option must instead pass every
// known ward ID explicitly).
func wardsByIDs(ids []string) []*WardBoundary {
	if len(ids) == 0 {
		return nil
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var out []*WardBoundary
	for _, w := range listWardBoundaries() {
		if want[w.ID] {
			out = append(out, w)
		}
	}
	return out
}

// pointInWards reports whether (lat,lng) falls inside any ring of any of
// the given wards. An empty/nil selectedWards means "no boundary filter
// at all" — every point passes — which is the correct behavior for
// www/tti-suggest.html's "Không giới hạn" choice, but NOT for its "Toàn
// TP.HCM (168 đơn vị)" choice: that one must pass the full ward list
// through wardsByIDs so a point outside every imported ward (e.g. a scan
// from a region that extends past the city's own administrative
// boundary) is correctly excluded rather than trivially passing.
func pointInWards(lat, lng float64, selectedWards []*WardBoundary) bool {
	if len(selectedWards) == 0 {
		return true
	}
	for _, w := range selectedWards {
		// Cheap bounding-box reject before the expensive ray-casting
		// test below. "Toàn TP.HCM (168 đơn vị)" selects every imported
		// ward at once — over 220K boundary points combined — so
		// walking every ring of every ward for every flow_segments row
		// (computeHotspots' own hot loop) was the actual slow part
		// ("dò từng phường xã"). A point's own bbox rejects it against
		// all but a small handful of wards (ordinarily just the one
		// it's actually inside, plus maybe an immediate neighbor at a
		// shared border) before any ring is ever walked — same exact
		// result as before, just without testing 167 wards' full
		// polygons that could never have matched in the first place.
		if lat < w.minLat || lat > w.maxLat || lng < w.minLng || lng > w.maxLng {
			continue
		}
		for _, ring := range w.Rings {
			if pointInPolygon(lat, lng, ring) {
				return true
			}
		}
	}
	return false
}
