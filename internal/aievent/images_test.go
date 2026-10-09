package aievent

import (
	"encoding/base64"
	"os"
	"testing"
	"time"
)

func setupImageTest(t *testing.T) {
	t.Helper()
	dataDir = t.TempDir()
}

// Minimal-but-real magic-byte prefixes — sniffImageExt only inspects the
// header, so these don't need to be valid decodable images.
var fakeJPEGBytes = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46}
var fakePNGBytes = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00}

func TestReplaceImagesSavesValidAttachments(t *testing.T) {
	setupImageTest(t)
	saved, skipped := ReplaceImages("rec1", []Attachment{
		{Name: "snapshot.jpg", Content: base64.StdEncoding.EncodeToString(fakeJPEGBytes)},
	})
	if skipped != 0 {
		t.Errorf("expected 0 skipped, got %d", skipped)
	}
	if len(saved) != 1 {
		t.Fatalf("expected 1 saved path, got %d", len(saved))
	}
	full, ok := ImageFilePath(saved[0])
	if !ok {
		t.Fatal("expected the saved image to be servable via ImageFilePath")
	}
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("expected the file to exist on disk: %v", err)
	}
}

func TestReplaceImagesSkipsInvalidBase64(t *testing.T) {
	setupImageTest(t)
	saved, skipped := ReplaceImages("rec1", []Attachment{{Name: "bad.jpg", Content: "not-valid-base64!!"}})
	if skipped != 1 {
		t.Errorf("expected 1 skipped, got %d", skipped)
	}
	if len(saved) != 0 {
		t.Errorf("expected 0 saved, got %d", len(saved))
	}
}

func TestReplaceImagesSkipsNonImageContent(t *testing.T) {
	setupImageTest(t)
	saved, skipped := ReplaceImages("rec1", []Attachment{
		{Name: "fake.jpg", Content: base64.StdEncoding.EncodeToString([]byte("this is not an image"))},
	})
	if skipped != 1 {
		t.Errorf("expected 1 skipped (bad magic bytes), got %d", skipped)
	}
	if len(saved) != 0 {
		t.Errorf("expected 0 saved, got %d", len(saved))
	}
}

func TestReplaceImagesEnforcesMaxCount(t *testing.T) {
	setupImageTest(t)
	atts := make([]Attachment, 7)
	for i := range atts {
		atts[i] = Attachment{Name: "a.jpg", Content: base64.StdEncoding.EncodeToString(fakeJPEGBytes)}
	}
	saved, skipped := ReplaceImages("rec1", atts)
	if len(saved) != maxImagesPerEvent {
		t.Errorf("expected at most %d saved, got %d", maxImagesPerEvent, len(saved))
	}
	if skipped != len(atts)-maxImagesPerEvent {
		t.Errorf("expected %d skipped for exceeding the cap, got %d", len(atts)-maxImagesPerEvent, skipped)
	}
}

// TestReplaceImagesReplacesNotAccumulates is the behavior documented in
// docs/omnia-integration-api-spec.md §7: a new Attachments list on a
// re-push replaces the old one entirely, it never accumulates.
func TestReplaceImagesReplacesNotAccumulates(t *testing.T) {
	setupImageTest(t)
	first, _ := ReplaceImages("rec1", []Attachment{{Name: "a.jpg", Content: base64.StdEncoding.EncodeToString(fakeJPEGBytes)}})
	if len(first) != 1 {
		t.Fatal("expected the first image to be saved")
	}

	second, _ := ReplaceImages("rec1", []Attachment{{Name: "b.png", Content: base64.StdEncoding.EncodeToString(fakePNGBytes)}})
	if len(second) != 1 {
		t.Fatal("expected the second image to be saved")
	}

	if _, ok := ImageFilePath(first[0]); ok {
		t.Error("expected the first push's image file to be removed after a replacing push")
	}
}

func TestImageFilePathRejectsPathTraversal(t *testing.T) {
	setupImageTest(t)
	if _, ok := ImageFilePath("../../../etc/passwd"); ok {
		t.Error("expected path traversal to be rejected")
	}
}

func TestImageFilePathRejectsMissingFile(t *testing.T) {
	setupImageTest(t)
	if _, ok := ImageFilePath("rec1/nonexistent.jpg"); ok {
		t.Error("expected a nonexistent file to report not-found")
	}
}

func TestCleanupOldImagesDeletesOnlyExpiredFiles(t *testing.T) {
	setupImageTest(t)
	saved, _ := ReplaceImages("rec1", []Attachment{{Name: "a.jpg", Content: base64.StdEncoding.EncodeToString(fakeJPEGBytes)}})
	full, _ := ImageFilePath(saved[0])
	old := time.Now().Add(-10 * time.Hour)
	if err := os.Chtimes(full, old, old); err != nil {
		t.Fatal(err)
	}

	deleted := CleanupOldImages(4) // 4-hour retention; this file is 10h old
	if deleted != 1 {
		t.Errorf("expected 1 file deleted, got %d", deleted)
	}
	if _, ok := ImageFilePath(saved[0]); ok {
		t.Error("expected the expired image to be gone")
	}
}

func TestCleanupOldImagesKeepsRecentFiles(t *testing.T) {
	setupImageTest(t)
	saved, _ := ReplaceImages("rec1", []Attachment{{Name: "a.jpg", Content: base64.StdEncoding.EncodeToString(fakeJPEGBytes)}})

	deleted := CleanupOldImages(4)
	if deleted != 0 {
		t.Errorf("expected 0 files deleted for a just-written image, got %d", deleted)
	}
	if _, ok := ImageFilePath(saved[0]); !ok {
		t.Error("expected the recent image to still exist")
	}
}
