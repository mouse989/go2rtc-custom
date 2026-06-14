package traffic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/auth"
)

// Point represents a jam cluster point with metadata.
type Point struct {
	Lat         float64 `json:"lat"`
	Lng         float64 `json:"lng"`
	JamFactor   float64 `json:"jamFactor"`
	Speed       float64 `json:"speed"`       // km/h from traffic API
	MemberCount int     `json:"memberCount"` // number of raw points in cluster
	RegionName  string  `json:"regionName"`
	Label       string  `json:"label"`
	Area        string  `json:"area"`
	ScannedAt   int64   `json:"scannedAt"`
}

// LogEntry is a single log line for the UI.
type LogEntry struct {
	Time  string `json:"time"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

type geoResult struct {
	Label string
	Area  string
}

var (
	lastFilteredMu sync.RWMutex
	lastFiltered   []Point

	geoCache   = map[string]geoResult{}
	geoCacheMu sync.RWMutex

	scanStateMu   sync.RWMutex
	lastScanAt    time.Time
	nextScanAt    time.Time
	lastRawCnt    int
	lastFilterCnt int
	lastPersistCnt int
	lastErr       string

	logsMu     sync.Mutex
	recentLogs []LogEntry
)

func setScanNext(t time.Time) {
	scanStateMu.Lock()
	nextScanAt = t
	scanStateMu.Unlock()
}

func getScanState() (last, next time.Time, raw, filtered, persistent int, errStr string, running bool) {
	scanStateMu.RLock()
	last = lastScanAt
	next = nextScanAt
	raw = lastRawCnt
	filtered = lastFilterCnt
	persistent = lastPersistCnt
	errStr = lastErr
	scanStateMu.RUnlock()
	running = isRunning()
	return
}

// haversine returns distance in metres between two WGS-84 points.
func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371000.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// pointInPolygon uses ray-casting. coords[i] = [lat, lon].
func pointInPolygon(lat, lon float64, coords [][2]float64) bool {
	n := len(coords)
	inside := false
	j := n - 1
	for i := 0; i < n; i++ {
		xi, yi := coords[i][1], coords[i][0] // lon, lat
		xj, yj := coords[j][1], coords[j][0]
		if ((yi > lat) != (yj > lat)) &&
			(lon < (xj-xi)*(lat-yi)/(yj-yi)+xi) {
			inside = !inside
		}
		j = i
	}
	return inside
}

func bboxOfRegion(r Region) (minLat, minLon, maxLat, maxLon float64) {
	if len(r.Coords) == 0 {
		return
	}
	minLat = r.Coords[0][0]
	maxLat = r.Coords[0][0]
	minLon = r.Coords[0][1]
	maxLon = r.Coords[0][1]
	for _, c := range r.Coords[1:] {
		if c[0] < minLat {
			minLat = c[0]
		}
		if c[0] > maxLat {
			maxLat = c[0]
		}
		if c[1] < minLon {
			minLon = c[1]
		}
		if c[1] > maxLon {
			maxLon = c[1]
		}
	}
	return
}

func fetchTrafficForRegion(apiKey string, r Region) ([]byte, error) {
	minLat, minLon, maxLat, maxLon := bboxOfRegion(r)
	// API expects: bbox=lng_west,lat_south,lng_east,lat_north  (lon first, NOT lat first)
	url := fmt.Sprintf(
		"https://traffic.genfast.vn/api/traffic/flow?bbox=%f,%f,%f,%f",
		minLon, minLat, maxLon, maxLat,
	)
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Apikey", apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// segment represents a road segment with jam factor and shape.
type segment struct {
	JamFactor float64
	Speed     float64      // km/h
	Shape     [][2]float64 // [lat, lon] pairs
}

// normalizeTrafficData parses GeoJSON FeatureCollection, HERE format, flat array, or {segments:[]}.
func normalizeTrafficData(data []byte) ([]segment, error) {
	// Format B & others: object
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err == nil {
		// Format B: GeoJSON FeatureCollection
		if typeVal, ok := raw["type"]; ok {
			var typStr string
			_ = json.Unmarshal(typeVal, &typStr)
			if typStr == "FeatureCollection" {
				return parseGeoJSON(raw)
			}
		}
		// Format A: HERE-style {results:[...]}
		if _, ok := raw["results"]; ok {
			return parseHERE(raw)
		}
		// Format D: {segments:[...]} — recurse on the inner array
		if segRaw, ok := raw["segments"]; ok {
			return normalizeTrafficData(segRaw)
		}
	}

	// Format C: flat JSON array  [...{geometry, jam_factor, ...}]
	var arr []json.RawMessage
	if err := json.Unmarshal(data, &arr); err == nil && len(arr) > 0 {
		return parseFlatArray(arr)
	}

	return nil, fmt.Errorf("unknown traffic data format")
}

func parseGeoJSON(raw map[string]json.RawMessage) ([]segment, error) {
	var features []struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Geometry   struct {
			Type        string          `json:"type"`
			Coordinates json.RawMessage `json:"coordinates"`
		} `json:"geometry"`
	}
	if err := json.Unmarshal(raw["features"], &features); err != nil {
		return nil, err
	}

	var segs []segment
	for _, f := range features {
		// Extract jam factor: try jam_factor, jamFactor, congestion
		var jf float64
		for _, key := range []string{"jam_factor", "jamFactor", "congestion"} {
			if v, ok := f.Properties[key]; ok {
				if json.Unmarshal(v, &jf) == nil {
					break
				}
			}
		}

		// Extract speed
		var spd float64
		for _, key := range []string{"speed", "current_speed", "currentSpeed"} {
			if v, ok := f.Properties[key]; ok {
				_ = json.Unmarshal(v, &spd)
				if spd > 0 {
					break
				}
			}
		}

		// Build shape from geometry — GeoJSON coords are [lon, lat]
		var shape [][2]float64
		switch f.Geometry.Type {
		case "LineString":
			var coords [][2]float64
			if err := json.Unmarshal(f.Geometry.Coordinates, &coords); err != nil {
				continue
			}
			for _, c := range coords {
				shape = append(shape, [2]float64{c[1], c[0]})
			}
		case "MultiLineString":
			var multiCoords [][][2]float64
			if err := json.Unmarshal(f.Geometry.Coordinates, &multiCoords); err != nil {
				continue
			}
			// Flatten all lines into one shape
			for _, line := range multiCoords {
				for _, c := range line {
					shape = append(shape, [2]float64{c[1], c[0]})
				}
			}
		case "Point":
			var coord [2]float64
			if err := json.Unmarshal(f.Geometry.Coordinates, &coord); err != nil {
				continue
			}
			shape = [][2]float64{{coord[1], coord[0]}}
		default:
			continue // skip Polygon, GeometryCollection, etc.
		}

		if len(shape) == 0 {
			continue
		}
		segs = append(segs, segment{JamFactor: jf, Speed: spd, Shape: shape})
	}
	return segs, nil
}

// parseFlatArray handles Format C: a flat JSON array of items with geometry/jam_factor fields.
func parseFlatArray(arr []json.RawMessage) ([]segment, error) {
	var segs []segment
	for _, raw := range arr {
		var item struct {
			JamFactor  *float64        `json:"jam_factor"`
			JamFactor2 *float64        `json:"jamFactor"`
			Congestion *float64        `json:"congestion"`
			Speed      float64         `json:"speed"`
			CurSpeed   float64         `json:"current_speed"`
			Lat        *float64        `json:"lat"`
			Lng        *float64        `json:"lng"`
			Geometry   *struct {
				Type        string          `json:"type"`
				Coordinates json.RawMessage `json:"coordinates"`
			} `json:"geometry"`
			Shape json.RawMessage `json:"shape"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}

		var jf float64
		switch {
		case item.JamFactor != nil:
			jf = *item.JamFactor
		case item.JamFactor2 != nil:
			jf = *item.JamFactor2
		case item.Congestion != nil:
			jf = *item.Congestion
		}

		spd := item.Speed
		if spd == 0 {
			spd = item.CurSpeed
		}

		var shape [][2]float64
		if item.Geometry != nil {
			switch item.Geometry.Type {
			case "LineString":
				var coords [][2]float64
				if json.Unmarshal(item.Geometry.Coordinates, &coords) == nil {
					for _, c := range coords {
						shape = append(shape, [2]float64{c[1], c[0]})
					}
				}
			case "MultiLineString":
				var multi [][][2]float64
				if json.Unmarshal(item.Geometry.Coordinates, &multi) == nil {
					for _, line := range multi {
						for _, c := range line {
							shape = append(shape, [2]float64{c[1], c[0]})
						}
					}
				}
			case "Point":
				var coord [2]float64
				if json.Unmarshal(item.Geometry.Coordinates, &coord) == nil {
					shape = [][2]float64{{coord[1], coord[0]}}
				}
			}
		} else if item.Lat != nil && item.Lng != nil {
			shape = [][2]float64{{*item.Lat, *item.Lng}}
		} else if len(item.Shape) > 0 {
			// shape may be [[lat,lng],...] or [{lat,lng},...]
			var coordArr [][2]float64
			if json.Unmarshal(item.Shape, &coordArr) == nil {
				for _, c := range coordArr {
					shape = append(shape, [2]float64{c[0], c[1]})
				}
			}
		}

		if len(shape) == 0 {
			continue
		}
		segs = append(segs, segment{JamFactor: jf, Speed: spd, Shape: shape})
	}
	return segs, nil
}

