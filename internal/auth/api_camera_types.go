package auth

// api_camera_types.go — HTTP API for camera type management
//
// GET  /api/camera-types                → list all CameraType objects
// POST /api/camera-types                → create or update a type (body: CameraType JSON)
// DELETE /api/camera-types?id=X         → delete type by ID
//
// GET  /api/camera-type-assignments     → map of streamName → typeID
// PUT  /api/camera-type-assignments     → replace full assignment map (body: {streamName: typeID})

import (
	"encoding/json"
	"net/http"
)

func registerCameraTypesHandler() {
	http.HandleFunc("/api/camera-types", cameraTypesHandler)
	http.HandleFunc("/api/camera-type-assignments", cameraTypeAssignmentsHandler)
}

func cameraTypesHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok || user.Role != RoleAdmin {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet:
		types := listCameraTypes()
		if types == nil {
			types = []*CameraType{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types)

	case http.MethodPost:
		var t CameraType
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if t.ID == "" || t.Name == "" {
			http.Error(w, "id and name are required", http.StatusBadRequest)
			return
		}
		if err := upsertCameraType(&t); err != nil {
			http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
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
		if err := deleteCameraType(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func cameraTypeAssignmentsHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok || user.Role != RoleAdmin {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet:
		assignments := getCameraTypeAssignments()
		if assignments == nil {
			assignments = map[string]string{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(assignments)

	case http.MethodPut:
		var assignments map[string]string
		if err := json.NewDecoder(r.Body).Decode(&assignments); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if assignments == nil {
			assignments = map[string]string{}
		}
		if err := setCameraTypeAssignments(assignments); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
