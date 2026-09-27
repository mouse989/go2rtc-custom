package traveltime

import (
	"math"
	"net/http"
	"strconv"
	"time"
)

// Short-term travel-time forecasting (AI forecast — phase 2).
//
// Method: seasonal Holt's linear exponential smoothing with a multiplicative
// seasonal index taken from the historical "typical profile" (average TTI per
// time slot over the last N same-weekdays). The slot length follows the
// travel-time collection interval (Config.IntervalMin) unless the caller asks
// for another one, so a 10-minute collector gets a 10-minute forecast grid. This is robust to the sparse /
// irregular sampling of travel-time logs:
//
//	seasonal[slot] = typicalProfile[slot] / mean(typicalProfile)
//	deseasonalised y'[t] = observed[t] / seasonal[slot(t)]
//	Holt(level L, damped trend T) is smoothed over y' respecting slot spacing
//	forecast(h) = (L + (φ+…+φ^h)·T) · seasonal[slot(now)+h]
//	band = forecast ± z · σ_resid · seasonal · √h     (≈80% interval)
//
// Everything runs in-process in Go (the travel-time logs live here), so no
// extra Python service or heavy stats dependency is needed.

const (
	fcMinSlotMin    = 1     // finest allowed slot
	fcMaxSlotMin    = 60    // coarsest allowed slot
	fcBaseWeeks     = 4     // same-weekdays averaged for the seasonal profile
	fcAlpha         = 0.5   // Holt level smoothing
	fcBeta          = 0.2   // Holt trend smoothing
	fcZ80           = 1.282 // ≈80% confidence band
	fcMinObs        = 3     // need this many measured slots today to model
	fcMaxHorizonMin = 120   // cap forecast at 2h ahead
	fcProfileGapMin = 60    // interpolate profile holes up to this wide
	fcTTIFloor      = 1.0   // free-flow lower bound for TTI
	fcPhi           = 0.9   // damped-trend factor (traffic mean-reverts to the profile)
)

// slotsPerDay returns how many slots of slotMin minutes cover a day (the last
// one may be shorter when slotMin doesn't divide 1440).
func slotsPerDay(slotMin int) int {
	return (24*60 + slotMin - 1) / slotMin
}

// dampedTrendSum returns φ + φ² + … + φ^h = φ(1-φ^h)/(1-φ). A damped trend
// (Gardner–McKenzie) stops the linear trend from over-extrapolating at longer
// horizons — important for traffic, which reverts toward its typical level
// rather than rising without bound.
func dampedTrendSum(h int) float64 {
	if fcPhi >= 1 {
		return float64(h)
	}
	return fcPhi * (1 - math.Pow(fcPhi, float64(h))) / (1 - fcPhi)
}

// ForecastPoint is a single predicted slot for a route.
type ForecastPoint struct {
	Slot         int     `json:"slot"`         // slot index of the prediction (see RouteForecast.SlotMin)
	MinutesAhead int     `json:"minutesAhead"` // minutes from "now"
	TTI          float64 `json:"tti"`          // point forecast
	Lo           float64 `json:"lo"`           // lower confidence bound
	Hi           float64 `json:"hi"`           // upper confidence bound
}

// RouteForecast is the forecast bundle for one route.
type RouteForecast struct {
	RouteID      string          `json:"routeId"`
	Name         string          `json:"name"`
	CurrentSlot  int             `json:"currentSlot"`
	SlotMin      int             `json:"slotMin"` // minutes per slot
	WeeksUsed    int             `json:"weeksUsed"`
	Method       string          `json:"method"`                 // "holt-seasonal" | "profile" | "none"
	HolidayLabel string          `json:"holidayLabel,omitempty"` // non-empty on holiday/break days
	Points       []ForecastPoint `json:"points"`
}

// slotOfTS converts an RFC3339(Nano) timestamp to a local-time slot index.
func slotOfTS(ts string, slotMin int) int {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		if t, err = time.Parse(time.RFC3339, ts); err != nil {
			return -1
		}
	}
	lt := t.In(loc())
	return (lt.Hour()*60 + lt.Minute()) / slotMin
}

// slotMeans buckets a day's entries for one route into per-slot mean TTI.
func slotMeans(entries []LogEntry, routeID string, slotMin int) ([]float64, []int) {
	nSlots := slotsPerDay(slotMin)
	sum := make([]float64, nSlots)
	n := make([]int, nSlots)
	for _, e := range entries {
		if e.RouteID != routeID || e.TTI <= 0 {
			continue
		}
		s := slotOfTS(e.Timestamp, slotMin)
		if s < 0 || s >= nSlots {
			continue
		}
		sum[s] += e.TTI
		n[s]++
	}
	mean := make([]float64, nSlots)
	for i := 0; i < nSlots; i++ {
		if n[i] > 0 {
			mean[i] = sum[i] / float64(n[i])
		}
	}
	return mean, n
}

