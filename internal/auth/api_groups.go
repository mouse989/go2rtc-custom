package auth

import (
	"encoding/json"
	"net/http"
	"strings"
)

func registerGroupHandlers() {
	http.HandleFunc("/api/groups", groupsHandler)
	http.HandleFunc("/api/groups/", groupsHandler)
}

// groupsHandler handles /api/groups and /api/groups/{name}
// GET is allowed for all authenticated users; write ops require admin.
func groupsHandler(w http.ResponseWriter, r *http.Request) {
	caller, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Extract optional group name from URL
	name := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/groups"), "/")
	// URL-decode the name (spaces, etc.)
	name = strings.ReplaceAll(name, "%20", " ")

	switch r.Method {
	case http.MethodGet:
		if name != "" {
			g, found := GetGroup(name)
			if !found {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			responseJSON(w, g)
		} else {
			responseJSON(w, ListGroups())
		}

	case http.MethodPost:
		if caller.Role != RoleAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var req struct {
			Name    string   `json:"name"`
			Cameras []string `json:"cameras"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if _, exists := GetGroup(req.Name); exists {
			http.Error(w, "group already exists", http.StatusConflict)
			return
		}
		if req.Cameras == nil {
			req.Cameras = []string{}
		}
		g := &Group{Name: req.Name, Cameras: req.Cameras}
		if err := CreateGroup(g); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		responseJSON(w, g)

	case http.MethodPut:
		if caller.Role != RoleAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if name == "" {
			http.Error(w, "group name required in path", http.StatusBadRequest)
			return
		}
		var req struct {
			Cameras []string `json:"cameras"`
			NewName string   `json:"new_name,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Cameras == nil {
			req.Cameras = []string{}
		}
		// Rename if requested
		if req.NewName != "" && req.NewName != name {
			if _, exists := GetGroup(req.NewName); exists {
				http.Error(w, "group name already exists", http.StatusConflict)
				return
			}
			if err := RenameGroup(name, req.NewName, req.Cameras); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			responseJSON(w, &Group{Name: req.NewName, Cameras: req.Cameras})
			return
		}
		g := &Group{Name: name, Cameras: req.Cameras}
		if err := UpdateGroup(g); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		responseJSON(w, g)

	case http.MethodDelete:
		if caller.Role != RoleAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if name == "" {
			http.Error(w, "group name required", http.StatusBadRequest)
			return
		}
		if err := DeleteGroup(name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