func parseHERE(raw map[string]json.RawMessage) ([]segment, error) {
	var results []struct {
		CurrentFlow struct {
			JamFactor float64 `json:"jamFactor"`
			Speed     float64 `json:"speed"`
		} `json:"currentFlow"`
		Location struct {
			Shape struct {
				Links []struct {
					Points []struct {
						Lat float64 `json:"lat"`
						Lng float64 `json:"lng"`
					} `json:"points"`
				} `json:"links"`
			} `json:"shape"`
			// some responses put links directly under location
			Links []struct {
				Points []struct {
					Lat float64 `json:"lat"`
					Lng float64 `json:"lng"`
				} `json:"points"`
			} `json:"links"`
		} `json:"location"`
	}
	if err := json.Unmarshal(raw["results"], &results); err != nil {
		return nil, err
	}

	// addLink is a helper that converts a typed link's points into a segment.
	addLink := func(segs []segment, jf, spd float64, pts []struct {
		Lat float64 `json:"lat"`
		Lng float64 `json:"lng"`
	}) []segment {
		if len(pts) == 0 {
			return segs
		}
		shape := make([][2]float64, len(pts))
		for i, p := range pts {
			shape[i] = [2]float64{p.Lat, p.Lng}
		}
		return append(segs, segment{JamFactor: jf, Speed: spd, Shape: shape})
	}

	var segs []segment
	for _, r := range results {
		jf := r.CurrentFlow.JamFactor
		spd := r.CurrentFlow.Speed

		// Create ONE SEGMENT PER LINK — matches original JS behaviour.
		// Merging all links into one would give a single midpoint for long roads,
		// losing most data points compared to the reference implementation.
		for _, lk := range r.Location.Shape.Links {
			segs = addLink(segs, jf, spd, lk.Points)
		}
		for _, lk := range r.Location.Links {
			segs = addLink(segs, jf, spd, lk.Points)
		}
	}
	return segs, nil
}