// forecastRoute runs the seasonal-Holt model for one route.
//
//	todayMean[slot]   – today's mean TTI per slot (0 = no data)
//	profile[slot]     – typical mean TTI per slot, profileN = #days contributing
//	currentSlot       – slot index of "now"
//	horizon           – number of future slots to predict
func forecastRoute(id, name string, todayMean, profile []float64, profileN []int,
	weeksUsed, currentSlot, horizon, slotMin int) RouteForecast {

	fcSlots := len(todayMean)
	out := RouteForecast{RouteID: id, Name: name, CurrentSlot: currentSlot, SlotMin: slotMin, WeeksUsed: weeksUsed, Method: "none"}

	// Seasonal index from the typical profile.
	var pSum float64
	var pCnt int
	for i := 0; i < fcSlots; i++ {
		if profileN[i] > 0 {
			pSum += profile[i]
			pCnt++
		}
	}
	hasProfile := pCnt > 0
	pMean := 1.0
	if hasProfile {
		pMean = pSum / float64(pCnt)
	}
	seasonal := func(slot int) float64 {
		if slot >= 0 && slot < fcSlots && profileN[slot] > 0 && pMean > 0 {
			return profile[slot] / pMean
		}
		return 1.0 // no seasonal info → neutral
	}

	// Collect today's observed slots in chronological order up to now.
	firstObs := -1
	obsCount := 0
	for i := 0; i <= currentSlot && i < fcSlots; i++ {
		if todayMean[i] > 0 {
			if firstObs < 0 {
				firstObs = i
			}
			obsCount++
		}
	}

	// Not enough of today's data to fit Holt → fall back to the typical profile.
	if obsCount < fcMinObs {
		if !hasProfile {
			return out
		}
		out.Method = "profile"
		for h := 1; h <= horizon; h++ {
			slot := currentSlot + h
			if slot >= fcSlots {
				break
			}
			if profileN[slot] == 0 {
				continue
			}
			tti := math.Max(fcTTIFloor, profile[slot])
			band := 0.15 * tti * math.Sqrt(float64(h)) // wide, data-poor band
			out.Points = append(out.Points, ForecastPoint{
				Slot: slot, MinutesAhead: h * slotMin, TTI: round2(tti),
				Lo: round2(math.Max(fcTTIFloor, tti-band)), Hi: round2(tti + band),
			})
		}
		return out
	}

	// Holt's linear smoothing over the deseasonalised series, walking every slot
	// from the first observation to now so that gaps advance the trend correctly.
	var level, trend float64
	inited := false
	var resid []float64
	for i := firstObs; i <= currentSlot; i++ {
		predict := level + trend // one-step-ahead before seeing slot i
		if todayMean[i] > 0 {
			y := todayMean[i] / seasonal(i)
			if !inited {
				level, trend, inited = y, 0, true
				continue
			}
			resid = append(resid, y-predict)
			prevLevel := level
			level = fcAlpha*y + (1-fcAlpha)*(level+trend)
			trend = fcBeta*(level-prevLevel) + (1-fcBeta)*trend
		} else if inited {
			// No observation: roll the state forward (predict-only step).
			level = level + trend
		}
	}

	sigma := stddev(resid)
	out.Method = "holt-seasonal"
	for h := 1; h <= horizon; h++ {
		slot := currentSlot + h
		if slot >= fcSlots {
			break
		}
		base := (level + dampedTrendSum(h)*trend) * seasonal(slot)
		if base <= 0 {
			continue
		}
		tti := math.Max(fcTTIFloor, base)
		band := fcZ80 * sigma * seasonal(slot) * math.Sqrt(float64(h))
		out.Points = append(out.Points, ForecastPoint{
			Slot: slot, MinutesAhead: h * slotMin, TTI: round2(tti),
			Lo: round2(math.Max(fcTTIFloor, tti-band)), Hi: round2(tti + band),
		})
	}
	return out
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func stddev(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	var m float64
	for _, x := range xs {
		m += x
	}
	m /= float64(len(xs))
	var s float64
	for _, x := range xs {
		d := x - m
		s += d * d
	}
	return math.Sqrt(s / float64(len(xs)-1))
}

// fillProfileGaps linearly interpolates empty profile slots (n == 0) that sit
// between two measured slots at most maxGap slots apart. Past days collected at
// a coarser interval (e.g. 15 min before the collector was switched to 10) only
// populate some slots of a finer grid; without this the seasonal index would
// fall back to neutral 1.0 on every other slot and make the forecast zig-zag.
// Filled slots get n = 1 so they count as (weak) profile data.
func fillProfileGaps(profile []float64, n []int, maxGap int) {
	prev := -1
	for i := range profile {
		if n[i] == 0 {
			continue
		}
		if prev >= 0 && i-prev > 1 && i-prev <= maxGap {
			for j := prev + 1; j < i; j++ {
				f := float64(j-prev) / float64(i-prev)
				profile[j] = profile[prev] + f*(profile[i]-profile[prev])
				n[j] = 1
			}
		}
		prev = i
	}
}

// parseSlotMin returns the slot length for a forecast request: ?slotMin= if
// given, else the configured collection interval, clamped to a sane range.
func parseSlotMin(r *http.Request) int {
	slotMin := IntervalMin()
	if v, err := strconv.Atoi(r.URL.Query().Get("slotMin")); err == nil && v > 0 {
		slotMin = v
	}
	if slotMin < fcMinSlotMin {
		slotMin = fcMinSlotMin
	} else if slotMin > fcMaxSlotMin {
		slotMin = fcMaxSlotMin
	}
	return slotMin
}

// GET /api/traveltime/forecast?slotMin=10&horizonMin=120   (or legacy ?horizon=N slots)
// Returns per-route short-term TTI forecasts with ≈80% confidence bands.
func handleForecast(w http.ResponseWriter, r *http.Request) {
	if !requireLogin(w, r) {
		return
	}
	q := r.URL.Query()
	slotMin := parseSlotMin(r)
	nSlots := slotsPerDay(slotMin)

	horizon := (60 + slotMin - 1) / slotMin // default ≈1h
	if v, err := strconv.Atoi(q.Get("horizonMin")); err == nil && v > 0 {
		horizon = (v + slotMin - 1) / slotMin
	} else if v, err := strconv.Atoi(q.Get("horizon")); err == nil && v > 0 {
		horizon = v
	}
	if maxH := fcMaxHorizonMin / slotMin; horizon > maxH {
		horizon = maxH
	}
	if horizon < 1 {
		horizon = 1
	}

	now := time.Now().In(loc())
	currentSlot := (now.Hour()*60 + now.Minute()) / slotMin

	// Tier A: Vietnam/HCMC holiday calendar — classify today.
	todayKind, holidayLabel := HolidayKind(now)

	// Load today + the last N same-weekdays (one file read each, reused across routes).
	todayEntries, _ := getLogs(now.Format("2006-01-02"), 0)
	type pastDay struct {
		date    time.Time
		entries []LogEntry
	}
	pastDays := make([]pastDay, 0, fcBaseWeeks)
	weeksUsed := 0
	for wk := 1; wk <= fcBaseWeeks; wk++ {
		pd := now.AddDate(0, 0, -7*wk)
		es, _ := getLogs(pd.Format("2006-01-02"), 0)
		if len(es) > 0 {
			weeksUsed++
		}
		pastDays = append(pastDays, pastDay{date: pd, entries: es})
	}

	maxGap := fcProfileGapMin / slotMin
	routes := listRoutes()
	out := make([]RouteForecast, 0, len(routes))
	for _, rt := range routes {
		todayMean, _ := slotMeans(todayEntries, rt.ID, slotMin)

		// Typical profile = average across past same-weekdays.
		// Skip past holiday days so the baseline stays "normal weekday" traffic.
		pSum := make([]float64, nSlots)
		pCnt := make([]int, nSlots)
		for _, pd := range pastDays {
			if len(pd.entries) == 0 {
				continue
			}
			// Tier A: exclude past major-holiday days from the normal profile.
			if IsHolidayDate(pd.date) {
				continue
			}
			mean, n := slotMeans(pd.entries, rt.ID, slotMin)
			for i := 0; i < nSlots; i++ {
				if n[i] > 0 {
					pSum[i] += mean[i]
					pCnt[i]++
				}
			}
		}
		profile := make([]float64, nSlots)
		for i := 0; i < nSlots; i++ {
			if pCnt[i] > 0 {
				profile[i] = pSum[i] / float64(pCnt[i])
			}
		}
		fillProfileGaps(profile, pCnt, maxGap)

		// Tier A: scale the profile for today's holiday/break context.
		if todayKind != hkNone {
			for i := 0; i < nSlots; i++ {
				if profile[i] > 0 {
					profile[i] *= HolidayProfileScale(todayKind, i*slotMin/60)
				}
			}
		}

		rf := forecastRoute(rt.ID, rt.Name, todayMean, profile, pCnt, weeksUsed, currentSlot, horizon, slotMin)
		rf.HolidayLabel = holidayLabel
		out = append(out, rf)
	}
	writeJSON(w, out)
}
