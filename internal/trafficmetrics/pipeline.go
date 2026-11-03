package trafficmetrics

// pipeline.go — the two composite entry points callers actually use,
// stitching metrics.go's pure functions together per Config's calibration.
// Two separate shapes because the two existing data sources this feeds
// carry different inputs (see the planning discussion that preceded this):
// internal/traffic's Flow Capture readings have speed+freeFlow+JamFactor
// all from the same API response (ComputeFlow, the full pipeline);
// internal/traveltime's route legs only ever have TTI, no co-located JF
// (ComputeRouteLeg, Z/LOS from TTI alone — no fusion, no anomaly/bottleneck
// checks, which need a JF reading neither path has per-leg).

// FlowResult is everything derived from one flow-capture reading (a whole
// result, or one of its subSegments) beyond the raw speed/freeFlow/JF
// already stored — TTI from both signals, the fused TTI, Z/LOS from the
// fused TTI, and the cross-check anomaly flag (bottleneck is NOT here: it's
// a property of a whole result's subSegments together, not one reading —
// see DetectBottleneck, called separately per result).
type FlowResult struct {
	TTIRaw    float64
	TTIFromJF float64
	TTIFused  float64
	Z         float64
	ZPercent  float64
	LOS       LOSLevel
	Anomaly   bool
}

// ComputeFlow runs the full pipeline for one flow-capture reading.
func (c Config) ComputeFlow(speedMps, freeFlowMps, jf float64) FlowResult {
	ttiRaw := CalculateTTIFromSpeed(speedMps, freeFlowMps)
	ttiFromJF := EstimateTTIFromJF(jf)
	ttiFused := FuseTTI(ttiRaw, ttiFromJF, c.FusionWeightTTI, c.FusionWeightJF)
	z := InferZ(ttiFused, c.Alpha, c.Beta)
	zPercent := round3(z * 100)
	impliedJF := ImpliedJFFromTTI(ttiRaw)
	return FlowResult{
		TTIRaw: ttiRaw, TTIFromJF: ttiFromJF, TTIFused: ttiFused,
		Z: z, ZPercent: zPercent, LOS: ClassifyLOS(zPercent, c.LOSLevels),
		Anomaly: ConsistencyFlag(jf, impliedJF, c.AnomalyJFDelta),
	}
}

// RouteLegResult is the TTI-only path's output — just Z/LOS, since nothing
// else (fused TTI, anomaly) is computable without a co-located JF.
type RouteLegResult struct {
	Z        float64
	ZPercent float64
	LOS      LOSLevel
}

// ComputeRouteLeg runs the reduced pipeline for one traveltime route leg,
// whose TTI already is the only signal available (no speed/freeFlow/JF).
func (c Config) ComputeRouteLeg(tti float64) RouteLegResult {
	z := InferZ(tti, c.Alpha, c.Beta)
	zPercent := round3(z * 100)
	return RouteLegResult{Z: z, ZPercent: zPercent, LOS: ClassifyLOS(zPercent, c.LOSLevels)}
}
