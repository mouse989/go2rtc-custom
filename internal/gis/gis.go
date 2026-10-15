// Package gis manages admin-defined "GIS data layers" shown on the Map
// page: named collections of point/segment features (e.g. "28 điểm nguy cơ
// ùn tắc", and later "Đèn giao thông", "Bảng VMS"...) that are purely
// reference geometry, distinct from the live/operational data feeds
// (traffic events, AI events, traffic-flow sensors) which already have
// their own dedicated subsystems.
//
// internal/auth's Region ("địa bàn") module is deliberately NOT folded into
// this package even though it's conceptually the same kind of data
// (admin-drawn geometry shown on the map) — Region is deeply wired into the
// user-permission system (User.RegionIDs, camera visibility) and has an
// in-flight buffer-zone feature; migrating its storage here would risk
// that working system for no functional gain. The Map page's UI still
// presents both under one "Dữ liệu GIS" menu group — see map.html — but
// the two keep independent backends.
//
// A Layer's features can be either a POINT (a lat/lng + a circular radius)
// or a SEGMENT (a polyline + a corridor half-width) — same two shapes used
// elsewhere on the map (Search area / Theo tuyến), reusing their exact
// proximity math (see proximity.go) so "is this incident near a congestion
// point or segment" behaves identically to the existing camera searches.
package gis

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/rs/zerolog"
)

// FeatureKind selects which geometry a Feature carries.
type FeatureKind string

const (
	FeatureKindPoint   FeatureKind = "point"
	FeatureKindSegment FeatureKind = "segment"
)

// Layer is one named GIS data layer (e.g. "28 điểm nguy cơ ùn tắc").
type Layer struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color,omitempty"` // map display color, e.g. "#f59e0b"
	Icon  string `json:"icon,omitempty"`  // display emoji/icon, e.g. "⚠️"
	// DefaultOn: shown on the map by default for users who haven't toggled
	// it themselves yet (per-browser toggle state lives in the browser's
	// own localStorage, same convention as every other map layer).
	DefaultOn bool `json:"default_on"`
	// FlagsIncidentCongestionRisk: when true, this layer's features feed
	// the "Thêm sự cố" form's "⚠️ Thuộc 28 vị trí có nguy cơ ùn tắc" auto-
	// tick (see map.html's incident-modal wiring). A layer-level flag
	// rather than a hardcoded layer name/ID so the admin can designate
	// whichever layer should drive that check (and so re-purposing it
	// later, or adding a second such layer, needs no code change).
	FlagsIncidentCongestionRisk bool      `json:"flags_incident_congestion_risk,omitempty"`
	CreatedAt                   time.Time `json:"created_at"`
	UpdatedAt                   time.Time `json:"updated_at"`
}

