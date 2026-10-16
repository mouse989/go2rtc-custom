package incidents

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// pngBytes renders a solid-color square of the given side length as a PNG,
// so dimension-limit tests don't depend on a hardcoded binary fixture.
func pngBytes(t *testing.T, side int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode test PNG: %v", err)
	}
	return buf.Bytes()
}

// withTempCategoryIconStore points the package's category-icon store at a
// fresh temp directory for the duration of the test, restoring whatever
// was there before (dataDir/cistore are package globals shared with
// incidents.go's own state) so this test can't leak into others.
func withTempCategoryIconStore(t *testing.T) {
	t.Helper()
	prevDir, prevStore := dataDir, cistore
	dataDir = t.TempDir()
	initCategoryIcons()
	t.Cleanup(func() { dataDir, cistore = prevDir, prevStore })
}

func TestSetCategoryIconRejectsOversizedImage(t *testing.T) {
	withTempCategoryIconStore(t)

	// At the limit: accepted.
	if _, err := SetCategoryIcon("Đông xe", pngBytes(t, maxCategoryIconDimPx), "tester"); err != nil {
		t.Fatalf("icon at the %dpx limit should be accepted, got: %v", maxCategoryIconDimPx, err)
	}
	// One pixel over: rejected, not silently resized.
	if _, err := SetCategoryIcon("Va chạm", pngBytes(t, maxCategoryIconDimPx+1), "tester"); err == nil {
		t.Fatalf("icon over the %dpx limit should be rejected", maxCategoryIconDimPx)
	}
}

func TestSetCategoryIconRejectsNonImage(t *testing.T) {
	withTempCategoryIconStore(t)
	if _, err := SetCategoryIcon("Ngập nước", []byte("not an image"), "tester"); err == nil {
		t.Fatal("non-image upload should be rejected")
	}
}

// TestCategoryIconRoundTripIsNotHardcodedToKnownCategories is the direct
// regression this feature exists for: an arbitrary, never-before-seen
// category string (not one of the small set this codebase's JS used to
// string-match) must round-trip through Set/Get/List/Delete exactly like
// any other — nothing here may special-case a specific category value.
func TestCategoryIconRoundTripIsNotHardcodedToKnownCategories(t *testing.T) {
	withTempCategoryIconStore(t)

	category := "Cây đổ bất thường 🌳"
	data := pngBytes(t, 16)
	ci, err := SetCategoryIcon(category, data, "tester")
	if err != nil {
		t.Fatalf("SetCategoryIcon: %v", err)
	}
	if ci.Width != 16 || ci.Height != 16 || ci.ContentType != "image/png" {
		t.Fatalf("unexpected metadata: %+v", ci)
	}

	got, ct, ok := GetCategoryIconImage(category)
	if !ok || ct != "image/png" || !bytes.Equal(got, data) {
		t.Fatalf("GetCategoryIconImage round-trip mismatch: ok=%v ct=%s len=%d", ok, ct, len(got))
	}

	list := ListCategoryIcons()
	if len(list) != 1 || list[0].Category != category {
		t.Fatalf("ListCategoryIcons = %+v, want one entry for %q", list, category)
	}

	if _, _, ok := GetCategoryIconImage("some other category entirely"); ok {
		t.Fatal("an unmapped category must report ok=false, not fall back to someone else's icon")
	}

	if err := DeleteCategoryIcon(category); err != nil {
		t.Fatalf("DeleteCategoryIcon: %v", err)
	}
	if _, _, ok := GetCategoryIconImage(category); ok {
		t.Fatal("icon should be gone after DeleteCategoryIcon")
	}
	if err := DeleteCategoryIcon(category); err != errNotFound {
		t.Fatalf("deleting an already-deleted category should return errNotFound, got: %v", err)
	}
}

func TestSetCategoryColorWithoutIcon(t *testing.T) {
	withTempCategoryIconStore(t)
	ci, err := SetCategoryColor("Sự cố khác", "#ff8800", "tester")
	if err != nil {
		t.Fatalf("SetCategoryColor: %v", err)
	}
	if ci.Color != "#ff8800" || ci.ContentType != "" {
		t.Fatalf("expected color set with no icon yet, got: %+v", ci)
	}
}
