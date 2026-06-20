package workers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/auth"
)

// tokenCache holds cached JWT tokens keyed by worker ID.
var tokenCache sync.Map

func workerLogin(wk *Worker) (string, error) {
	payload, _ := json.Marshal(map[string]string{"username": wk.Username, "password": wk.Password})
	req, err := http.NewRequest(http.MethodPost, wk.URL+"/api/auth/login", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := workerHTTPClient(wk).Do(req)
	if err != nil {
		return "", fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("login failed %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode login response: %w", err)
	}
	if result.Token == "" {
		return "", fmt.Errorf("empty token from worker")
	}
	tokenCache.Store(wk.ID, result.Token)
	return result.Token, nil
}

func workerToken(wk *Worker) (string, error) {
	if wk.Username == "" && wk.Password == "" {
		return "", nil
	}
	if v, ok := tokenCache.Load(wk.ID); ok {
		return v.(string), nil
	}
	return workerLogin(wk)
}

func registerAPI() {
	api.HandleFunc("api/workers", handleWorkers)
	api.HandleFunc("api/workers/status", handleWorkersStatus)
	api.HandleFunc("api/workers/sync", handleWorkersSync)
	api.HandleFunc("api/workers/models", handleWorkersModels)
	api.HandleFunc("api/workers/models/pull", handleWorkersModelsPull)
	api.HandleFunc("api/workers/models/push", handleWorkersModelsPush)
	api.HandleFunc("api/workers/train", handleWorkersTrain)
	api.HandleFunc("api/workers/train/status", handleWorkersTrainStatus)
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

// RequestWorkerStream makes an authenticated HTTP request to a named worker with
// no timeout. Use this for long-lived MJPEG/SSE streams instead of RequestWorker.
func RequestWorkerStream(id, method, path string) (*http.Response, error) {
	wk := getWorkerByID(id)
	if wk == nil {
		return nil, fmt.Errorf("worker not found: %s", id)
	}
	token, _ := workerToken(wk)
	req, err := http.NewRequest(method, wk.URL+path, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return (&http.Client{}).Do(req) // no timeout — caller must close body
}

func workerRequest(wk *Worker, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	return workerRequestWithToken(wk, method, path, body, contentType, true)
}

func workerRequestWithToken(wk *Worker, method, path string, body io.Reader, contentType string, retry bool) (*http.Response, error) {
	// Buffer body so we can resend on 401 retry.
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = io.ReadAll(body)
		if err != nil {
			return nil, err
		}
	}

	token, err := workerToken(wk)
	if err != nil {
		log.Warn().Err(err).Str("worker", wk.ID).Msg("[workers] could not get token, trying unauthenticated")
	}

	var bodyReader io.Reader
	if bodyBytes != nil {
		bodyReader = bytes.NewReader(bodyBytes)
	}
	req, err := http.NewRequest(method, wk.URL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := workerHTTPClient(wk).Do(req)
	if err != nil {
		return nil, err
	}

	// On 401, evict cached token and retry once with fresh login.
	if resp.StatusCode == http.StatusUnauthorized && retry && (wk.Username != "" || wk.Password != "") {
		resp.Body.Close()
		tokenCache.Delete(wk.ID)
		_, loginErr := workerLogin(wk)
		if loginErr != nil {
			return nil, fmt.Errorf("re-login after 401: %w", loginErr)
		}
		return workerRequestWithToken(wk, method, path, bytes.NewReader(bodyBytes), contentType, false)
	}

	return resp, nil
}

// POST /api/workers/train?id=X
// Proxies a training request (JSON body) to the worker's POST /api/counting/train.
func handleWorkersTrain(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
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
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	resp, err := workerRequest(wk, http.MethodPost, "/api/counting/train", bytes.NewReader(body), "application/json")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// GET /api/workers/train/status?id=X
// Proxies to the worker's GET /api/counting/train-status.
func handleWorkersTrainStatus(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
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
	resp, err := workerRequest(wk, http.MethodGet, "/api/counting/train-status", nil, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
