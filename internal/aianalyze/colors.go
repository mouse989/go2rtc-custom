package aianalyze

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// DefaultClassColors are the built-in per-class marker colours for the
// browser-side detection overlay (www/ai-analyze-hud.js), used for any
// class an admin hasn't overridden via SetClassColors below. This
// deployment doesn't count people, so motorcycle (the class people
// actually care to pick out at a glance) gets the green normally
// reserved for "the thing that matters", and person — kept only in case
// a future deployment re-enables people counting — gets the orange that
// used to mark motorcycles.
var DefaultClassColors = map[string]string{
	"car":        "#00E5FF", // cyan
	"motorcycle": "#3fb950", // green
	"bus":        "#B388FF", // violet
	"truck":      "#FFEE58", // yellow
	"bicycle":    "#F06292", // pink
	"person":     "#FF9800", // orange
}

var hexColorRE = regexp.MustCompile(`^#[0-9a-fA-F]{3}$|^#[0-9a-fA-F]{6}$`)

func colorsFile() string { return filepath.Join(dataDir, "class_colors.json") }

var (
	colorsMu        sync.RWMutex
	classColorOverr map[string]string
)

func loadClassColors() {
	data, err := os.ReadFile(colorsFile())
	if err != nil {
		return // none saved yet — overrides stay empty, defaults apply
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		log.Error().Err(err).Msg("[aianalyze] corrupt class_colors.json, ignoring")
		return
	}
	colorsMu.Lock()
	classColorOverr = m
	colorsMu.Unlock()
}

// GetClassColors returns the full class→colour map the browser overlay
// should use: DefaultClassColors with any admin-saved overrides applied
// on top. Never nil, never missing a known class — a class name outside
// this map is this package's caller's problem (the overlay itself falls
// back to a generic colour for anything unrecognized).
func GetClassColors() map[string]string {
	colorsMu.RLock()
	defer colorsMu.RUnlock()
	out := make(map[string]string, len(DefaultClassColors))
	for k, v := range DefaultClassColors {
		out[k] = v
	}
	for k, v := range classColorOverr {
		out[k] = v
	}
	return out
}

// SetClassColors validates and persists a full replacement set of class
// colour overrides (admin-only — see api.go). Each value must be a CSS
// hex colour (#rgb or #rrggbb); an empty value for a class removes that
// class's override (reverting it to DefaultClassColors).
func SetClassColors(colors map[string]string) error {
	cleaned := make(map[string]string, len(colors))
	for class, color := range colors {
		if color == "" {
			continue
		}
		if !hexColorRE.MatchString(color) {
			return fmt.Errorf("invalid colour %q for class %q — expected a CSS hex colour like #3fb950", color, class)
		}
		cleaned[class] = color
	}
	data, err := json.MarshalIndent(cleaned, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(colorsFile(), data, 0644); err != nil {
		return err
	}
	colorsMu.Lock()
	classColorOverr = cleaned
	colorsMu.Unlock()
	return nil
}
