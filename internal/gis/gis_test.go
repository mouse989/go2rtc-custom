package gis

import "testing"

// setupTestStore resets the package-level store for one test, bypassing
// Init() (which needs app.GetLogger/app.ConfigPath wiring this package's
// tests don't set up) — same "reset globals + point dataDir at a temp dir"
// shape as internal/trafficflow's own test helpers.
func setupTestStore(t *testing.T) {
	mu.Lock()
	layers = map[string]*Layer{}
	features = map[string]*Feature{}
	dataDir = t.TempDir()
	mu.Unlock()
}

func TestCreateLayerAndUpdateDelete(t *testing.T) {
	setupTestStore(t)

	l := &Layer{Name: "28 điểm nguy cơ ùn tắc", Color: "#f59e0b", FlagsIncidentCongestionRisk: true}
	if err := CreateLayer(l); err != nil {
		t.Fatalf("CreateLayer: %v", err)
	}
	if l.ID == "" {
		t.Fatal("expected an ID to be assigned")
	}
	if l.CreatedAt.IsZero() || l.UpdatedAt.IsZero() {
		t.Fatal("expected CreatedAt/UpdatedAt to be set")
	}

	got, ok := GetLayer(l.ID)
	if !ok || got.Name != l.Name {
		t.Fatalf("GetLayer mismatch: %+v", got)
	}

	upd, err := UpdateLayer(l.ID, &Layer{Name: "renamed", Color: "#000000"})
	if err != nil {
		t.Fatalf("UpdateLayer: %v", err)
	}
	if upd.Name != "renamed" || upd.CreatedAt != l.CreatedAt {
		t.Fatalf("UpdateLayer should keep CreatedAt and apply new fields: %+v", upd)
	}

	if err := DeleteLayer(l.ID); err != nil {
		t.Fatalf("DeleteLayer: %v", err)
	}
	if _, ok := GetLayer(l.ID); ok {
		t.Fatal("expected layer to be gone after delete")
	}
}

func TestDeleteLayerCascadesFeatures(t *testing.T) {
	setupTestStore(t)

	l := &Layer{Name: "L1"}
	if err := CreateLayer(l); err != nil {
		t.Fatalf("CreateLayer: %v", err)
	}
	f := &Feature{LayerID: l.ID, Label: "P1", Kind: FeatureKindPoint, Lat: 10.0, Lng: 106.0, RadiusM: 100}
	if err := CreateFeature(f); err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}

	if err := DeleteLayer(l.ID); err != nil {
		t.Fatalf("DeleteLayer: %v", err)
	}
	if _, ok := GetFeature(f.ID); ok {
		t.Fatal("expected feature to be deleted along with its layer")
	}
	if got := ListFeatures(l.ID); len(got) != 0 {
		t.Fatalf("expected no features left for deleted layer, got %d", len(got))
	}
}

func TestCreateFeatureRequiresExistingLayer(t *testing.T) {
	setupTestStore(t)

	f := &Feature{LayerID: "no-such-layer", Label: "P1", Kind: FeatureKindPoint, Lat: 10, Lng: 106, RadiusM: 50}
	if err := CreateFeature(f); err != errLayerNotFound {
		t.Fatalf("expected errLayerNotFound, got %v", err)
	}
}

func TestMatchNearPoint(t *testing.T) {
	setupTestStore(t)

	l := &Layer{Name: "28 điểm", FlagsIncidentCongestionRisk: true}
	if err := CreateLayer(l); err != nil {
		t.Fatalf("CreateLayer: %v", err)
	}
	// Ngã tư Hàng Xanh-ish coordinate, 150m radius.
	f := &Feature{LayerID: l.ID, Label: "Hàng Xanh", Kind: FeatureKindPoint, Lat: 10.8010, Lng: 106.7110, RadiusM: 150}
	if err := CreateFeature(f); err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}

	// ~50m away (tiny lat offset) — should match.
	near := 10.8010 + 0.00045
	if !MatchesCongestionRisk(near, 106.7110) {
		t.Fatal("expected a point ~50m away to match within a 150m radius")
	}

	// ~1.1km away — should not match.
	far := 10.8010 + 0.01
	if MatchesCongestionRisk(far, 106.7110) {
		t.Fatal("expected a point ~1.1km away NOT to match within a 150m radius")
	}
}

func TestMatchNearSegment(t *testing.T) {
	setupTestStore(t)

	l := &Layer{Name: "Đoạn đường", FlagsIncidentCongestionRisk: true}
	if err := CreateLayer(l); err != nil {
		t.Fatalf("CreateLayer: %v", err)
	}
	// A short straight segment running east-west.
	line := [][2]float64{{106.700, 10.800}, {106.710, 10.800}}
	f := &Feature{LayerID: l.ID, Label: "QL13", Kind: FeatureKindSegment, Line: line, RadiusM: 100}
	if err := CreateFeature(f); err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}

	// A point just off the segment's midpoint, well within 100m.
	if !MatchesCongestionRisk(10.8005, 106.705) {
		t.Fatal("expected a point ~55m from the segment to match within a 100m radius")
	}

	// A point far north of the segment.
	if MatchesCongestionRisk(10.820, 106.705) {
		t.Fatal("expected a point ~2.2km from the segment NOT to match within a 100m radius")
	}
}

func TestMatchesCongestionRiskIgnoresUnflaggedLayers(t *testing.T) {
	setupTestStore(t)

	l := &Layer{Name: "Đèn giao thông", FlagsIncidentCongestionRisk: false}
	if err := CreateLayer(l); err != nil {
		t.Fatalf("CreateLayer: %v", err)
	}
	f := &Feature{LayerID: l.ID, Label: "P1", Kind: FeatureKindPoint, Lat: 10.8, Lng: 106.7, RadiusM: 500}
	if err := CreateFeature(f); err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}

	if MatchesCongestionRisk(10.8, 106.7) {
		t.Fatal("a feature in a non-congestion-flagged layer must not trigger the auto-tick")
	}
	// But MatchNear itself should still find it (it's layer-agnostic).
	if len(MatchNear(10.8, 106.7)) != 1 {
		t.Fatal("MatchNear should still report the feature regardless of the layer's flag")
	}
}
