package traveltime

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/AlexxIT/go2rtc/internal/auth"
)

const (
	vietmapRouteAPI  = "https://maps.vietmap.vn/api/route"
	vietmapSearchAPI = "https://maps.vietmap.vn/api/search/v3"
	vietmapPlaceAPI  = "https://maps.vietmap.vn/api/place/v3"
)

func registerVietmapHandlers() {
	http.HandleFunc("/api/vietmap/key", handleVietmapKey)
	http.HandleFunc("/api/vietmap/route", handleVietmapRoute)
	http.HandleFunc("/api/vietmap/search", handleVietmapSearch)
	http.HandleFunc("/api/vietmap/place", handleVietmapPlace)
}

// handleVietmapKey returns the commercial API key for use in the frontend map.
func handleVietmapKey(w http.ResponseWriter, r *http.Request) {
	if !requireLogin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key := auth.GetSettings().VietmapAPIKey
	writeJSON(w, map[string]string{"apikey": key})
}

// handleVietmapRoute proxies Route v3 requests to the VietMap commercial API.
// The frontend sends: GET /api/vietmap/route?point=lat,lng&point=lat,lng&vehicle=car
func handleVietmapRoute(w http.ResponseWriter, r *http.Request) {
	if !requireLogin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	apiKey := auth.GetSettings().VietmapAPIKey
	if apiKey == "" {
		http.Error(w, "VietMap API key not configured", http.StatusServiceUnavailable)
		return
	}

	q := r.URL.Query()
	q.Set("apikey", apiKey)
	if q.Get("points_encoded") == "" {
		q.Set("points_encoded", "false")
	}
	if q.Get("vehicle") == "" {
		q.Set("vehicle", "car")
	}

	upstream := fmt.Sprintf("%s?%s", vietmapRouteAPI, q.Encode())
	proxyGet(w, upstream)
}

// handleVietmapSearch proxies place autocomplete requests to VietMap Search v3.
func handleVietmapSearch(w http.ResponseWriter, r *http.Request) {
	if !requireLogin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	apiKey := auth.GetSettings().VietmapAPIKey
	if apiKey == "" {
		http.Error(w, "VietMap API key not configured", http.StatusServiceUnavailable)
		return
	}

	q := r.URL.Query()
	q.Set("apikey", apiKey)
	upstream := fmt.Sprintf("%s?%s", vietmapSearchAPI, q.Encode())
	proxyGet(w, upstream)
}

// handleVietmapPlace proxies place detail requests to VietMap Place v3.
func handleVietmapPlace(w http.ResponseWriter, r *http.Request) {
	if !requireLogin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	apiKey := auth.GetSettings().VietmapAPIKey
	if apiKey == "" {
		http.Error(w, "VietMap API key not configured", http.StatusServiceUnavailable)
		return
	}

	q := r.URL.Query()
	q.Set("apikey", apiKey)
	upstream := fmt.Sprintf("%s?%s", vietmapPlaceAPI, q.Encode())
	proxyGet(w, upstream)
}

// proxyGet performs a GET to upstream and forwards the response body to w.
func proxyGet(w http.ResponseWriter, upstream string) {
	if _, err := url.Parse(upstream); err != nil {
		http.Error(w, "bad upstream URL", http.StatusInternalServerError)
		return
	}

	resp, err := http.Get(upstream) //nolint:noctx
	if err != nil {
		http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	// Only forward JSON (block HTML error pages)
	if !strings.Contains(ct, "json") && resp.StatusCode == http.StatusOK {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
