package aianalyze

import (
	"os"
	"testing"
)

// withTempStore points the package's data dir at a fresh temp directory
// for the duration of the test, restoring whatever was there before
// (dataDir/meta/hasMeta are package globals) so tests can't leak into
// each other or into a real data dir.
func withTempStore(t *testing.T) {
	t.Helper()
	prevDir, prevMeta, prevHas := dataDir, meta, hasMeta
	dataDir = t.TempDir()
	meta, hasMeta = ModelMeta{}, false
	t.Cleanup(func() { dataDir, meta, hasMeta = prevDir, prevMeta, prevHas })
}

func TestPublishModelRejectsEmptyData(t *testing.T) {
	withTempStore(t)
	_, err := PublishModel(nil, ModelMeta{Labels: []string{"car"}}, "admin")
	if err == nil {
		t.Fatal("expected error publishing empty model data")
	}
}

func TestPublishModelRejectsMissingLabels(t *testing.T) {
	withTempStore(t)
	_, err := PublishModel([]byte("fake onnx bytes"), ModelMeta{}, "admin")
	if err == nil {
		t.Fatal("expected error publishing a model with no labels — the browser decoder can't name classes without them")
	}
}

func TestPublishModelRoundTrip(t *testing.T) {
	withTempStore(t)
	data := []byte("fake onnx bytes for a 4-class vehicle model")
	m := ModelMeta{
		Labels:        []string{"car", "motorcycle", "bus", "truck"},
		VehicleLabels: []string{"car", "motorcycle", "bus", "truck"},
		InputSize:     416,
		Format:        "ultralytics",
		SourceModel:   "models/trained_20260101_120000.pt",
	}
	saved, err := PublishModel(data, m, "admin")
	if err != nil {
		t.Fatalf("PublishModel: %v", err)
	}
	if saved.SizeBytes != int64(len(data)) {
		t.Errorf("SizeBytes = %d, want %d", saved.SizeBytes, len(data))
	}
	if saved.UpdatedBy != "admin" {
		t.Errorf("UpdatedBy = %q, want admin", saved.UpdatedBy)
	}
	if saved.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set")
	}

	if !HasModel() {
		t.Fatal("HasModel() should be true right after a successful publish")
	}
	got, ok := GetModelMeta()
	if !ok {
		t.Fatal("GetModelMeta() should report a model is published")
	}
	if len(got.Labels) != 4 || got.Labels[2] != "bus" {
		t.Errorf("GetModelMeta().Labels = %v, want the 4 published labels in order", got.Labels)
	}

	onDisk, err := os.ReadFile(modelFile())
	if err != nil {
		t.Fatalf("read model file: %v", err)
	}
	if string(onDisk) != string(data) {
		t.Error("model.onnx on disk doesn't match the published bytes")
	}

	// A fresh process (loadMeta on Init) must see the same metadata.
	meta, hasMeta = ModelMeta{}, false
	loadMeta()
	reloaded, ok := GetModelMeta()
	if !ok {
		t.Fatal("loadMeta() should restore the published metadata from disk")
	}
	if len(reloaded.Labels) != 4 {
		t.Errorf("reloaded.Labels = %v, want 4 labels", reloaded.Labels)
	}
}

func TestPublishModelRejectsOversized(t *testing.T) {
	withTempStore(t)
	big := make([]byte, maxModelUploadBytes+1)
	_, err := PublishModel(big, ModelMeta{Labels: []string{"car"}}, "admin")
	if err == nil {
		t.Fatal("expected error publishing an over-the-cap model file")
	}
}

func TestHasModelFalseBeforePublish(t *testing.T) {
	withTempStore(t)
	if HasModel() {
		t.Error("HasModel() should be false before anything is published")
	}
	if _, ok := GetModelMeta(); ok {
		t.Error("GetModelMeta() should report nothing published")
	}
}

func TestSplitCSV(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"car", []string{"car"}},
		{"car,motorcycle,bus,truck", []string{"car", "motorcycle", "bus", "truck"}},
		{"car,,truck", []string{"car", "truck"}}, // empty middle segment dropped
	}
	for _, c := range cases {
		got := splitCSV(c.in)
		if len(got) != len(c.want) {
			t.Errorf("splitCSV(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitCSV(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}
