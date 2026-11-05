package trafficmetrics

import "testing"

func approxEqual(a, b, tol float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= tol
}

// Free-flow traffic (speed == freeFlow) must land exactly at TTI 1.0 and
// classify as the best LOS band.
func TestComputeFlowFreeFlow(t *testing.T) {
	cfg := defaultConfig()
	res := cfg.ComputeFlow(13.88, 13.88, 0.0)
	if res.TTIRaw != 1.0 {
		t.Errorf("TTIRaw = %v, want 1.0", res.TTIRaw)
	}
	if res.LOS.Label != "A" {
		t.Errorf("LOS = %q, want A (TTI 1.0 is the lowest band)", res.LOS.Label)
	}
}

// Moderate real-world congestion (~27 km/h on a typical VN arterial,
// TTI≈1.6) must NOT land in the worst LOS tier — the exact case that
// motivated dropping the old BPR-inverse Z estimate, which (at its
// originally specified constants) classified this as LOS F. Per HCM's own
// Urban Streets speed-ratio bands (A≥85%/B67–85%/C51–67%/D39–51%/E29–39%/
// F<29% of free-flow speed, i.e. TTI = 1/ratio), TTI≈1.6 is LOS C.
func TestComputeFlowModerateCongestionIsNotWorstTier(t *testing.T) {
	cfg := defaultConfig()
	// speed=26.9km/h=7.47m/s, freeFlow matching TTI≈1.61 (≈12.03m/s), JF
	// left at 0 so TTIFused reduces to a weighted blend still near TTIRaw.
	res := cfg.ComputeFlow(7.47, 12.03, 4.0)
	if res.TTIRaw < 1.55 || res.TTIRaw > 1.65 {
		t.Fatalf("test setup: TTIRaw = %v, want ~1.61", res.TTIRaw)
	}
	if res.LOS.Label == "F" || res.LOS.Label == "E" {
		t.Errorf("LOS = %q for TTI≈1.6, want a moderate band (B/C/D), not the worst tiers", res.LOS.Label)
	}
}

// speed=0 must never divide by zero or panic, and must land in the worst
// LOS band — a stalled/fully-jammed reading.
func TestComputeFlowZeroSpeedNoPanic(t *testing.T) {
	cfg := defaultConfig()
	res := cfg.ComputeFlow(0.0, 8.0, 10.0)
	if res.TTIRaw <= 0 {
		t.Errorf("TTIRaw = %v, want a large finite positive value", res.TTIRaw)
	}
	if res.LOS.Label != "F" {
		t.Errorf("LOS = %q, want F (worst band) for a stalled/fully-jammed reading", res.LOS.Label)
	}
}

func TestEstimateTTIFromJF(t *testing.T) {
	cases := []struct {
		jf   float64
		want float64
	}{
		{0.0, 1.0},
		{9.9, 20.0},
		{10.0, 20.0}, // clamped
		{-1.0, 1.0},  // clamped to 0
	}
	for _, c := range cases {
		if got := EstimateTTIFromJF(c.jf); got != c.want {
			t.Errorf("EstimateTTIFromJF(%v) = %v, want %v", c.jf, got, c.want)
		}
	}
}

// EstimateTTIFromJF and ImpliedJFFromTTI are meant to be exact inverses of
// each other (JF ≈ 10·sqrt(1-1/TTI) ⟺ TTI = 1/(1-(JF/10)²)). Near JF≈0 the
// mapping's derivative is steep, so round3's 3-decimal rounding on the
// intermediate TTI is amplified on the way back — a real property of this
// formula, not a bug — hence the looser tolerance at the low end.
func TestTTIJFRoundTrip(t *testing.T) {
	cases := []struct {
		jf  float64
		tol float64
	}{
		{0.5, 0.1}, {2.0, 0.02}, {4.8, 0.01}, {7.5, 0.01}, {9.0, 0.01},
	}
	for _, c := range cases {
		tti := EstimateTTIFromJF(c.jf)
		back := ImpliedJFFromTTI(tti)
		if !approxEqual(back, c.jf, c.tol) {
			t.Errorf("round-trip jf=%v -> tti=%v -> jf=%v, want ~%v (tol %v)", c.jf, tti, back, c.jf, c.tol)
		}
	}
}

func TestFuseTTI(t *testing.T) {
	got := FuseTTI(2.0, 1.0, 0.65, 0.35)
	want := 0.65*2.0 + 0.35*1.0
	if !approxEqual(got, want, 0.001) {
		t.Errorf("FuseTTI = %v, want %v", got, want)
	}
}

func TestConsistencyFlag(t *testing.T) {
	if ConsistencyFlag(8.0, 6.0, 2.5) {
		t.Error("delta=2.0 should not flag at threshold 2.5")
	}
	if !ConsistencyFlag(8.0, 4.0, 2.5) {
		t.Error("delta=4.0 should flag at threshold 2.5")
	}
}

func TestDetectBottleneck(t *testing.T) {
	if DetectBottleneck([]float64{5.0}, 3.0) {
		t.Error("single subSegment should never flag a bottleneck")
	}
	if DetectBottleneck([]float64{9.1, 3.9, 9.1}, 3.0) == false {
		t.Error("spread of 5.2 should flag a bottleneck at threshold 3.0")
	}
	if DetectBottleneck([]float64{5.0, 5.5, 5.2}, 3.0) {
		t.Error("spread of 0.5 should not flag a bottleneck at threshold 3.0")
	}
}

// Boundaries are HCM Exhibit 15-2's A–F speed-ratio bands converted to
// TTI (A<1.18, B 1.18–1.49, C 1.49–1.96, D 1.96–2.56, E 2.56–3.45, F>3.45).
func TestClassifyLOSBoundaries(t *testing.T) {
	levels := defaultLOSLevels()
	cases := []struct {
		tti  float64
		want string
	}{
		{1.0, "A"}, {1.17, "A"}, {1.18, "B"}, {1.48, "B"}, {1.49, "C"},
		{1.95, "C"}, {1.96, "D"}, {2.55, "D"}, {2.56, "E"}, {3.44, "E"},
		{3.45, "F"}, {10.0, "F"},
	}
	for _, c := range cases {
		if got := ClassifyLOS(c.tti, levels).Label; got != c.want {
			t.Errorf("ClassifyLOS(%v) = %q, want %q", c.tti, got, c.want)
		}
	}
}

// Out-of-order input (e.g. an admin PUT that saved levels unsorted, or a
// stale config file hand-edited) must not break classification.
func TestClassifyLOSTolerateUnsortedInput(t *testing.T) {
	shuffled := []LOSLevel{
		{Label: "F", TTIMin: 3.45},
		{Label: "A", TTIMin: 1.0},
		{Label: "C", TTIMin: 1.49},
	}
	if got := ClassifyLOS(1.7, shuffled).Label; got != "C" {
		t.Errorf("ClassifyLOS with unsorted levels = %q, want C", got)
	}
}

func TestComputeRouteLeg(t *testing.T) {
	cfg := defaultConfig()
	if got := cfg.ComputeRouteLeg(1.0).Label; got != "A" {
		t.Errorf("ComputeRouteLeg(1.0) = %q, want A", got)
	}
	if got := cfg.ComputeRouteLeg(1.61).Label; got != "C" {
		t.Errorf("ComputeRouteLeg(1.61) = %q, want C (moderate congestion, not the worst tier)", got)
	}
	if got := cfg.ComputeRouteLeg(4.0).Label; got != "F" {
		t.Errorf("ComputeRouteLeg(4.0) = %q, want F", got)
	}
}
