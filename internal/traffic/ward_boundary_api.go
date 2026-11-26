package traffic

// ward_boundary_api.go — API for www/tti-suggest.html's ward-boundary
// filter and its admin KML import tool (ward_boundary.go has the
// storage/point-in-polygon side).

import (
	"encoding/json"
	"io"
	"net/http"
)

func registerWardBoundaryHandlers() {
	http.HandleFunc("/api/tti-suggest/wards", handleWardBoundaries)
	http.HandleFunc("/api/tti-suggest/wards/import", handleWardBoundariesImport)
}

// handleWardBoundaries  GET /api/tti-suggest/wards
// Returns every imported ward's ID/Name/Type only (not Rings — the
// filter's picker list never needs the polygon data itself, only enough
// to build the select/checkbox list and to send IDs back on the hotspot
// query). Any authenticated TabTtiSuggest holder — the real restriction
// already ran in internal/auth/middleware.go's tabExtraPaths.
func handleWardBoundaries(w http.ResponseWriter, r *http.Request) {
	if !requireTrafficViewer(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	type wardListItem struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Type string `json:"type,omitempty"`
	}
	all := listWardBoundaries()
	out := make([]wardListItem, len(all))
	for i, ward := range all {
		out[i] = wardListItem{ID: ward.ID, Name: ward.Name, Type: ward.Type}
	}
	writeJSON(w, out)
}

// wardImportRequest is one ward entry in the bulk-import body — Rings is
// [[ [lat,lng], ... ], ...], already reordered from KML's own lng,lat
// into this app's [lat,lng] convention by the client-side parser (see
// tti-suggest.html's parseKmlToWards).
type wardImportRequest struct {
	Name  string         `json:"name"`
	Type  string         `json:"type"`
	Rings [][][2]float64 `json:"rings"`
}

// maxWardImportBodyBytes bounds the KML-derived JSON import payload —
// generous enough for a detailed 168-placemark official boundary dataset
// (the source KML file itself was ~8MB; JSON-encoded coordinates run a
// bit larger) without leaving the request body truly unbounded.
const maxWardImportBodyBytes = 64 << 20 // 64MB

// handleWardBoundariesImport  POST /api/tti-suggest/wards/import
// Body: a JSON array of wardImportRequest — replaces the ENTIRE stored
// ward list in one shot (re-importing the same KML dataset, or a revised
// one, is meant to just replace what was there, not merge with it).
// Admin-only: this replaces shared reference data every TabTtiSuggest
// holder's filter then reads, same sensitivity as internal/traveltime's
// own admin-managed region-name list.
func handleWardBoundariesImport(w http.ResponseWriter, r *http.Request) {
	if !requireTrafficAccess(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxWardImportBodyBytes+1))
	if err != nil {
		http.Error(w, "read error: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body) > maxWardImportBodyBytes {
		http.Error(w, "import payload too large", http.StatusRequestEntityTooLarge)
		return
	}

	var reqs []wardImportRequest
	if err := json.Unmarshal(body, &reqs); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(reqs) == 0 {
		http.Error(w, "no wards in import payload", http.StatusBadRequest)
		return
	}

	next := make([]*WardBoundary, 0, len(reqs))
	for _, wr := range reqs {
		if wr.Name == "" || len(wr.Rings) == 0 {
			continue // skip a malformed entry rather than failing the whole import
		}
		rings := make([]Ring, 0, len(wr.Rings))
		for _, r := range wr.Rings {
			if len(r) >= 3 {
				rings = append(rings, Ring(r))
			}
		}
		if len(rings) == 0 {
			continue
		}
		next = append(next, &WardBoundary{Name: wr.Name, Type: wr.Type, Rings: rings})
	}
	if len(next) == 0 {
		http.Error(w, "no valid wards (each needs a name and at least one ring of >= 3 points)", http.StatusBadRequest)
		return
	}

	if err := replaceWardBoundaries(next); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]int{"imported": len(next)})
}
