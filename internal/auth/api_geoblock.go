package auth

import (
	"encoding/json"
	"net/http"
)

func registerGeoBlockHandler() {
	http.HandleFunc("/api/geoblock/status", geoBlockStatusHandler)
	http.HandleFunc("/api/geoblock/update-now", geoBlockUpdateNowHandler)
}

// GET /api/geoblock/status — any authenticated user (read-only operational
// info, same sensitivity level as GET /api/settings).
func geoBlockStatusHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := UserFromContext(r.Context()); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(GeoBlockStatus())
}

// POST /api/geoblock/update-now — admin only. Synchronously re-fetches the
// configured source(s) and returns the resulting status (or an error if
// the fetch failed — the previous good list, if any, is kept regardless).
func geoBlockUpdateNowHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if user.Role != RoleAdmin {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if fetchErr := TriggerGeoBlockUpdateNow(); fetchErr != nil {
		// Plain-text error, same convention as every other handler's
		// http.Error in this package (www/app.js's apiFetch surfaces a
		// non-OK response body directly as the error message).
		http.Error(w, fetchErr.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(GeoBlockStatus())
}
