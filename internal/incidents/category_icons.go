package incidents

// category_icons.go — admin-configured marker icon (+ optional background
// color) per incident Category VALUE. Category is free text (see
// Incident.Category's doc comment and metaData.Categories' auto-learned
// vocabulary in incidents.go) — never a fixed enum — so this intentionally
// does NOT work like internal/aievent/categories.go's ValidIconKeys (a
// closed set of 4 bundled PNGs selected by key): here the admin uploads
// the actual image for whatever category string is actually in use, and
// nothing in this package or its callers hardcodes a specific category
// string to recognize ("Đông xe", "Va chạm", ...). www/map.html's Traffic
// Event layer and www/dashboard2.html's incident map both read this store
// (via GET /api/incidents/category-icons) instead of string-matching
// categories in JS, and fall back to one generic marker for any category
// with no mapping here.

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"  // image.DecodeConfig format registration
	_ "image/jpeg" // image.DecodeConfig format registration
	_ "image/png"  // image.DecodeConfig format registration
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// maxCategoryIconDimPx bounds an uploaded icon's width AND height.
// Incident markers are drawn small on the map (20-30px — see map.html's
// .te-marker-img), so anything bigger only costs storage/bandwidth
// without looking any better on screen; 128px leaves headroom for
// higher-density displays while still rejecting an oversized image
// dropped in by mistake. Enforced server-side so every renderer can trust
// a stored icon is already map-sized without re-checking itself.
const maxCategoryIconDimPx = 128

// maxCategoryIconUploadBytes bounds the raw upload before it's even
// decoded — a marker icon this small should never legitimately need more
// than a few hundred KB.
const maxCategoryIconUploadBytes = 1 << 20 // 1MB

// CategoryIcon is one category's marker configuration.
type CategoryIcon struct {
	Category    string    `json:"category"`
	ContentType string    `json:"content_type,omitempty"`
	Width       int       `json:"width,omitempty"`
	Height      int       `json:"height,omitempty"`
	Color       string    `json:"color,omitempty"` // optional marker background (CSS color); "" = renderer's default
	UpdatedAt   time.Time `json:"updated_at"`
	UpdatedBy   string    `json:"updated_by,omitempty"`
}

type categoryIconStore struct {
	mu    sync.RWMutex
	file  string
	icons map[string]*CategoryIcon // by Category
}

var cistore *categoryIconStore

// initCategoryIcons loads the store. Called from Init() alongside
// loadMeta() — dataDir is already set by the time this runs.
func initCategoryIcons() {
	cistore = &categoryIconStore{
		file:  filepath.Join(dataDir, "category_icons.json"),
		icons: map[string]*CategoryIcon{},
	}
	data, err := os.ReadFile(cistore.file)
	if err != nil {
		return // fine on first run — nothing configured yet
	}
	var list []*CategoryIcon
	if json.Unmarshal(data, &list) == nil {
		for _, ci := range list {
			cistore.icons[ci.Category] = ci
		}
	}
}

func (s *categoryIconStore) saveLocked() error {
	list := make([]*CategoryIcon, 0, len(s.icons))
	for _, ci := range s.icons {
		list = append(list, ci)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Category < list[j].Category })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.file, data, 0644)
}

// categoryIconKey turns an arbitrary category string (Unicode, spaces,
// slashes — whatever an operator typed) into a safe filename.
func categoryIconKey(category string) string {
	sum := sha1.Sum([]byte(category))
	return hex.EncodeToString(sum[:])
}

func categoryIconPath(category string) string {
	return filepath.Join(dataDir, "category_icons", categoryIconKey(category))
}

// ListCategoryIcons returns every configured category's metadata (not the
// image bytes themselves — fetch those via GetCategoryIconImage/the
// GET .../category-icons/image endpoint).
func ListCategoryIcons() []CategoryIcon {
	cistore.mu.RLock()
	defer cistore.mu.RUnlock()
	out := make([]CategoryIcon, 0, len(cistore.icons))
	for _, ci := range cistore.icons {
		out = append(out, *ci)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Category < out[j].Category })
	return out
}

// SetCategoryIcon validates and stores category's marker icon, preserving
// any color already set for it. Rejects anything that doesn't sniff as a
// supported image type, or whose decoded pixel dimensions exceed
// maxCategoryIconDimPx in either axis.
func SetCategoryIcon(category string, data []byte, updatedBy string) (*CategoryIcon, error) {
	if category == "" {
		return nil, validationError("category required")
	}
	if len(data) == 0 {
		return nil, validationError("empty file")
	}
	ct := http.DetectContentType(data)
	if ct != "image/png" && ct != "image/jpeg" && ct != "image/gif" {
		return nil, validationError("unsupported image type (dùng PNG, JPEG hoặc GIF)")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, validationError("không đọc được ảnh: " + err.Error())
	}
	if cfg.Width > maxCategoryIconDimPx || cfg.Height > maxCategoryIconDimPx {
		return nil, validationError(fmt.Sprintf(
			"icon quá lớn: %dx%d px (tối đa %dx%d px)", cfg.Width, cfg.Height, maxCategoryIconDimPx, maxCategoryIconDimPx))
	}

	if err := os.MkdirAll(filepath.Join(dataDir, "category_icons"), 0755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(categoryIconPath(category), data, 0644); err != nil {
		return nil, err
	}

	cistore.mu.Lock()
	ci, ok := cistore.icons[category]
	if !ok {
		ci = &CategoryIcon{Category: category}
		cistore.icons[category] = ci
	}
	ci.ContentType, ci.Width, ci.Height = ct, cfg.Width, cfg.Height
	ci.UpdatedAt, ci.UpdatedBy = time.Now(), updatedBy
	err = cistore.saveLocked()
	cistore.mu.Unlock()
	if err != nil {
		return nil, err
	}
	cp := *ci
	return &cp, nil
}

// SetCategoryColor sets (or clears, color == "") category's marker
// background color without touching its icon — creates the category's
// entry if it doesn't exist yet (an admin may want to set a color before
// ever uploading an icon, or vice versa).
func SetCategoryColor(category, color, updatedBy string) (*CategoryIcon, error) {
	if category == "" {
		return nil, validationError("category required")
	}
	cistore.mu.Lock()
	defer cistore.mu.Unlock()
	ci, ok := cistore.icons[category]
	if !ok {
		ci = &CategoryIcon{Category: category}
		cistore.icons[category] = ci
	}
	ci.Color = color
	ci.UpdatedAt, ci.UpdatedBy = time.Now(), updatedBy
	if err := cistore.saveLocked(); err != nil {
		return nil, err
	}
	cp := *ci
	return &cp, nil
}

// GetCategoryIconImage returns category's stored icon bytes + content
// type, if one has been uploaded.
func GetCategoryIconImage(category string) (data []byte, contentType string, ok bool) {
	cistore.mu.RLock()
	ci, found := cistore.icons[category]
	cistore.mu.RUnlock()
	if !found || ci.ContentType == "" {
		return nil, "", false
	}
	data, err := os.ReadFile(categoryIconPath(category))
	if err != nil {
		return nil, "", false
	}
	return data, ci.ContentType, true
}

// DeleteCategoryIcon removes category's mapping entirely (icon + color) —
// renderers fall back to their generic marker for it again.
func DeleteCategoryIcon(category string) error {
	cistore.mu.Lock()
	_, ok := cistore.icons[category]
	delete(cistore.icons, category)
	err := cistore.saveLocked()
	cistore.mu.Unlock()
	if !ok {
		return errNotFound
	}
	if err == nil {
		_ = os.Remove(categoryIconPath(category))
	}
	return err
}
