package auth

// api_devices.go — HTTP API for device management and monitoring
//
// GET/POST/DELETE /api/device-types      — CRUD for device type definitions
// GET/POST/DELETE /api/devices           — CRUD for monitored devices
// GET             /api/device-stats      — live connectivity stats per type
// GET/PUT         /api/device-ping-interval — read/write ping interval

import (
	"encoding/json"
	"net/http"
)

func registerDevicesHandler() {
	http.HandleFunc("/api/device-types",         deviceTypesHandler)
	http.HandleFunc("/api/devices",              devicesHandler)
	http.HandleFunc("/api/device-stats",         deviceStatsHandler)
	http.HandleFunc("/api/device-ping-interval", devicePingIntervalHandler)
}

// ── /api/device-types ────────────────────────────────────────────

func deviceTypesHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok || user.Role != RoleAdmin {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		types := listDeviceTypes()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types)

	case http.MethodPost:
		var t DeviceType
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if t.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if err := upsertDeviceType(&t); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(t)

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		if err := deleteDeviceType(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ── /api/devices ─────────────────────────────────────────────────

func devicesHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok || user.Role != RoleAdmin {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		devs := listDevices()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(devs)

	case http.MethodPost:
		var d Device
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if d.Name == "" || d.IP == "" {
			http.Error(w, "name and ip required", http.StatusBadRequest)
			return
		}
		if err := upsertDevice(&d); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(d)

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		if err := deleteDevice(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ── /api/device-stats ────────────────────────────────────────────

func deviceStatsHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok || (user.Role != RoleAdmin && !HasTab(r.Context(), TabMonitor)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	stats := getDeviceStats()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(stats)
}

// ── /api/device-ping-interval ────────────────────────────────────

func devicePingIntervalHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok || user.Role != RoleAdmin {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"interval_sec": getPingIntervalSec()})

	case http.MethodPut:
		var body struct {
			IntervalSec int `json:"interval_sec"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := setPingIntervalSec(body.IntervalSec); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
