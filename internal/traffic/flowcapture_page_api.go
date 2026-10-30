package traffic

// flowcapture_page_api.go — read-only API for www/flowcapture.html (the map
// view of Flow Capture research data). Separate from
// /api/traffic/flow-capture/* (admin-only, the Traffic Monitor tool's own
// embedded viewer in www/traffic.html) because this page is gated by its
// own grantable tab (auth.TabFlowCapture), not an admin-only role check —
// same "a page's own tab must gate its own API, not borrow a stricter
// gate from another page" principle internal/trafficinsight's API already
// follows. Both surfaces read the same underlying flow_capture_db.go
// functions; only the HTTP gate differs.

import (
	"net/http"
	"strconv"

	"github.com/AlexxIT/go2rtc/internal/auth"
)

func registerFlowCapturePageHandlers() {
	http.HandleFunc("/api/flowcapture/scans", handleFlowCapturePageScans)
	http.HandleFunc("/api/flowcapture/geometry", handleFlowCapturePageGeometry)
	http.HandleFunc("/api/flowcapture/latest", handleFlowCapturePageLatest)
}

// requireFlowCaptureViewer only checks the caller is authenticated — the
// real restriction (must hold TabFlowCapture, or be admin) already ran in
// internal/auth/middleware.go's tabExtraPaths[TabFlowCapture] before the
// request reaches here, same convention trafficinsight/api.go documents.
func requireFlowCaptureViewer(w http.ResponseWriter, r *http.Request) bool {
	_, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

// handleFlowCapturePageScans  GET /api/flowcapture/scans?limit=N
// Lists the most recent captured scans, newest first — same data
// handleFlowCaptureScans (admin-only) returns, under this page's own gate.
func handleFlowCapturePageScans(w http.ResponseWriter, r *http.Request) {
	if !requireFlowCaptureViewer(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	scans, err := listFlowScans(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, scans)
}

// flowGeometrySubSegment/Link/Result mirror flowSubSegment/flowLink/
// flowResult (flow_capture_parse.go) with speeds pre-converted to km/h —
// the unit the map/legend actually renders in — so the frontend never has
// to know about the raw m/s fields.
type flowGeometrySubSegment struct {
	LengthM        float64 `json:"lengthM"`
	SpeedKmh       float64 `json:"speedKmh"`
	FreeFlowKmh    float64 `json:"freeFlowKmh"`
	JamFactor      float64 `json:"jamFactor"`
	HasJamTendency bool    `json:"hasJamTendency"`
	JamTendency    float64 `json:"jamTendency,omitempty"`
	Confidence     float64 `json:"confidence"`
	Traversability string  `json:"traversability"`
}

type flowGeometryLink struct {
	Points          [][2]float64 `json:"points"` // [lat,lng]
	LengthM         float64      `json:"lengthM"`
	FunctionalClass int          `json:"functionalClass"`
}

type flowGeometryResult struct {
	Description    string                   `json:"description"`
	LengthM        float64                  `json:"lengthM"`
	Links          []flowGeometryLink       `json:"links"`
	SpeedKmh       float64                  `json:"speedKmh"`
	FreeFlowKmh    float64                  `json:"freeFlowKmh"`
	JamFactor      float64                  `json:"jamFactor"`
	HasJamTendency bool                     `json:"hasJamTendency"`
	JamTendency    float64                  `json:"jamTendency,omitempty"`
	Confidence     float64                  `json:"confidence"`
	Traversability string                   `json:"traversability"`
	SubSegments    []flowGeometrySubSegment `json:"subSegments,omitempty"`
}

type flowGeometryResponse struct {
	ScanID        int64                `json:"scanId"`
	RegionName    string               `json:"regionName"`
	ScannedAt     string               `json:"scannedAt"`
	SourceUpdated string               `json:"sourceUpdated"`
	Results       []flowGeometryResult `json:"results"`
}

// resultsToGeometry converts parsed flowResults (flow_capture_parse.go) to
// the km/h-converted shape the map actually renders, shared by
// handleFlowCapturePageGeometry and handleFlowCapturePageLatest.
func resultsToGeometry(parsed []flowResult) []flowGeometryResult {
	out := make([]flowGeometryResult, 0, len(parsed))
	for _, fr := range parsed {
		gr := flowGeometryResult{
			Description: fr.Description, LengthM: fr.LengthM,
			SpeedKmh: fr.SpeedMps * 3.6, FreeFlowKmh: fr.FreeFlowMps * 3.6,
			JamFactor: fr.JamFactor, HasJamTendency: fr.HasJamTendency, JamTendency: fr.JamTendency,
			Confidence: fr.Confidence, Traversability: fr.Traversability,
		}
		for _, lk := range fr.Links {
			gr.Links = append(gr.Links, flowGeometryLink{Points: lk.Points, LengthM: lk.LengthM, FunctionalClass: lk.FunctionalClass})
		}
		for _, ss := range fr.SubSegments {
			gr.SubSegments = append(gr.SubSegments, flowGeometrySubSegment{
				LengthM: ss.LengthM, SpeedKmh: ss.SpeedMps * 3.6, FreeFlowKmh: ss.FreeFlowMps * 3.6,
				JamFactor: ss.JamFactor, HasJamTendency: ss.HasJamTendency, JamTendency: ss.JamTendency,
				Confidence: ss.Confidence, Traversability: ss.Traversability,
			})
		}
		out = append(out, gr)
	}
	return out
}

// handleFlowCapturePageGeometry  GET /api/flowcapture/geometry?scanId=N
// Re-parses one scan's raw_json (flow_capture_db.go stores it verbatim) and
// returns the FULL geometry every result/link/subSegment carries — not
// just the first/last point flow_segments' columns keep — so the map can
// draw every link's real polyline and split it at subSegment boundaries.
func handleFlowCapturePageGeometry(w http.ResponseWriter, r *http.Request) {
	if !requireFlowCaptureViewer(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	scanID, err := strconv.ParseInt(r.URL.Query().Get("scanId"), 10, 64)
	if err != nil {
		http.Error(w, "scanId is required", http.StatusBadRequest)
		return
	}
	meta, err := getFlowScan(scanID)
	if err != nil {
		http.Error(w, "scan not found", http.StatusNotFound)
		return
	}

	raw, err := rawFlowScanJSON(scanID)
	if err != nil {
		http.Error(w, "not found: "+err.Error(), http.StatusNotFound)
		return
	}
	parsed, _, err := parseFlowResults([]byte(raw))
	if err != nil {
		http.Error(w, "stored scan no longer parses: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, flowGeometryResponse{
		ScanID: scanID, RegionName: meta.RegionName, ScannedAt: meta.ScannedAt, SourceUpdated: meta.SourceUpdated,
		Results: resultsToGeometry(parsed),
	})
}

// flowLatestRegion is one region's own latest captured scan, in the
// combined /api/flowcapture/latest response.
type flowLatestRegion struct {
	ScanID        int64  `json:"scanId"`
	RegionName    string `json:"regionName"`
	ScannedAt     string `json:"scannedAt"`
	SourceUpdated string `json:"sourceUpdated"`
}

type flowLatestResponse struct {
	Regions []flowLatestRegion   `json:"regions"` // metadata per region, for the "as of" status line
	Results []flowGeometryResult `json:"results"` // every region's own latest results, concatenated
}

// handleFlowCapturePageLatest  GET /api/flowcapture/latest
// Combines every scanned region's own most recent capture into one
// response — regions are purely a bbox-scanning control (how the admin
// splits the area queried), not a meaningful way to split what the map
// shows, so the map always displays the latest data from every region
// together rather than making the viewer pick a region or a specific past
// scan. Call again (the page polls this) to pick up newly captured scans;
// each call reflects whatever is newest at that moment, replacing what was
// shown before — there is no history browsing here, see
// handleFlowCapturePageGeometry for that.
func handleFlowCapturePageLatest(w http.ResponseWriter, r *http.Request) {
	if !requireFlowCaptureViewer(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	scans, err := latestFlowScanPerRegion()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := flowLatestResponse{Regions: []flowLatestRegion{}, Results: []flowGeometryResult{}}
	for _, s := range scans {
		resp.Regions = append(resp.Regions, flowLatestRegion{
			ScanID: s.ID, RegionName: s.RegionName, ScannedAt: s.ScannedAt, SourceUpdated: s.SourceUpdated,
		})
		raw, err := rawFlowScanJSON(s.ID)
		if err != nil {
			continue // a region whose stored row can't be read is skipped, not fatal to the others
		}
		parsed, _, err := parseFlowResults([]byte(raw))
		if err != nil {
			continue
		}
		resp.Results = append(resp.Results, resultsToGeometry(parsed)...)
	}
	writeJSON(w, resp)
}
