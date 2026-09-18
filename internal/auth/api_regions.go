package auth

import (
	"encoding/json"
	"net/http"
	"strings"
)

// RegionWithCameras is what the API returns for a region — the stored
// polygon plus its currently-resolved camera list, so the admin UI (map
// drawing tool, user-form region picker) never has to compute point-in-
// polygon itself just to show a camera count.
type RegionWithCameras struct {
	Region
	Cameras []string `json:"cameras"`
}

func registerRegionHandlers() {
	http.HandleFunc("/api/regions", regionsHandler)
	http.HandleFunc("/api/regions/", regionsHandler)
}

func withCameras(r *Region) RegionWithCameras {
	cams := camerasInRegion(r.ID)
	if cams == nil {
		cams = []string{}
	}
	return RegionWithCameras{Region: *r, Cameras: cams}
}

// regionsHandler handles /api/regions and /api/regions/{id} — admin only,
// both read and write (regions are a permission-management tool, not
// something viewers browse).
func regionsHandler(w http.ResponseWriter, r *http.Request) {
	caller, ok := UserFromContext(r.Context())
	if !ok || caller.Role != RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/regions"), "/")

	switch r.Method {
	case http.MethodGet:
		if id != "" {
			reg, found := GetRegion(id)
			if !found {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			responseJSON(w, withCameras(reg))
			return
		}
		list := ListRegions()
		out := make([]RegionWithCameras, len(list))
		for i, reg := range list {
			out[i] = withCameras(reg)
		}
		responseJSON(w, out)

	case http.MethodPost:
		var req struct {
			Name     string `json:"name"`
			Color    string `json:"color"`
			Polygons []Ring `json:"polygons"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if len(req.Polygons) == 0 {
			http.Error(w, "at least one polygon required", http.StatusBadRequest)
			return
		}
		reg := &Region{ID: newRegionID(), Name: req.Name, Color: req.Color, Polygons: req.Polygons}
		if err := CreateRegion(reg); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		responseJSON(w, withCameras(reg))

	case http.MethodPut:
		if id == "" {
			http.Error(w, "region id required in path", http.StatusBadRequest)
			return
		}
		if _, found := GetRegion(id); !found {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var req struct {
			Name     string `json:"name"`
			Color    string `json:"color"`
			Polygons []Ring `json:"polygons"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if len(req.Polygons) == 0 {
			http.Error(w, "at least one polygon required", http.StatusBadRequest)
			return
		}
		reg := &Region{ID: id, Name: req.Name, Color: req.Color, Polygons: req.Polygons}
		if err := UpdateRegion(reg); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		responseJSON(w, withCameras(reg))

	case http.MethodDelete:
		if id == "" {
			http.Error(w, "region id required", http.StatusBadRequest)
			return
		}
		if err := DeleteRegion(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
