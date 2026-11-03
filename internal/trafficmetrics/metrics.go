package trafficmetrics

// metrics.go — pure math implementing SPEC-ITS-TRAFFIC-ANALYSIS-01: TTI<->JF
// correlation, Z (hệ số sử dụng khả năng thông hành / capacity utilization
// ratio) inference via a Vietnam-calibrated inverse-BPR model, and the
// bottleneck/consistency diagnostic flags. No I/O, no config loading here —
// callers pass the calibration constants explicitly (see config.go's Config
// for where those live and how they're admin-editable) so this file stays
// trivially unit-testable against the spec's own TC-01..TC-05 cases.

import "math"

// CalculateTTIFromSpeed computes TTI = freeFlow/speed from raw m/s values
// (VietMap's currentFlow.speed/freeFlow — confirmed m/s, not km/h, by
// internal/traffic/flow_capture_parse.go's own doc comment). Both inputs
// are floored to a small positive value so a near-zero/zero reading
// (stalled traffic, a bad sample) never divides by zero or yields a wild
// ratio.
func CalculateTTIFromSpeed(speedMps, freeFlowMps float64) float64 {
	safeSpeed := math.Max(speedMps, 0.2)
	safeFreeFlow := math.Max(freeFlowMps, 0.5)
	return round3(safeFreeFlow / safeSpeed)
}

// EstimateTTIFromJF estimates TTI from a 0–10 JamFactor via the spec's
// empirical inverse mapping. jf is clamped to [0, 9.9]; the result is
// capped at 20.0 so jf→10 (where the denominator approaches 0) never
// overflows.
func EstimateTTIFromJF(jf float64) float64 {
	if jf >= 9.9 {
		return 20.0
	}
	normalized := math.Max(math.Min(jf, 9.9), 0) / 10.0
	return round3(1.0 / (1.0 - normalized*normalized))
}

// ImpliedJFFromTTI is the forward direction of the same empirical mapping
// EstimateTTIFromJF inverts: the JamFactor a given TTI would imply, used by
// ConsistencyFlag to cross-check an actually-reported JF against the one
// implied by an independently speed-derived TTI.
func ImpliedJFFromTTI(tti float64) float64 {
	if tti <= 1.0 {
		return 0.0
	}
	v := 10.0 * math.Sqrt(1.0-1.0/tti)
	if v > 10 {
		v = 10
	}
	return round3(v)
}

// FuseTTI blends a directly-measured TTI (from speed) with one implied by
// JamFactor, weighted — the spec's default (w1=0.65, w2=0.35) favors the
// raw speed-derived reading and uses JF as a smoothing filter.
func FuseTTI(ttiRaw, ttiFromJF, weightRaw, weightJF float64) float64 {
	return round3(weightRaw*ttiRaw + weightJF*ttiFromJF)
}

// InferZ suy diễn hệ số sử dụng khả năng thông hành Z từ TTI qua mô hình
// BPR nghịch đảo hiệu chuẩn: TTI = 1 + alpha·Z^beta ⇒ Z = ((TTI-1)/alpha)^(1/beta).
// Returns 0 for TTI ≤ 1.0 (free flow, no excess delay to attribute to
// over-capacity demand).
func InferZ(tti, alpha, beta float64) float64 {
	if tti <= 1.0 || alpha <= 0 || beta <= 0 {
		return 0.0
	}
	return round3(math.Pow((tti-1.0)/alpha, 1.0/beta))
}

// ConsistencyFlag reports whether an actually-reported JamFactor disagrees
// with the JamFactor implied by an independently speed-derived TTI by more
// than threshold — both numbers describe the same congestion from two
// different raw signals, so a large mismatch usually points to a noisy
// speed/GPS sample rather than a genuine anomaly.
func ConsistencyFlag(jfActual, jfImplied, threshold float64) bool {
	return math.Abs(jfActual-jfImplied) > threshold
}

// DetectBottleneck reports whether jamFactors — one road's own subSegments
// — span more than threshold. A wide spread within an otherwise-uniform
// road points to a localized chokepoint (queue spillback from a downstream
// intersection, a lane drop) rather than even congestion along the whole
// length.
func DetectBottleneck(jamFactors []float64, threshold float64) bool {
	if len(jamFactors) < 2 {
		return false
	}
	minV, maxV := jamFactors[0], jamFactors[0]
	for _, v := range jamFactors[1:] {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	return maxV-minV >= threshold
}

func round3(v float64) float64 {
	return math.Round(v*1000) / 1000
}
