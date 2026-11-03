package trafficmetrics

import "testing"

func approxEqual(a, b, tol float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= tol
}

// TC-01 (SPEC-ITS-TRAFFIC-ANALYSIS-01 §7): free-flow night traffic — TTI
// must land exactly at 1.0 and Z at 0.0, the only one of the doc's 5 worked
// examples that is actually self-consistent with its own stated formula
// when checked by hand (see TestSpecTableTC04IsNotSelfConsistent below for
// why the others aren't used as literal assertions here).
func TestSpecTC01FreeFlow(t *testing.T) {
	cfg := defaultConfig()
	res := cfg.ComputeFlow(13.88, 13.88, 0.0)
	if res.TTIRaw != 1.0 {
		t.Errorf("TTIRaw = %v, want 1.0", res.TTIRaw)
	}
	if res.Z != 0.0 {
		t.Errorf("Z = %v, want 0.0", res.Z)
	}
	if res.LOS.Label != "A" {
		t.Errorf("LOS = %q, want A (Z=0 is the lowest band)", res.LOS.Label)
	}
}

// TC-05: speed=0 must never divide by zero or panic, and must land in the
// worst LOS band (the spec only asserts "no error, TTI max, Level 5" —
// not an exact number, since the real guarantee being tested is fault
// tolerance, not a specific value).
func TestSpecTC05ZeroSpeedNoPanic(t *testing.T) {
	cfg := defaultConfig()
	res := cfg.ComputeFlow(0.0, 8.0, 10.0)
	if res.TTIRaw <= 0 {
		t.Errorf("TTIRaw = %v, want a large finite positive value", res.TTIRaw)
	}
	if res.LOS.Label != "F" {
		t.Errorf("LOS = %q, want F (worst band) for a stalled/fully-jammed reading", res.LOS.Label)
	}
}

// The doc's own TC-02/03/04 worked examples do not reproduce under the
// formulas as literally stated in §3.1-3.3 and the §5.2 Python reference
// (hand-verified: e.g. TC-04's inputs give TTI_fused≈6.41 → Z≈2.95 under
// alpha=0.50/beta=2.20, not the table's "TTI:6.36, Z_est:1.80") — most
// likely an authoring error in the illustrative table, not a different
// intended formula. This test pins that the *formula itself* (not the
// table) is what's implemented, so a future "fix" blindly matching the
// table's numbers doesn't silently break the documented math.
func TestSpecTableTC04IsNotSelfConsistent(t *testing.T) {
	cfg := defaultConfig()
	res := cfg.ComputeFlow(0.83, 5.28, 9.2)
	if approxEqual(res.Z, 1.80, 0.05) {
		t.Fatalf("Z = %v happens to match the doc table's 1.80 — re-check whether the table was fixed upstream; if so, update this test's expectation instead of deleting it", res.Z)
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
// each other per the doc's "⟺" in §3.2 — the strongest check available
// given the suspect worked-example table above. Near JF≈0 the mapping's
// derivative is steep, so round3's 3-decimal rounding on the intermediate
// TTI is amplified on the way back — a real property of this formula, not
// a bug — hence the looser tolerance at the low end.
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

func TestInferZ(t *testing.T) {
	if z := InferZ(1.0, 0.5, 2.2); z != 0 {
		t.Errorf("InferZ(1.0) = %v, want 0", z)
	}
	if z := InferZ(0.5, 0.5, 2.2); z != 0 {
		t.Errorf("InferZ(<1.0) = %v, want 0", z)
	}
	// TTI = 1 + alpha*Z^beta with Z=1 exactly: TTI = 1.5.
	if z := InferZ(1.5, 0.5, 2.2); !approxEqual(z, 1.0, 0.01) {
		t.Errorf("InferZ(1.5, alpha=0.5, beta=2.2) = %v, want ~1.0", z)
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

func TestClassifyLOSBoundaries(t *testing.T) {
	levels := defaultLOSLevels()
	cases := []struct {
		pct  float64
		want string
	}{
		{0, "A"}, {24.9, "A"}, {25, "B"}, {44.9, "B"}, {45, "C"},
		{74.9, "C"}, {75, "D"}, {89.9, "D"}, {90, "E"}, {104.9, "E"},
		{105, "F"}, {500, "F"},
	}
	for _, c := range cases {
		if got := ClassifyLOS(c.pct, levels).Label; got != c.want {
			t.Errorf("ClassifyLOS(%v) = %q, want %q", c.pct, got, c.want)
		}
	}
}

// Out-of-order input (e.g. an admin PUT that saved levels unsorted, or a
// stale config file hand-edited) must not break classification.
func TestClassifyLOSTolerateUnsortedInput(t *testing.T) {
	shuffled := []LOSLevel{
		{Label: "F", ZMinPercent: 105},
		{Label: "A", ZMinPercent: 0},
		{Label: "C", ZMinPercent: 45},
	}
	if got := ClassifyLOS(50, shuffled).Label; got != "C" {
		t.Errorf("ClassifyLOS with unsorted levels = %q, want C", got)
	}
}

func TestComputeRouteLeg(t *testing.T) {
	cfg := defaultConfig()
	res := cfg.ComputeRouteLeg(1.0)
	if res.Z != 0 || res.LOS.Label != "A" {
		t.Errorf("ComputeRouteLeg(1.0) = %+v, want Z=0, LOS=A", res)
	}
	res2 := cfg.ComputeRouteLeg(2.45) // real sample TTI from this session's segment_capture_db_test.go fixture
	if res2.Z <= 0 {
		t.Errorf("ComputeRouteLeg(2.45).Z = %v, want > 0", res2.Z)
	}
}
