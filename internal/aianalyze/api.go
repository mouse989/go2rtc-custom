package aianalyze

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/AlexxIT/go2rtc/internal/auth"
	"github.com/AlexxIT/go2rtc/internal/counting"
)

func registerHandlers() {
	http.HandleFunc("/api/ai-analyze/model", handleModel)
	http.HandleFunc("/api/ai-analyze/model/meta", handleModelMeta)
	http.HandleFunc("/api/ai-analyze/model/publish", handleModelPublish)
	http.HandleFunc("/api/ai-analyze/class-colors", handleClassColors)
}

// requireViewer allows admin or any user explicitly granted AllowAIAnalyze
// and/or AllowAIWatch — the same client-visible-feature convention as
// AllowMapAIEvents (internal/aievent/api.go's requireAIEventViewer), just
// with two flags instead of one since AI ANALYZE and Watch are grantable
// independently but both read the same published model through this file.
// The path itself is additionally gated in internal/auth/middleware.go,
// same double-gate pattern used throughout this codebase (outer middleware
// allow + inner handler check).
func requireViewer(w http.ResponseWriter, r *http.Request) bool {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	if user.Role != auth.RoleAdmin && !user.AllowAIAnalyze && !user.AllowAIWatch {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user.Role != auth.RoleAdmin {
		http.Error(w, "admin only", http.StatusForbidden)
		return false
	}
	return true
}

// GET /api/ai-analyze/model — serves the published .onnx binary.
// PUT  /api/ai-analyze/model?labels=a,b,c&vehicleLabels=a,b&personLabels=&inputSize=416&format=ultralytics&sourceModel=models/x.pt
//
//	— direct raw-body upload fallback (admin only), for a model exported
//	outside the Counting page's own "Export & Publish" flow (see
//	handleModelPublish below for the normal path).
func handleModel(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !requireViewer(w, r) {
			return
		}
		if !HasModel() {
			http.Error(w, "no AI Analyze model has been published yet", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		// Published models are swapped rarely and deliberately (an admin
		// action), but the browser Web Worker caches aggressively across
		// camera sessions — callers are expected to cache-bust with
		// &_=<updated_at> the same way map.html/dashboard2.html already do
		// for category icons, so a long max-age here is safe.
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeFile(w, r, modelFile())

	case http.MethodPut:
		if !requireAdmin(w, r) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxModelUploadBytes)
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "file too large or malformed upload: "+err.Error(), http.StatusBadRequest)
			return
		}
		m := modelMetaFromQuery(r)
		user, _ := auth.UserFromContext(r.Context())
		saved, err := PublishModel(data, m, user.Username)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, saved)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func modelMetaFromQuery(r *http.Request) ModelMeta {
	inputSize, _ := strconv.Atoi(r.URL.Query().Get("inputSize"))
	return ModelMeta{
		Labels:        splitCSV(r.URL.Query().Get("labels")),
		VehicleLabels: splitCSV(r.URL.Query().Get("vehicleLabels")),
		PersonLabels:  splitCSV(r.URL.Query().Get("personLabels")),
		InputSize:     inputSize,
		Format:        r.URL.Query().Get("format"),
		SourceModel:   r.URL.Query().Get("sourceModel"),
	}
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}

// GET /api/ai-analyze/model/meta
func handleModelMeta(w http.ResponseWriter, r *http.Request) {
	if !requireViewer(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	m, ok := GetModelMeta()
	if !ok {
		http.Error(w, "no AI Analyze model has been published yet", http.StatusNotFound)
		return
	}
	writeJSON(w, m)
}

type publishFromCountingRequest struct {
	WorkerID      string   `json:"workerId"`
	OnnxFile      string   `json:"onnxFile"`
	Labels        []string `json:"labels"`
	VehicleLabels []string `json:"vehicleLabels"`
	PersonLabels  []string `json:"personLabels"`
	InputSize     int      `json:"inputSize"`
	Format        string   `json:"format"`
	SourceModel   string   `json:"sourceModel"`
}

// POST /api/ai-analyze/model/publish
// The normal publish path: counting.html's "Export & Publish" button first
// calls POST /api/counting/export-onnx (internal/counting), which runs the
// actual .pt→.onnx conversion on the yolo_counter worker and returns
// {onnx, labels, vehicleLabels, personLabels, inputSize, format} — the
// browser forwards that response's fields straight into this request's
// body, and this handler pulls the exported file's bytes itself
// (counting.GetModelFileBytes, local-disk or remote-worker as needed) so
// the (possibly tens-of-MB) binary never has to round-trip through the
// admin's own browser connection.
func handleModelPublish(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req publishFromCountingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.OnnxFile == "" {
		http.Error(w, "onnxFile required", http.StatusBadRequest)
		return
	}
	data, err := counting.GetModelFileBytes(req.WorkerID, req.OnnxFile)
	if err != nil {
		http.Error(w, "fetch exported model: "+err.Error(), http.StatusBadGateway)
		return
	}
	m := ModelMeta{
		Labels:        req.Labels,
		VehicleLabels: req.VehicleLabels,
		PersonLabels:  req.PersonLabels,
		InputSize:     req.InputSize,
		Format:        req.Format,
		SourceModel:   req.SourceModel,
	}
	user, _ := auth.UserFromContext(r.Context())
	saved, err := PublishModel(data, m, user.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, saved)
}

// GET /api/ai-analyze/class-colors — any viewer (same gate as the model
// itself): the browser overlay needs this to render boxes, not just the
// admin who configures it.
// PUT /api/ai-analyze/class-colors — admin only. Body is the full
// class→colour map to save (a class omitted or sent with an empty value
// reverts to DefaultClassColors for that class — see SetClassColors).
func handleClassColors(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !requireViewer(w, r) {
			return
		}
		writeJSON(w, GetClassColors())

	case http.MethodPut, http.MethodPost:
		if !requireAdmin(w, r) {
			return
		}
		var colors map[string]string
		if err := json.NewDecoder(r.Body).Decode(&colors); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := SetClassColors(colors); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, GetClassColors())

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
