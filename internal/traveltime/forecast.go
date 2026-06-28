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
// 15-min slot over the last N same-weekdays). This is robust to the sparse /
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
	fcSlotMin    = 15                  // minutes per slot (matches the dashboard)
	fcSlots      = 24 * 60 / fcSlotMin // 96 slots/day
	fcBaseWeeks  = 4                   // same-weekdays averaged for the seasonal profile
	fcAlpha      = 0.5                 // Holt level smoothing
	fcBeta       = 0.2                 // Holt trend smoothing
	fcZ80        = 1.282               // ≈80% confidence band
	fcMinObs     = 3                   // need this many measured slots today to model
	fcMaxHorizon = 8                   // cap forecast at 8 slots (2h)
	fcTTIFloor   = 1.0                 // free-flow lower bound for TTI
	fcPhi        = 0.9                 // damped-trend factor (traffic mean-reverts to the profile)
)

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
	Slot         int     `json:"slot"`         // 0..95 slot index of the prediction
	MinutesAhead int     `json:"minutesAhead"` // minutes from "now"
	TTI          float64 `json:"tti"`          // point forecast
	Lo           float64 `json:"lo"`           // lower confidence bound
	Hi           float64 `json:"hi"`           // upper confidence bound
}

// RouteForecast is the forecast bundle for one route.
type RouteForecast struct {
	RouteID     string          `json:"routeId"`
	Name        string          `json:"name"`
	CurrentSlot int             `json:"currentSlot"`
	WeeksUsed   int             `json:"weeksUsed"`
	Method      string          `json:"method"` // "holt-seasonal" | "profile" | "none"
	Points      []ForecastPoint `json:"points"`
}

// slotOfTS converts an RFC3339(Nano) timestamp to a local-time 15-min slot.
func slotOfTS(ts string) int {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		if t, err = time.Parse(time.RFC3339, ts); err != nil {
			return -1
		}
	}
	lt := t.In(loc())
	return (lt.Hour()*60 + lt.Minute()) / fcSlotMin
}

// slotMeans buckets a day's entries for one route into per-slot mean TTI.
func slotMeans(entries []LogEntry, routeID string) ([fcSlots]float64, [fcSlots]int) {
	var sum [fcSlots]float64
	var n [fcSlots]int
	for _, e := range entries {
		if e.RouteID != routeID || e.TTI <= 0 {
			continue
		}
		s := slotOfTS(e.Timestamp)
		if s < 0 || s >= fcSlots {
			continue
		}
		sum[s] += e.TTI
		n[s]++
	}
	var mean [fcSlots]float64
	for i := 0; i < fcSlots; i++ {
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
func forecastRoute(id, name string, todayMean [fcSlots]float64,
	profile [fcSlots]float64, profileN [fcSlots]int, weeksUsed, currentSlot, horizon int) RouteForecast {

	out := RouteForecast{RouteID: id, Name: name, CurrentSlot: currentSlot, WeeksUsed: weeksUsed, Method: "none"}

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
				Slot: slot, MinutesAhead: h * fcSlotMin, TTI: round2(tti),
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
			Slot: slot, MinutesAhead: h * fcSlotMin, TTI: round2(tti),
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

// GET /api/traveltime/forecast?horizon=4
// Returns per-route short-term TTI forecasts with ≈80% confidence bands.
func handleForecast(w http.ResponseWriter, r *http.Request) {
	if !requireLogin(w, r) {
		return
	}
	horizon := 4
	if hs := r.URL.Query().Get("horizon"); hs != "" {
		if v, err := strconv.Atoi(hs); err == nil && v > 0 {
			horizon = v
		}
	}
	if horizon > fcMaxHorizon {
		horizon = fcMaxHorizon
	}

	now := time.Now().In(loc())
	currentSlot := (now.Hour()*60 + now.Minute()) / fcSlotMin

	// Load today + the last N same-weekdays (one file read each, reused across routes).
	todayEntries, _ := getLogs(now.Format("2006-01-02"), 0)
	pastDays := make([][]LogEntry, 0, fcBaseWeeks)
	weeksUsed := 0
	for wk := 1; wk <= fcBaseWeeks; wk++ {
		d := now.AddDate(0, 0, -7*wk).Format("2006-01-02")
		es, _ := getLogs(d, 0)
		if len(es) > 0 {
			weeksUsed++
		}
		pastDays = append(pastDays, es)
	}

	routes := listRoutes()
	out := make([]RouteForecast, 0, len(routes))
	for _, rt := range routes {
		todayMean, _ := slotMeans(todayEntries, rt.ID)

		// Typical profile = average across the past same-weekdays.
		var pSum [fcSlots]float64
		var pCnt [fcSlots]int
		for _, es := range pastDays {
			if len(es) == 0 {
				continue
			}
			mean, n := slotMeans(es, rt.ID)
			for i := 0; i < fcSlots; i++ {
				if n[i] > 0 {
					pSum[i] += mean[i]
					pCnt[i]++
				}
			}
		}
		var profile [fcSlots]float64
		for i := 0; i < fcSlots; i++ {
			if pCnt[i] > 0 {
				profile[i] = pSum[i] / float64(pCnt[i])
			}
		}

		out = append(out, forecastRoute(rt.ID, rt.Name, todayMean, profile, pCnt, weeksUsed, currentSlot, horizon))
	}
	writeJSON(w, out)
}
