package trafficmetrics

// pipeline.go — the two composite entry points callers actually use,
// stitching metrics.go's pure functions together per Config's calibration.
// Two separate shapes because the two existing data sources this feeds
// carry different inputs: internal/traffic's Flow Capture readings have
// speed+freeFlow+JamFactor all from the same API response (ComputeFlow,
// the full pipeline, fuses TTI from both signals before classifying LOS);
// internal/traveltime's route legs only ever have TTI, no co-located JF
// (ComputeRouteLeg — LOS straight from that TTI, no fusion, no anomaly
// check, which needs a JF reading neither path has per-leg).

// FlowResult is everything derived from one flow-capture reading (a whole
// result, or one of its subSegments) beyond the raw speed/freeFlow/JF
// already stored — TTI from both signals, the fused TTI, its LOS, and the
// cross-check anomaly flag (bottleneck is NOT here: it's a property of a
// whole result's subSegments together, not one reading — see
// DetectBottleneck, called separately per result).
type FlowResult struct {
	TTIRaw    float64
	TTIFromJF float64
	TTIFused  float64
	LOS       LOSLevel
	Anomaly   bool
}

// ComputeFlow runs the full pipeline for one flow-capture reading.
func (c Config) ComputeFlow(speedMps, freeFlowMps, jf float64) FlowResult {
	ttiRaw := CalculateTTIFromSpeed(speedMps, freeFlowMps)
	ttiFromJF := EstimateTTIFromJF(jf)
	ttiFused := FuseTTI(ttiRaw, ttiFromJF, c.FusionWeightTTI, c.FusionWeightJF)
	impliedJF := ImpliedJFFromTTI(ttiRaw)
	return FlowResult{
		TTIRaw: ttiRaw, TTIFromJF: ttiFromJF, TTIFused: ttiFused,
		LOS:     ClassifyLOS(ttiFused, c.LOSLevels),
		Anomaly: ConsistencyFlag(jf, impliedJF, c.AnomalyJFDelta),
	}
}

// ComputeRouteLeg classifies one traveltime route leg's LOS straight from
// its own TTI (the only signal available — no speed/freeFlow/JF per leg).
func (c Config) ComputeRouteLeg(tti float64) LOSLevel {
	return ClassifyLOS(tti, c.LOSLevels)
}
