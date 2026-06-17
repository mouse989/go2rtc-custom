package workers

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/auth"
)

func registerAPI() {
	api.HandleFunc("api/workers", handleWorkers)
	api.HandleFunc("api/workers/status", handleWorkersStatus)
	api.HandleFunc("api/workers/sync", handleWorkersSync)
	api.HandleFunc("api/workers/models", handleWorkersModels)
	api.HandleFunc("api/workers/models/pull", handleWorkersModelsPull)
	api.HandleFunc("api/workers/models/push", handleWorkersModelsPush)
}

func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user.Role != auth.RoleAdmin {
		http.Error(w, "admin only", http.StatusForbidden)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ── Workers CRUD ─────────────────────────────────────────────────────────────

// GET /api/workers          → list workers (passwords masked)
// POST /api/workers         → create worker
// PUT /api/workers          → update worker
// DELETE /api/workers?id=X  → delete worker
func handleWorkers(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		list := listWorkers()
		// Mask passwords.
		masked := make([]Worker, len(list))
		for i, wk := range list {
			masked[i] = *wk
			masked[i].Password = ""
		}
		writeJSON(w, masked)

	case http.MethodPost:
		var wk Worker
		if err := json.NewDecoder(r.Body).Decode(&wk); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if wk.Name == "" || wk.URL == "" {
			http.Error(w, "name and url are required", http.StatusBadRequest)
			return
		}
		if err := addWorker(&wk); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		out := wk
		out.Password = ""
		writeJSON(w, out)

	case http.MethodPut:
		var wk Worker
		if err := json.NewDecoder(r.Body).Decode(&wk); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if wk.ID == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		// If password was masked (empty string sent by client), keep existing password.
		if wk.Password == "" {
			if existing := getWorkerByID(wk.ID); existing != nil {
				wk.Password = existing.Password
			}
		}
		if err := updateWorker(&wk); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		out := wk
		out.Password = ""
		writeJSON(w, out)

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		if err := deleteWorker(id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// GET /api/workers/status → all worker statuses
func handleWorkersStatus(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, allStatuses())
}

// POST /api/workers/sync?id=X → trigger immediate sync for a worker
func handleWorkersSync(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	go func() {
		if err := syncWorkerEvents(id); err != nil {
			log.Error().Err(err).Str("worker", id).Msg("[workers] manual sync failed")
		}
	}()
	writeJSON(w, map[string]bool{"ok": true})
}

// GET /api/workers/models?id=X → proxy worker's GET /api/counting/models
func handleWorkersModels(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	wk := getWorkerByID(id)
	if wk == nil {
		http.Error(w, "worker not found", http.StatusNotFound)
		return
	}
	resp, err := workerRequest(wk, http.MethodGet, "/api/counting/models", nil, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// POST /api/workers/models/pull?id=X&model=PATH
// Downloads a .pt file from the worker and saves it to the local models/ dir.
func handleWorkersModelsPull(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("id")
	model := r.URL.Query().Get("model")
	if id == "" || model == "" {
		http.Error(w, "id and model required", http.StatusBadRequest)
		return
	}
	wk := getWorkerByID(id)
	if wk == nil {
		http.Error(w, "worker not found", http.StatusNotFound)
		return
	}

	// Fetch model from worker via the /download endpoint.
	path := fmt.Sprintf("/api/counting/models/download?file=%s", model)
	resp, err := workerRequest(wk, http.MethodGet, path, nil, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("worker returned %d", resp.StatusCode), http.StatusBadGateway)
		return
	}

	// Save to local models/ dir (sibling of storeFile).
	modelsDir := filepath.Join(filepath.Dir(storeFile), "models")
	if err := os.MkdirAll(modelsDir, 0755); err != nil {
		http.Error(w, "cannot create models dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	filename := filepath.Base(model)
	savePath := filepath.Join(modelsDir, filename)

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "read error: "+err.Error(), http.StatusBadGateway)
		return
	}
	if err := os.WriteFile(savePath, data, 0644); err != nil {
		http.Error(w, "write error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	relPath := filepath.Join("models", filename)
	writeJSON(w, map[string]string{"saved": relPath})
}

// POST /api/workers/models/push?id=X&model=PATH
// Reads a local .pt file and uploads it to the worker.
func handleWorkersModelsPush(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("id")
	model := r.URL.Query().Get("model")
	if id == "" || model == "" {
		http.Error(w, "id and model required", http.StatusBadRequest)
		return
	}
	wk := getWorkerByID(id)
	if wk == nil {
		http.Error(w, "worker not found", http.StatusNotFound)
		return
	}

	// Read local file.
	localPath := model
	if !filepath.IsAbs(localPath) {
		localPath = filepath.Join(filepath.Dir(storeFile), model)
	}
	fileData, err := os.ReadFile(localPath)
	if err != nil {
		http.Error(w, "cannot read local model: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Build multipart body.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("model", filepath.Base(localPath))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := fw.Write(fileData); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = mw.Close()

	resp, err := workerRequest(wk, http.MethodPost, "/api/counting/models/upload", &buf, mw.FormDataContentType())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		http.Error(w, fmt.Sprintf("worker returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body))), http.StatusBadGateway)
		return
	}

	writeJSON(w, map[string]bool{"ok": true})
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

func workerHTTPClient(_ *Worker) *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

func workerRequest(wk *Worker, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	url := wk.URL + path
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	if wk.Username != "" || wk.Password != "" {
		creds := base64.StdEncoding.EncodeToString([]byte(wk.Username + ":" + wk.Password))
		req.Header.Set("Authorization", "Basic "+creds)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return workerHTTPClient(wk).Do(req)
}