// extractPoints computes midpoints of segments, filters by jamFactor and polygon.
func extractPoints(segs []segment, r Region, minJam float64) []Point {
	now := time.Now().Unix()
	var pts []Point
	for _, s := range segs {
		if s.JamFactor < minJam {
			continue
		}
		if len(s.Shape) == 0 {
			continue
		}
		// midpoint
		mid := s.Shape[len(s.Shape)/2]
		lat, lng := mid[0], mid[1]

		if !pointInPolygon(lat, lng, r.Coords) {
			continue
		}
		pts = append(pts, Point{
			Lat:        lat,
			Lng:        lng,
			JamFactor:  s.JamFactor,
			Speed:      s.Speed,
			RegionName: r.Name,
			ScannedAt:  now,
		})
	}
	return pts
}

// clusterPoints greedy-merges points within radiusM of each other.
func clusterPoints(pts []Point, radiusM float64) []Point {
	used := make([]bool, len(pts))
	var result []Point
	for i, p := range pts {
		if used[i] {
			continue
		}
		used[i] = true
		cluster := []Point{p}
		for j := i + 1; j < len(pts); j++ {
			if !used[j] && haversine(p.Lat, p.Lng, pts[j].Lat, pts[j].Lng) <= radiusM {
				cluster = append(cluster, pts[j])
				used[j] = true
			}
		}
		// representative: highest jam factor
		best := cluster[0]
		for _, c := range cluster[1:] {
			if c.JamFactor > best.JamFactor {
				best = c
			}
		}
		best.MemberCount = len(cluster)
		result = append(result, best)
	}
	return result
}

