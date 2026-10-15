package gis

// api.go — HTTP surface for the GIS data module:
//
//   GET  /api/gis/data                         — Map layer data source: every
//                                                 layer + its features, for
//                                                 rendering. Normal user auth
//                                                 + AllowMapGIS (or admin).
//   GET/POST    /api/gis/layers                — admin-only layer CRUD (list/create).
//   GET/PUT/DELETE /api/gis/layers/{id}        — admin-only, one layer.
//   GET/POST    /api/gis/layers/{id}/features  — admin-only feature CRUD (list/create) within a layer.
//   PUT/DELETE  /api/gis/layers/{id}/features/{featureID} — admin-only, one feature.

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/AlexxIT/go2rtc/internal/auth"
)

func registerHandlers() {
	http.HandleFunc("/api/gis/data", handleData)
	http.HandleFunc("/api/gis/layers", handleLayers)
	http.HandleFunc("/api/gis/layers/", handleLayersSubpath)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func requireGISViewer(w http.ResponseWriter, r *http.Request) bool {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	if user.Role != auth.RoleAdmin && !user.AllowMapGIS {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func requireGISAdmin(w http.ResponseWriter, r *http.Request) bool {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user.Role != auth.RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

// ── GET /api/gis/data (view-only, consolidated for map rendering) ─────

type dataLayerOut struct {
	Layer
	Features []*Feature `json:"features"`
}

func handleData(w http.ResponseWriter, r *http.Request) {
	if !requireGISViewer(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	layers := ListLayers()
	out := make([]dataLayerOut, len(layers))
	for i, l := range layers {
		feats := ListFeatures(l.ID)
		if feats == nil {
			feats = []*Feature{}
		}
		out[i] = dataLayerOut{Layer: *l, Features: feats}
	}
	writeJSON(w, http.StatusOK, out)
}

// ── Layers (admin-only CRUD) ────────────────────────────────────────

type layerReq struct {
	Name                        string `json:"name"`
	Color                       string `json:"color"`
	Icon                        string `json:"icon"`
	DefaultOn                   bool   `json:"default_on"`
	FlagsIncidentCongestionRisk bool   `json:"flags_incident_congestion_risk"`
}

func handleLayers(w http.ResponseWriter, r *http.Request) {
	if !requireGISAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, ListLayers())

	case http.MethodPost:
		var req layerReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		l := &Layer{
			Name: req.Name, Color: req.Color, Icon: req.Icon,
			DefaultOn: req.DefaultOn, FlagsIncidentCongestionRisk: req.FlagsIncidentCongestionRisk,
		}
		if err := CreateLayer(l); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, l)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleLayersSubpath routes everything under /api/gis/layers/... :
//
//	/api/gis/layers/{id}                   → one layer (admin)
//	/api/gis/layers/{id}/features          → feature list/create (admin)
//	/api/gis/layers/{id}/features/{fid}    → one feature (admin)
//	GET /api/gis/layers/{id}/icon          → the layer's custom marker image, if any (any GIS viewer — needed to render the map)
//	DELETE /api/gis/layers/{id}/icon       → clear the custom icon (admin)
//	POST /api/gis/layers/{id}/import       → KML/KMZ import (admin)
func handleLayersSubpath(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/gis/layers/"), "/")
	parts := strings.Split(rest, "/")
	if parts[0] == "" {
		http.Error(w, "layer id required", http.StatusBadRequest)
		return
	}
	layerID := parts[0]

	// The icon GET is the one sub-route a non-admin GIS viewer needs (to
	// render custom markers on the map) — everything else under
	// /layers/{id}/... is layer/feature management, admin-only.
	if len(parts) == 2 && parts[1] == "icon" && r.Method == http.MethodGet {
		if !requireGISViewer(w, r) {
			return
		}
		handleLayerIcon(w, r, layerID)
		return
	}

	if !requireGISAdmin(w, r) {
		return
	}

	switch {
	case len(parts) == 1:
		handleOneLayer(w, r, layerID)
	case len(parts) == 2 && parts[1] == "features":
		handleFeatures(w, r, layerID)
	case len(parts) == 3 && parts[1] == "features":
		handleOneFeature(w, r, layerID, parts[2])
	case len(parts) == 2 && parts[1] == "icon":
		handleLayerIcon(w, r, layerID) // DELETE only at this point (GET already handled above)
	case len(parts) == 2 && parts[1] == "import":
		handleLayerImport(w, r, layerID)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func handleLayerIcon(w http.ResponseWriter, r *http.Request, layerID string) {
	switch r.Method {
	case http.MethodGet:
		data, ct, ok := GetLayerIcon(layerID)
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "private, max-age=300")
		_, _ = w.Write(data)

	case http.MethodDelete:
		if err := ClearLayerIcon(layerID); err != nil {
			status := http.StatusInternalServerError
			if err == errNotFound {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// maxImportUploadBytes bounds the whole multipart body — generous for a
// KMZ carrying dozens of placemarks plus one small embedded icon image.
const maxImportUploadBytes = 20 << 20

func handleLayerImport(w http.ResponseWriter, r *http.Request, layerID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxImportUploadBytes)
	if err := r.ParseMultipartForm(maxImportUploadBytes); err != nil {
		http.Error(w, "invalid or too-large upload: "+err.Error(), http.StatusBadRequest)
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file required", http.StatusBadRequest)
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "failed to read uploaded file", http.StatusBadRequest)
		return
	}

	pointRadius, _ := strconv.ParseFloat(r.FormValue("point_radius_m"), 64)
	segmentRadius, _ := strconv.ParseFloat(r.FormValue("segment_radius_m"), 64)
	if pointRadius <= 0 && segmentRadius <= 0 {
		http.Error(w, "at least one of point_radius_m/segment_radius_m must be > 0", http.StatusBadRequest)
		return
	}

	result, err := ImportKML(layerID, raw, pointRadius, segmentRadius)
	if err != nil {
		status := http.StatusBadRequest
		if err == errLayerNotFound {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func handleOneLayer(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		l, ok := GetLayer(id)
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, l)

	case http.MethodPut:
		var req layerReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		l, err := UpdateLayer(id, &Layer{
			Name: req.Name, Color: req.Color, Icon: req.Icon,
			DefaultOn: req.DefaultOn, FlagsIncidentCongestionRisk: req.FlagsIncidentCongestionRisk,
		})
		if err != nil {
			status := http.StatusInternalServerError
			if err == errNotFound {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, http.StatusOK, l)

	case http.MethodDelete:
		if err := DeleteLayer(id); err != nil {
			status := http.StatusInternalServerError
			if err == errNotFound {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ── Features (admin-only CRUD) ──────────────────────────────────────

type featureReq struct {
	Label   string       `json:"label"`
	Kind    FeatureKind  `json:"kind"`
	Lat     float64      `json:"lat"`
	Lng     float64      `json:"lng"`
	Line    [][2]float64 `json:"line"`
	RadiusM float64      `json:"radius_m"`
}

func validateFeatureReq(req *featureReq) string {
	switch req.Kind {
	case FeatureKindPoint:
		if req.Lat == 0 && req.Lng == 0 {
			return "lat/lng required for a point feature"
		}
	case FeatureKindSegment:
		if len(req.Line) < 2 {
			return "line needs at least 2 points for a segment feature"
		}
	default:
		return `kind must be "point" or "segment"`
	}
	if req.RadiusM <= 0 {
		return "radius_m must be > 0"
	}
	return ""
}

func handleFeatures(w http.ResponseWriter, r *http.Request, layerID string) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, ListFeatures(layerID))

	case http.MethodPost:
		var req featureReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if msg := validateFeatureReq(&req); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		f := &Feature{
			LayerID: layerID, Label: req.Label, Kind: req.Kind,
			Lat: req.Lat, Lng: req.Lng, Line: req.Line, RadiusM: req.RadiusM,
		}
		if err := CreateFeature(f); err != nil {
			status := http.StatusInternalServerError
			if err == errLayerNotFound {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, http.StatusCreated, f)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleOneFeature(w http.ResponseWriter, r *http.Request, layerID, featureID string) {
	switch r.Method {
	case http.MethodPut:
		var req featureReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if msg := validateFeatureReq(&req); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		f, err := UpdateFeature(featureID, &Feature{
			LayerID: layerID, Label: req.Label, Kind: req.Kind,
			Lat: req.Lat, Lng: req.Lng, Line: req.Line, RadiusM: req.RadiusM,
		})
		if err != nil {
			status := http.StatusInternalServerError
			if err == errNotFound || err == errLayerNotFound {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, http.StatusOK, f)

	case http.MethodDelete:
		if err := DeleteFeature(featureID); err != nil {
			status := http.StatusInternalServerError
			if err == errNotFound {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
