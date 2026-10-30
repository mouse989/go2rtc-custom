package traffic

import "testing"

// realSampleJSON is a trimmed excerpt of a real traffic.genfast.vn
// /api/traffic/flow response (captured 2026-10-30, region "TPHCM BD") —
// used to pin parseFlowResults against the actual production shape instead
// of a guessed one. Includes: an entry with no "description" (the first
// entry in the real response lacks one), an entry with "jamTendency"
// absent entirely (first entry again), and the one entry with
// "subSegments" that originally revealed the API already splits a long
// road into jammed/clear pieces on its own.
const realSampleJSON = `{"sourceUpdated":"2026-06-09T11:06:40Z","results":[` +
	`{"location":{"length":47.0,"shape":{"links":[` +
	`{"points":[{"lat":10.80114,"lng":106.66217},{"lat":10.80099,"lng":106.66213},{"lat":10.80087,"lng":106.66214}],"length":31.0,"functionalClass":2},` +
	`{"points":[{"lat":10.80087,"lng":106.66214},{"lat":10.80073,"lng":106.66218}],"length":16.0,"functionalClass":2}` +
	`]}},"currentFlow":{"speed":1.9444445,"speedUncapped":1.9444445,"freeFlow":7.777778,"jamFactor":8.7,"confidence":0.97,"traversability":"open"}},` +
	`{"location":{"description":"Duong Nguyen Thi Thap","length":1054.0,"shape":{"links":[` +
	`{"points":[{"lat":10.73097,"lng":106.70022},{"lat":10.73114,"lng":106.70019}],"length":19.0,"functionalClass":3}` +
	`]}},"currentFlow":{"speed":4.4444447,"speedUncapped":4.4444447,"freeFlow":14.444445,"jamFactor":8.2,"confidence":0.92,"traversability":"open",` +
	`"subSegments":[` +
	`{"length":172.0,"speed":2.2222223,"speedUncapped":2.2222223,"freeFlow":13.611112,"jamFactor":9.1,"confidence":0.92,"traversability":"open"},` +
	`{"length":673.0,"speed":8.055555,"speedUncapped":8.055555,"freeFlow":14.166667,"jamFactor":3.9,"jamTendency":1,"confidence":0.92,"traversability":"open"},` +
	`{"length":209.0,"speed":2.777778,"speedUncapped":2.777778,"freeFlow":16.666668,"jamFactor":9.1,"jamTendency":1,"confidence":0.92,"traversability":"open"}` +
	`]}}` +
	`]}`

func TestParseFlowResultsRealSample(t *testing.T) {
	results, sourceUpdated, err := parseFlowResults([]byte(realSampleJSON))
	if err != nil {
		t.Fatalf("parseFlowResults: %v", err)
	}
	if sourceUpdated != "2026-06-09T11:06:40Z" {
		t.Errorf("sourceUpdated = %q, want the top-level sourceUpdated field", sourceUpdated)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	first := results[0]
	if first.Description != "" {
		t.Errorf("first.Description = %q, want empty (real sample's first entry has none)", first.Description)
	}
	if first.LengthM != 47.0 {
		t.Errorf("first.LengthM = %v, want 47.0", first.LengthM)
	}
	if first.JamFactor != 8.7 {
		t.Errorf("first.JamFactor = %v, want 8.7", first.JamFactor)
	}
	if first.HasJamTendency {
		t.Error("first.HasJamTendency = true, want false (field absent in real sample)")
	}
	if len(first.Links) != 2 {
		t.Fatalf("first.Links = %d, want 2", len(first.Links))
	}
	if first.Links[0].LengthM != 31.0 || first.Links[0].FunctionalClass != 2 {
		t.Errorf("first.Links[0] = %+v, want length 31.0 functionalClass 2", first.Links[0])
	}
	if len(first.Links[0].Points) != 3 {
		t.Errorf("first.Links[0].Points = %d, want 3", len(first.Links[0].Points))
	}
	if first.Links[0].Points[0] != [2]float64{10.80114, 106.66217} {
		t.Errorf("first.Links[0].Points[0] = %v, want [10.80114 106.66217]", first.Links[0].Points[0])
	}
	if len(first.SubSegments) != 0 {
		t.Errorf("first.SubSegments = %d, want 0", len(first.SubSegments))
	}

	second := results[1]
	if second.Description != "Duong Nguyen Thi Thap" {
		t.Errorf("second.Description = %q, want %q", second.Description, "Duong Nguyen Thi Thap")
	}
	if second.LengthM != 1054.0 {
		t.Errorf("second.LengthM = %v, want 1054.0", second.LengthM)
	}
	if len(second.SubSegments) != 3 {
		t.Fatalf("second.SubSegments = %d, want 3 (API already split this road into jammed/clear pieces)", len(second.SubSegments))
	}
	clear := second.SubSegments[1] // the 673m relatively-clear middle piece
	if clear.LengthM != 673.0 {
		t.Errorf("clear.LengthM = %v, want 673.0", clear.LengthM)
	}
	if clear.JamFactor != 3.9 {
		t.Errorf("clear.JamFactor = %v, want 3.9 (clearly lower than the 9.1 jammed pieces either side)", clear.JamFactor)
	}
	if !clear.HasJamTendency || clear.JamTendency != 1 {
		t.Errorf("clear jamTendency = (has=%v, val=%v), want (true, 1)", clear.HasJamTendency, clear.JamTendency)
	}
	jammedStart := second.SubSegments[0]
	if jammedStart.JamFactor != 9.1 || jammedStart.HasJamTendency {
		t.Errorf("jammedStart = %+v, want JamFactor 9.1, HasJamTendency false (field absent on this piece)", jammedStart)
	}

	// Unit sanity check (see flow_capture_parse.go's doc comment): raw
	// values are m/s, not km/h — a free-flow of 14.44 "km/h" would be
	// implausible for a named road; ×3.6 gives ~52 km/h, plausible.
	if got := second.FreeFlowMps * 3.6; got < 40 || got > 65 {
		t.Errorf("second free-flow in km/h = %.1f, expected a plausible ~50 km/h arterial-road figure", got)
	}
}

func TestParseFlowResultsNonFlowShapeIsNotAnError(t *testing.T) {
	results, sourceUpdated, err := parseFlowResults([]byte(`{"type":"FeatureCollection","features":[]}`))
	if err != nil {
		t.Fatalf("parseFlowResults: %v, want nil error for a non-HERE-shaped-but-valid JSON body", err)
	}
	if results != nil {
		t.Errorf("results = %+v, want nil", results)
	}
	if sourceUpdated != "" {
		t.Errorf("sourceUpdated = %q, want empty", sourceUpdated)
	}
}

func TestParseFlowResultsInvalidJSONIsAnError(t *testing.T) {
	if _, _, err := parseFlowResults([]byte("not json")); err == nil {
		t.Error("expected an error for malformed JSON")
	}
}