// findPersistent returns points from current that have a match in last within radiusM.
func findPersistent(current, last []Point, radiusM float64) []Point {
	var result []Point
	for _, c := range current {
		for _, l := range last {
			if haversine(c.Lat, c.Lng, l.Lat, l.Lng) <= radiusM {
				result = append(result, c)
				break
			}
		}
	}
	return result
}

// reverseGeocode calls VietMap reverse v4 and caches by "%.3f,%.3f".
func reverseGeocode(lat, lon float64, apiKey string) (label, area string) {
	key := fmt.Sprintf("%.3f,%.3f", lat, lon)

	geoCacheMu.RLock()
	if cached, ok := geoCache[key]; ok {
		geoCacheMu.RUnlock()
		return cached.Label, cached.Area
	}
	geoCacheMu.RUnlock()

	url := fmt.Sprintf(
		"https://maps.vietmap.vn/api/reverse/v4?apikey=%s&lat=%f&lng=%f&display_type=6",
		apiKey, lat, lon,
	)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()

	var result []struct {
		Name     string `json:"name"`
		Address  string `json:"address"`
		District string `json:"district"`
		Province string `json:"province"`
	}
	body, _ := io.ReadAll(resp.Body)
	// VietMap reverse v4 may return an object or array
	if len(body) > 0 && body[0] == '[' {
		_ = json.Unmarshal(body, &result)
	} else {
		var single struct {
			Name     string `json:"name"`
			Address  string `json:"address"`
			District string `json:"district"`
			Province string `json:"province"`
		}
		if json.Unmarshal(body, &single) == nil {
			result = append(result, single)
		}
	}

	if len(result) > 0 {
		label = result[0].Name
		if label == "" {
			label = result[0].Address
		}
		area = result[0].District
		if area == "" {
			area = result[0].Province
		}
	}

	geoCacheMu.Lock()
	geoCache[key] = geoResult{Label: label, Area: area}
	geoCacheMu.Unlock()
	return
}

// enrichPoints reverse-geocodes all points concurrently (max 5 goroutines).
func enrichPoints(pts []Point, vietmapKey string) {
	if vietmapKey == "" || len(pts) == 0 {
		return
	}
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	for i := range pts {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			l, a := reverseGeocode(pts[idx].Lat, pts[idx].Lng, vietmapKey)
			pts[idx].Label = l
			pts[idx].Area = a
		}(i)
	}
	wg.Wait()
}

// addLog appends a log entry (capped at 200) and logs via zerolog.
func addLog(level, msg string) {
	entry := LogEntry{
		Time:  time.Now().Format("15:04:05"),
		Level: level,
		Msg:   msg,
	}

	logsMu.Lock()
	recentLogs = append(recentLogs, entry)
	if len(recentLogs) > 200 {
		recentLogs = recentLogs[len(recentLogs)-200:]
	}
	logsMu.Unlock()

	switch level {
	case "error":
		log.Error().Msg("[traffic] " + msg)
	case "warn":
		log.Warn().Msg("[traffic] " + msg)
	default:
		log.Info().Msg("[traffic] " + msg)
	}
}

