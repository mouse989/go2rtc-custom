package gis

import (
	"archive/zip"
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

const testKML = `<?xml version="1.0" encoding="UTF-8"?>
<kml xmlns="http://www.opengis.net/kml/2.2">
  <Document>
    <name>Test layer</name>
    <Style id="icon-1-normal">
      <IconStyle><Icon><href>images/icon.png</href></Icon></IconStyle>
    </Style>
    <Style id="icon-1-highlight">
      <IconStyle><Icon><href>images/icon.png</href></Icon></IconStyle>
    </Style>
    <StyleMap id="icon-1">
      <Pair><key>normal</key><styleUrl>#icon-1-normal</styleUrl></Pair>
      <Pair><key>highlight</key><styleUrl>#icon-1-highlight</styleUrl></Pair>
    </StyleMap>
    <Placemark>
      <name>  Điểm thử nghiệm  </name>
      <styleUrl>#icon-1</styleUrl>
      <Point><coordinates>106.7,10.8,0</coordinates></Point>
    </Placemark>
    <Placemark>
      <name>Đoạn thử nghiệm</name>
      <LineString><coordinates>106.70,10.80,0 106.71,10.80,0 106.72,10.81,0</coordinates></LineString>
    </Placemark>
    <Placemark>
      <name>Không có tọa độ hợp lệ</name>
      <Point><coordinates></coordinates></Point>
    </Placemark>
  </Document>
</kml>`

func testPNGBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

func buildTestKMZ(t *testing.T, kml string, iconData []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("doc.kml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(kml)); err != nil {
		t.Fatal(err)
	}
	if iconData != nil {
		w, err = zw.Create("images/icon.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(iconData); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newTestLayer(t *testing.T) *Layer {
	t.Helper()
	l := &Layer{Name: "Test layer"}
	if err := CreateLayer(l); err != nil {
		t.Fatalf("CreateLayer: %v", err)
	}
	return l
}

func TestImportKMLPlainFile(t *testing.T) {
	setupTestStore(t)
	l := newTestLayer(t)

	result, err := ImportKML(l.ID, []byte(testKML), 100, 50)
	if err != nil {
		t.Fatalf("ImportKML: %v", err)
	}
	if result.PointsCreated != 1 || result.SegmentsCreated != 1 || result.Failed != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.IconCaptured {
		t.Error("a plain .kml (no archive) must never capture an icon")
	}

	feats := ListFeatures(l.ID)
	if len(feats) != 2 {
		t.Fatalf("expected 2 features (1 point + 1 segment), got %d", len(feats))
	}
	for _, f := range feats {
		if f.Label == "Điểm thử nghiệm" && f.Kind != FeatureKindPoint {
			t.Errorf("expected point kind for %q, got %s", f.Label, f.Kind)
		}
		if f.Label != "Điểm thử nghiệm" && f.Label != "Đoạn thử nghiệm" {
			t.Errorf("unexpected label %q", f.Label)
		}
		// Whitespace around <name> must be trimmed.
		if f.Label == " Điểm thử nghiệm " {
			t.Error("label was not trimmed")
		}
	}
}

func TestImportKMZCapturesEmbeddedIcon(t *testing.T) {
	setupTestStore(t)
	l := newTestLayer(t)
	icon := testPNGBytes(t)
	kmz := buildTestKMZ(t, testKML, icon)

	result, err := ImportKML(l.ID, kmz, 100, 50)
	if err != nil {
		t.Fatalf("ImportKML: %v", err)
	}
	if !result.IconCaptured {
		t.Fatal("expected icon to be captured from the .kmz")
	}
	if result.PointsCreated != 1 || result.SegmentsCreated != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}

	data, ct, ok := GetLayerIcon(l.ID)
	if !ok {
		t.Fatal("GetLayerIcon: not found")
	}
	if ct != "image/png" {
		t.Errorf("expected image/png, got %s", ct)
	}
	if !bytes.Equal(data, icon) {
		t.Error("stored icon bytes don't match the original")
	}
}

func TestImportKMLZeroRadiusSkipsThatKind(t *testing.T) {
	setupTestStore(t)
	l := newTestLayer(t)

	// Only points requested (segmentRadiusM=0) — the LineString placemark
	// must be skipped entirely, not errored.
	result, err := ImportKML(l.ID, []byte(testKML), 100, 0)
	if err != nil {
		t.Fatalf("ImportKML: %v", err)
	}
	if result.PointsCreated != 1 {
		t.Errorf("expected 1 point, got %d", result.PointsCreated)
	}
	if result.SegmentsCreated != 0 {
		t.Errorf("expected 0 segments (radius=0 should skip them), got %d", result.SegmentsCreated)
	}
	if len(ListFeatures(l.ID)) != 1 {
		t.Errorf("expected exactly 1 feature stored, got %d", len(ListFeatures(l.ID)))
	}
}

func TestImportKMLUnknownLayerErrors(t *testing.T) {
	setupTestStore(t)
	_, err := ImportKML("no-such-layer", []byte(testKML), 100, 50)
	if err != errLayerNotFound {
		t.Fatalf("expected errLayerNotFound, got %v", err)
	}
}

func TestImportKMLInvalidXMLErrors(t *testing.T) {
	setupTestStore(t)
	l := newTestLayer(t)
	_, err := ImportKML(l.ID, []byte("not xml at all"), 100, 50)
	if err == nil {
		t.Fatal("expected an error for invalid XML")
	}
}

func TestImportKMZWithoutKMLEntryErrors(t *testing.T) {
	setupTestStore(t)
	l := newTestLayer(t)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("readme.txt")
	_, _ = w.Write([]byte("no kml here"))
	_ = zw.Close()

	_, err := ImportKML(l.ID, buf.Bytes(), 100, 50)
	if err == nil {
		t.Fatal("expected an error for a .kmz with no .kml entry")
	}
}
