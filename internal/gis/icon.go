package gis

// icon.go — one custom marker image per Layer, stored as a single file on
// disk (outside layers.json — an image doesn't belong inlined into a JSON
// record). Captured automatically from a .kmz import when one is embedded
// (see kml.go); Layer.IconContentType is the only thing persisted in
// layers.json itself, telling a caller whether GET /{id}/icon has
// anything to fetch.

import (
	"os"
	"path/filepath"
)

func iconPath(layerID string) string {
	return filepath.Join(dataDir, "icons", layerID)
}

// SetLayerIcon stores data as layerID's marker icon and records its
// content type on the Layer record itself. A second call for the same
// layer overwrites the previous icon (used by ImportKML's "first icon
// found wins" rule only within a single import — a later, separate import
// is free to replace it).
func SetLayerIcon(layerID string, data []byte, contentType string) error {
	mu.Lock()
	l, ok := layers[layerID]
	if !ok {
		mu.Unlock()
		return errNotFound
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "icons"), 0755); err != nil {
		mu.Unlock()
		return err
	}
	if err := os.WriteFile(iconPath(layerID), data, 0644); err != nil {
		mu.Unlock()
		return err
	}
	l.IconContentType = contentType
	err := saveLayersLocked()
	mu.Unlock()
	return err
}

// GetLayerIcon returns layerID's stored icon bytes + content type, if one
// has been set.
func GetLayerIcon(layerID string) (data []byte, contentType string, ok bool) {
	mu.RLock()
	l, found := layers[layerID]
	ct := ""
	if found {
		ct = l.IconContentType
	}
	mu.RUnlock()
	if ct == "" {
		return nil, "", false
	}
	data, err := os.ReadFile(iconPath(layerID))
	if err != nil {
		return nil, "", false
	}
	return data, ct, true
}

// ClearLayerIcon removes layerID's custom icon, reverting its map markers
// to the generic colored-circle rendering.
func ClearLayerIcon(layerID string) error {
	mu.Lock()
	l, ok := layers[layerID]
	if !ok {
		mu.Unlock()
		return errNotFound
	}
	l.IconContentType = ""
	err := saveLayersLocked()
	mu.Unlock()
	if err == nil {
		_ = os.Remove(iconPath(layerID))
	}
	return err
}

// deleteLayerIconLocked removes layerID's icon file, if any — called from
// DeleteLayer. Caller must already hold mu (write lock). Never errors on a
// missing file.
func deleteLayerIconLocked(layerID string) {
	_ = os.Remove(iconPath(layerID))
}