func getLogs() []LogEntry {
	logsMu.Lock()
	defer logsMu.Unlock()
	result := make([]LogEntry, len(recentLogs))
	copy(result, recentLogs)
	return result
}

// runScan performs one full traffic scan cycle.
func runScan() error {
	c := getConfig()

	addLog("info", "scan started")

	var allRaw []Point

	// trafficKey: dedicated traffic API key (c.APIKey), fallback to VietMap key
	trafficKey := c.APIKey
	if trafficKey == "" {
		trafficKey = auth.GetSettings().VietmapAPIKey
	}
	// geocodingKey: always use VietMap key for reverse geocoding, fallback to traffic key
	geocodingKey := auth.GetSettings().VietmapAPIKey
	if geocodingKey == "" {
		geocodingKey = c.APIKey
	}

	for _, r := range c.Regions {
		if !r.Enabled || len(r.Coords) == 0 {
			continue
		}
		data, err := fetchTrafficForRegion(trafficKey, r)
		if err != nil {
			addLog("warn", fmt.Sprintf("region %s fetch error: %v", r.Name, err))
			continue
		}
		segs, err := normalizeTrafficData(data)
		if err != nil {
			addLog("warn", fmt.Sprintf("region %s parse error: %v", r.Name, err))
			// Try to unmarshal as raw JSON to get a better error
			var preview []byte
			if len(data) > 200 {
				preview = data[:200]
			} else {
				preview = data
			}
			addLog("warn", fmt.Sprintf("region %s raw response: %s", r.Name, string(preview)))
			continue
		}
		pts := extractPoints(segs, r, c.MinJam)
		allRaw = append(allRaw, pts...)
	}

	filtered := clusterPoints(allRaw, c.ClusterRadius)

	lastFilteredMu.RLock()
	prev := make([]Point, len(lastFiltered))
	copy(prev, lastFiltered)
	lastFilteredMu.RUnlock()

	persistent := findPersistent(filtered, prev, c.PersistRadius)

	// Enrich all unique points
	toEnrich := append(filtered, persistent...)
	enrichPoints(toEnrich, geocodingKey)

	// Copy enrichment back
	enrichMap := map[string]Point{}
	for _, p := range toEnrich {
		k := fmt.Sprintf("%.5f,%.5f", p.Lat, p.Lng)
		enrichMap[k] = p
	}
	for i, p := range filtered {
		k := fmt.Sprintf("%.5f,%.5f", p.Lat, p.Lng)
		if ep, ok := enrichMap[k]; ok {
			filtered[i] = ep
		}
	}
	for i, p := range persistent {
		k := fmt.Sprintf("%.5f,%.5f", p.Lat, p.Lng)
		if ep, ok := enrichMap[k]; ok {
			persistent[i] = ep
		}
	}

	lastFilteredMu.Lock()
	lastFiltered = filtered
	lastFilteredMu.Unlock()

	scanStateMu.Lock()
	lastScanAt = time.Now()
	lastRawCnt = len(allRaw)
	lastFilterCnt = len(filtered)
	lastPersistCnt = len(persistent)
	lastErr = ""
	scanStateMu.Unlock()

	addLog("info", fmt.Sprintf("scan done: raw=%d filtered=%d persistent=%d",
		len(allRaw), len(filtered), len(persistent)))

	// Save scan data to local disk (traffic_data/ next to traffic.json)
	if c.Storage.Enabled {
		if err := saveScanData(c, time.Now(), allRaw, filtered, persistent); err != nil {
			addLog("warn", "storage error: "+err.Error())
		}
	}

	if c.Telegram.Enabled {
		if err := sendTelegramReport(c, allRaw, filtered, persistent); err != nil {
			addLog("warn", "telegram error: "+err.Error())
		}
	}

	if c.Sheets.Enabled && c.Sheets.URL != "" {
		now := time.Now().Unix()
		if err := sendSheetsLog(c.Sheets.URL, now, allRaw, filtered, persistent); err != nil {
			addLog("warn", "sheets error: "+err.Error())
		}
	}

	return nil
}