// Feature is one geometry entry within a Layer — either a point (Lat/Lng +
// RadiusM circle) or a segment (Line polyline + RadiusM corridor half-
// width), per Kind.
type Feature struct {
	ID      string      `json:"id"`
	LayerID string      `json:"layer_id"`
	Label   string      `json:"label"`
	Kind    FeatureKind `json:"kind"`

	// Point fields (Kind == FeatureKindPoint).
	Lat float64 `json:"lat,omitempty"`
	Lng float64 `json:"lng,omitempty"`

	// Line fields (Kind == FeatureKindSegment) — [lng,lat] pairs, GeoJSON
	// coordinate order, matching Region.Ring's convention.
	Line [][2]float64 `json:"line,omitempty"`

	// RadiusM is the circle radius (point) or corridor half-width
	// (segment), in meters — "lấy theo bán kính: hiện nay từ điểm, hoặc
	// đoạn đường" per the admin's own spec.
	RadiusM float64 `json:"radius_m"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

var log zerolog.Logger

var (
	mu       sync.RWMutex
	layers   = map[string]*Layer{}
	features = map[string]*Feature{} // by feature ID
	dataDir  string
)

// Init loads (or initializes) the GIS data store. Call once at startup.
func Init() {
	log = app.GetLogger("gis")

	dataDir = "gis_data"
	if app.ConfigPath != "" {
		dataDir = filepath.Join(filepath.Dir(app.ConfigPath), "gis_data")
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Error().Err(err).Msg("[gis] cannot create data dir")
	}

	loadLocked()
	registerHandlers()

	log.Info().Str("dir", dataDir).Int("layers", len(layers)).Int("features", len(features)).Msg("[gis] ready")
}

func layersPath() string   { return filepath.Join(dataDir, "layers.json") }
func featuresPath() string { return filepath.Join(dataDir, "features.json") }

func loadLocked() {
	mu.Lock()
	defer mu.Unlock()

	if data, err := os.ReadFile(layersPath()); err == nil {
		var list []*Layer
		if json.Unmarshal(data, &list) == nil {
			for _, l := range list {
				layers[l.ID] = l
			}
		}
	}
	if data, err := os.ReadFile(featuresPath()); err == nil {
		var list []*Feature
		if json.Unmarshal(data, &list) == nil {
			for _, f := range list {
				features[f.ID] = f
			}
		}
	}
}

func saveLayersLocked() error {
	list := make([]*Layer, 0, len(layers))
	for _, l := range layers {
		list = append(list, l)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(layersPath(), data, 0644)
}

func saveFeaturesLocked() error {
	list := make([]*Feature, 0, len(features))
	for _, f := range features {
		list = append(list, f)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Label < list[j].Label })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(featuresPath(), data, 0644)
}

func newID(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

// ── Layers ───────────────────────────────────────────────────────────

// ListLayers returns all layers sorted by name (defensive copies).
func ListLayers() []*Layer {
	mu.RLock()
	defer mu.RUnlock()
	list := make([]*Layer, 0, len(layers))
	for _, l := range layers {
		cp := *l
		list = append(list, &cp)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

// GetLayer returns a copy of the layer with the given ID.
func GetLayer(id string) (*Layer, bool) {
	mu.RLock()
	defer mu.RUnlock()
	l, ok := layers[id]
	if !ok {
		return nil, false
	}
	cp := *l
	return &cp, true
}

// CreateLayer adds a new layer.
func CreateLayer(l *Layer) error {
	now := time.Now()
	stored := *l
	stored.ID = newID("layer")
	stored.CreatedAt = now
	stored.UpdatedAt = now

	mu.Lock()
	layers[stored.ID] = &stored
	err := saveLayersLocked()
	mu.Unlock()

	*l = stored
	return err
}

// UpdateLayer updates an existing layer's editable fields.
func UpdateLayer(id string, upd *Layer) (*Layer, error) {
	mu.Lock()
	defer mu.Unlock()
	existing, ok := layers[id]
	if !ok {
		return nil, errNotFound
	}
	stored := *upd
	stored.ID = id
	stored.CreatedAt = existing.CreatedAt
	stored.UpdatedAt = time.Now()
	layers[id] = &stored
	if err := saveLayersLocked(); err != nil {
		return nil, err
	}
	cp := stored
	return &cp, nil
}

// DeleteLayer removes a layer and every feature inside it.
func DeleteLayer(id string) error {
	mu.Lock()
	defer mu.Unlock()
	if _, ok := layers[id]; !ok {
		return errNotFound
	}
	delete(layers, id)
	for fid, f := range features {
		if f.LayerID == id {
			delete(features, fid)
		}
	}
	if err := saveLayersLocked(); err != nil {
		return err
	}
	return saveFeaturesLocked()
}

// ── Features ─────────────────────────────────────────────────────────

// ListFeatures returns every feature in layerID, sorted by label. layerID
// == "" returns every feature across all layers.
func ListFeatures(layerID string) []*Feature {
	mu.RLock()
	defer mu.RUnlock()
	list := make([]*Feature, 0, len(features))
	for _, f := range features {
		if layerID != "" && f.LayerID != layerID {
			continue
		}
		cp := *f
		list = append(list, &cp)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Label < list[j].Label })
	return list
}

// GetFeature returns a copy of the feature with the given ID.
func GetFeature(id string) (*Feature, bool) {
	mu.RLock()
	defer mu.RUnlock()
	f, ok := features[id]
	if !ok {
		return nil, false
	}
	cp := *f
	return &cp, true
}

// CreateFeature adds a new feature to an existing layer.
func CreateFeature(f *Feature) error {
	mu.Lock()
	if _, ok := layers[f.LayerID]; !ok {
		mu.Unlock()
		return errLayerNotFound
	}
	now := time.Now()
	stored := *f
	stored.ID = newID("feat")
	stored.CreatedAt = now
	stored.UpdatedAt = now
	features[stored.ID] = &stored
	err := saveFeaturesLocked()
	mu.Unlock()

	*f = stored
	return err
}

// UpdateFeature updates an existing feature's editable fields.
func UpdateFeature(id string, upd *Feature) (*Feature, error) {
	mu.Lock()
	defer mu.Unlock()
	existing, ok := features[id]
	if !ok {
		return nil, errNotFound
	}
	if _, ok := layers[upd.LayerID]; !ok {
		return nil, errLayerNotFound
	}
	stored := *upd
	stored.ID = id
	stored.CreatedAt = existing.CreatedAt
	stored.UpdatedAt = time.Now()
	features[id] = &stored
	if err := saveFeaturesLocked(); err != nil {
		return nil, err
	}
	cp := stored
	return &cp, nil
}

// DeleteFeature removes a feature by ID.
func DeleteFeature(id string) error {
	mu.Lock()
	defer mu.Unlock()
	if _, ok := features[id]; !ok {
		return errNotFound
	}
	delete(features, id)
	return saveFeaturesLocked()
}

type gisError string

func (e gisError) Error() string { return string(e) }

const (
	errNotFound      = gisError("not found")
	errLayerNotFound = gisError("layer not found")
)
