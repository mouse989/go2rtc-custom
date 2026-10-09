package aievent

// images.go — attachment image storage. Deliberately NOT bucketed by day
// the way event JSON records are (aievent.go): the default retention is
// hours, not days, so day-folder granularity would be the wrong shape for
// cleanup — this sweeps by each file's own mtime instead. Layout:
//
//	<dataDir>/images/<sanitized RecordID>/<name>.<jpg|png>
//
// A RecordID's image directory is fully replaced on every push that
// carries Attachments (see ReplaceImages) — matches the API spec's
// documented "a new Attachments list replaces the old one, it never
// accumulates" behavior.
//
// CleanupOldImages only ever deletes files here — it never touches an
// AIEvent's JSON record (aievent.go) or its ImagePaths field. A path left
// in ImagePaths after its file is gone simply 404s when requested; the Map
// layer treats that as "no image", not an error (see www/map.html's AI
// Event marker code).

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	maxImagesPerEvent = 5
	maxImageBytes     = 5 << 20 // 5 MB per image, before base64 — matches docs/omnia-integration-api-spec.md §7
)

// Attachment is one inbound (name, base64-content) pair from a push payload.
type Attachment struct {
	Name    string
	Content string
}

func imagesRoot() string { return filepath.Join(dataDir, "images") }

func imageDir(recordID string) string {
	return filepath.Join(imagesRoot(), SanitizeRecordID(recordID))
}

// ReplaceImages clears recordID's existing image directory (if any) and
// writes the given attachments in its place. Each attachment is validated
// independently (base64 decode, size cap, JPEG/PNG magic-byte sniff) — an
// invalid one is skipped, not fatal to the others or to the event itself
// (see docs/omnia-integration-api-spec.md §10.1: a bad attachment never
// turns a push into an error response). Returns the relative paths
// (relative to imagesRoot()) to store in AIEvent.ImagePaths, and how many
// attachments were skipped for being invalid.
func ReplaceImages(recordID string, attachments []Attachment) (saved []string, skipped int) {
	dir := imageDir(recordID)
	_ = os.RemoveAll(dir)
	if len(attachments) == 0 {
		return nil, 0
	}
	if len(attachments) > maxImagesPerEvent {
		skipped += len(attachments) - maxImagesPerEvent
		attachments = attachments[:maxImagesPerEvent]
	}

	safeID := SanitizeRecordID(recordID)
	for i, a := range attachments {
		raw, err := base64.StdEncoding.DecodeString(a.Content)
		if err != nil || len(raw) == 0 || len(raw) > maxImageBytes {
			skipped++
			continue
		}
		ext := sniffImageExt(raw)
		if ext == "" {
			skipped++
			continue
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			skipped++
			continue
		}
		base := sanitizeFilename(a.Name)
		if base == "" {
			base = "image"
		}
		// Index-prefixed so two attachments that sanitize to the same base
		// name (or both come in unnamed) never collide and silently
		// overwrite each other within one push.
		filename := fmt.Sprintf("%02d_%s.%s", i, base, ext)
		if err := os.WriteFile(filepath.Join(dir, filename), raw, 0644); err != nil {
			skipped++
			continue
		}
		saved = append(saved, filepath.Join(safeID, filename))
	}
	return saved, skipped
}

// ImageFilePath resolves relPath (as stored in an AIEvent's ImagePaths) to
// an absolute path, refusing anything that would escape imagesRoot() —
// relPath ultimately traces back to attacker-controlled input (the
// RecordID and attachment Name that built it), so this is the last line of
// defense even though SanitizeRecordID/sanitizeFilename already constrain
// what characters can appear in it.
func ImageFilePath(relPath string) (string, bool) {
	root := imagesRoot()
	full := filepath.Join(root, relPath)
	rel, err := filepath.Rel(root, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	if _, err := os.Stat(full); err != nil {
		return "", false
	}
	return full, true
}

// CleanupOldImages deletes image files whose mtime is older than
// retentionHours (<=0 → 4-hour default), and removes any RecordID
// directory left empty afterward. Returns how many files were deleted.
func CleanupOldImages(retentionHours int) int {
	if retentionHours <= 0 {
		retentionHours = 4
	}
	cutoff := time.Now().Add(-time.Duration(retentionHours) * time.Hour)

	root := imagesRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}

	deleted := 0
	for _, dirEntry := range entries {
		if !dirEntry.IsDir() {
			continue
		}
		sub := filepath.Join(root, dirEntry.Name())
		files, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		for _, f := range files {
			info, err := f.Info()
			if err != nil {
				continue
			}
			if info.ModTime().Before(cutoff) {
				if os.Remove(filepath.Join(sub, f.Name())) == nil {
					deleted++
				}
			}
		}
		if remaining, err := os.ReadDir(sub); err == nil && len(remaining) == 0 {
			_ = os.Remove(sub)
		}
	}
	return deleted
}

func sniffImageExt(b []byte) string {
	if len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF {
		return "jpg"
	}
	if len(b) >= 8 && bytes.Equal(b[:8], []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}) {
		return "png"
	}
	return ""
}

var sanitizeFilenameRe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// sanitizeFilename strips any directory component and extension from name
// (the extension we write is always derived from sniffImageExt, never
// trusted from the sender — a ".php" claimed over real JPEG bytes still
// only ever gets written as ".jpg") and replaces anything outside
// [A-Za-z0-9_-] in what remains.
func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	return sanitizeFilenameRe.ReplaceAllString(base, "_")
}