// sheetsRawPoint matches the format expected by the Google Apps Script.
type sheetsRawPoint struct {
	Lat       float64 `json:"lat"`
	Lng       float64 `json:"lng"`
	JamFactor float64 `json:"jamFactor"`
	Region    string  `json:"region"`
	Speed     float64 `json:"speed"`
}

type sheetsFilteredPoint struct {
	Lat       float64 `json:"lat"`
	Lng       float64 `json:"lng"`
	JamFactor float64 `json:"jamFactor"`
	Label     string  `json:"label"`
	Area      string  `json:"area"`
	Region    string  `json:"region"`
	Members   int     `json:"members"`
}

type sheetsPersistentPoint struct {
	Lat       float64 `json:"lat"`
	Lng       float64 `json:"lng"`
	JamFactor float64 `json:"jamFactor"`
	Label     string  `json:"label"`
	Area      string  `json:"area"`
	Region    string  `json:"region"`
}

// sheetsPayload is the JSON body sent to Google Sheets Apps Script.
// Content-Type must be text/plain;charset=utf-8 to avoid CORS preflight.
type sheetsPayload struct {
	ScannedAt  string                  `json:"scannedAt"` // ISO 8601
	Raw        []sheetsRawPoint        `json:"raw"`
	Filtered   []sheetsFilteredPoint   `json:"filtered"`
	Persistent []sheetsPersistentPoint `json:"persistent"`
}

// buildSheetsPayload converts scan results into the Sheets-style payload.
// The same structure is used for the local daily history files.
func buildSheetsPayload(scannedAt time.Time, raw, filtered, persistent []Point) sheetsPayload {
	rawPts := make([]sheetsRawPoint, len(raw))
	for i, p := range raw {
		rawPts[i] = sheetsRawPoint{
			Lat: p.Lat, Lng: p.Lng, JamFactor: p.JamFactor,
			Region: p.RegionName, Speed: p.Speed,
		}
	}

	filtPts := make([]sheetsFilteredPoint, len(filtered))
	for i, p := range filtered {
		filtPts[i] = sheetsFilteredPoint{
			Lat: p.Lat, Lng: p.Lng, JamFactor: p.JamFactor,
			Label: p.Label, Area: p.Area, Region: p.RegionName,
			Members: p.MemberCount,
		}
	}

	persPts := make([]sheetsPersistentPoint, len(persistent))
	for i, p := range persistent {
		persPts[i] = sheetsPersistentPoint{
			Lat: p.Lat, Lng: p.Lng, JamFactor: p.JamFactor,
			Label: p.Label, Area: p.Area, Region: p.RegionName,
		}
	}

	return sheetsPayload{
		ScannedAt:  scannedAt.UTC().Format(time.RFC3339),
		Raw:        rawPts,
		Filtered:   filtPts,
		Persistent: persPts,
	}
}

func sendSheetsLog(webhookURL string, scannedAt int64, raw, filtered, persistent []Point) error {
	payload := buildSheetsPayload(time.Unix(scannedAt, 0), raw, filtered, persistent)
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	// Google Apps Script /exec can be very slow on cold start (it spins up a
	// new V8 runtime and follows a 302 redirect), often exceeding 15s.
	// Use a generous timeout and retry transient failures with backoff.
	client := &http.Client{Timeout: 60 * time.Second}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewReader(data))
		if err != nil {
			return err
		}
		// CRITICAL: Google Apps Script requires text/plain to bypass CORS preflight
		req.Header.Set("Content-Type", "text/plain;charset=utf-8")

		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 400 {
				if attempt > 1 {
					addLog("info", fmt.Sprintf("sheets: succeeded on attempt %d", attempt))
				}
				return nil
			}
			lastErr = fmt.Errorf("sheets webhook returned %s", resp.Status)
			// 4xx won't be fixed by retrying (bad URL / permissions)
			if resp.StatusCode < 500 {
				return lastErr
			}
		} else {
			lastErr = err
		}

		if attempt < 3 {
			addLog("warn", fmt.Sprintf("sheets attempt %d failed (%v), retrying...", attempt, lastErr))
			time.Sleep(time.Duration(attempt*5) * time.Second)
		}
	}
	return lastErr
}
