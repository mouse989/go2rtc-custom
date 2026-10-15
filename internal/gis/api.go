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
	"net/http"
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
//	/api/gis/layers/{id}                   → one layer
//	/api/gis/layers/{id}/features          → feature list/create
//	/api/gis/layers/{id}/features/{fid}    → one feature
func handleLayersSubpath(w http.ResponseWriter, r *http.Request) {
	if !requireGISAdmin(w, r) {
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/gis/layers/"), "/")
	parts := strings.Split(rest, "/")
	if parts[0] == "" {
		http.Error(w, "layer id required", http.StatusBadRequest)
		return
	}
	layerID := parts[0]

	switch {
	case len(parts) == 1:
		handleOneLayer(w, r, layerID)
	case len(parts) == 2 && parts[1] == "features":
		handleFeatures(w, r, layerID)
	case len(parts) == 3 && parts[1] == "features":
		handleOneFeature(w, r, layerID, parts[2])
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
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
