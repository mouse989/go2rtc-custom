package auth

// jam_color.go — the built-in JamFactor (JF, 0–10) color scale every map
// page falls back to until an admin customizes it via PUT
// /api/jam-color-cfg (api_settings.go). Replaces the old 3-bucket
// hard-edged scheme (grey <5, yellow 5–7.9, red ≥8) with a continuous
// gradient: every page builds a MapLibre/VietMapGL `interpolate` paint
// expression from these stops (app.js's jfColorExpression), so color
// blends smoothly between them instead of snapping at a threshold.
//
// Stops are denser from 8.0–10.0 (~0.2 apart) than elsewhere — that's the
// "vùng ùn tắc nặng" (severe-jam band) where distinguishing JF 8.2 from
// JF 9.8 actually matters to an operator; below that, traffic is some
// shade of "not that bad" and a coarser gradient is enough.

import (
	"fmt"
	"regexp"
	"sort"
)

var jamColorDefaultStops = []JamColorStop{
	{0.0, "#2ecc71"},
	{3.0, "#a3e635"},
	{5.0, "#facc15"},
	{6.5, "#fb923c"},
	{7.5, "#f87171"},
	{8.0, "#ef4444"},
	{8.2, "#e03e3e"},
	{8.4, "#dc2626"},
	{8.6, "#c81e1e"},
	{8.8, "#b91c1c"},
	{9.0, "#a51c1c"},
	{9.2, "#941818"},
	{9.4, "#841414"},
	{9.6, "#711111"},
	{9.8, "#5e0d0d"},
	{10.0, "#450a0a"},
}

// EffectiveJamColorStops returns the admin-configured color stops if any
// are saved, else the built-in default — never empty, so a caller never
// needs a fallback of its own.
func EffectiveJamColorStops() []JamColorStop {
	s := GetSettings()
	if len(s.JamColorStops) > 0 {
		return s.JamColorStops
	}
	return jamColorDefaultStops
}

var hexColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// validateJamColorStops checks what a MapLibre `interpolate` expression
// requires (stops strictly increasing by Value, so the GL renderer doesn't
// reject the whole layer) plus what makes sense for this specific scale
// (at least 2 stops to interpolate between at all, Value within 0–10,
// Color a plain "#rrggbb" — MapLibre accepts other CSS color syntaxes too,
// but restricting to hex keeps the admin UI's <input type=color> and this
// validation trivially in sync).
func validateJamColorStops(stops []JamColorStop) error {
	if len(stops) < 2 {
		return fmt.Errorf("need at least 2 color stops")
	}
	for i, s := range stops {
		if s.Value < 0 || s.Value > 10 {
			return fmt.Errorf("stop %d: value %.2f must be between 0 and 10", i, s.Value)
		}
		if !hexColorRe.MatchString(s.Color) {
			return fmt.Errorf("stop %d: color %q must be a #rrggbb hex value", i, s.Color)
		}
		if i > 0 && s.Value <= stops[i-1].Value {
			return fmt.Errorf("stop %d: value %.2f must be greater than the previous stop's %.2f", i, s.Value, stops[i-1].Value)
		}
	}
	return nil
}

// sortJamColorStops returns a copy of stops sorted by Value ascending —
// used to tolerate a client that sent stops out of order rather than
// rejecting the whole save over ordering alone.
func sortJamColorStops(stops []JamColorStop) []JamColorStop {
	out := make([]JamColorStop, len(stops))
	copy(out, stops)
	sort.Slice(out, func(i, j int) bool { return out[i].Value < out[j].Value })
	return out
}
