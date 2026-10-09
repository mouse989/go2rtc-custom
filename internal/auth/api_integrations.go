package auth

// api_integrations.go — admin-only CRUD for external push integrations
// (internal/auth/integrations.go). The plaintext key is only ever present
// in a create/rotate/reveal response body — list/get responses always use
// integrationOut, which omits it (and the hash/encrypted form) entirely.
//
// POST   /api/integrations          body: {"name","allowed_path"} -> includes "key"
// GET    /api/integrations          list, redacted
// GET    /api/integrations/{id}     one, redacted
// PUT    /api/integrations/{id}     body: {"name","allowed_path","enabled"}
// POST   /api/integrations/{id}/rotate -> {"key": "<new plaintext>"}
// POST   /api/integrations/{id}/reveal body: {"password":"<caller's own login password>"} -> {"key": "<plaintext>"}
// DELETE /api/integrations/{id}

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func registerIntegrationsHandler() {
	http.HandleFunc("/api/integrations", integrationsHandler)
	http.HandleFunc("/api/integrations/", integrationsHandler)
}

// integrationOut is what the API returns for an integration — never the key
// or its hash.
type integrationOut struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	AllowedPath string `json:"allowed_path"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   string `json:"created_at"`
	LastUsedAt  string `json:"last_used_at,omitempty"`
	LastUsedIP  string `json:"last_used_ip,omitempty"`
}

func redactIntegration(in *Integration) integrationOut {
	out := integrationOut{
		ID: in.ID, Name: in.Name, AllowedPath: in.AllowedPath, Enabled: in.Enabled,
		CreatedAt: in.CreatedAt.Format(time.RFC3339),
	}
	if !in.LastUsedAt.IsZero() {
		out.LastUsedAt = in.LastUsedAt.Format(time.RFC3339)
		out.LastUsedIP = in.LastUsedIP
	}
	return out
}

func integrationsHandler(w http.ResponseWriter, r *http.Request) {
	caller, ok := UserFromContext(r.Context())
	if !ok || caller.Role != RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/integrations"), "/")
	id, action, _ := strings.Cut(rest, "/")

	if action == "rotate" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if id == "" {
			http.Error(w, "integration id required", http.StatusBadRequest)
			return
		}
		key, err := RotateIntegrationKey(id)
		if err != nil {
			status := http.StatusInternalServerError
			if err == errIntegrationNotFound {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		responseJSON(w, map[string]string{"key": key})
		return
	}

	if action == "reveal" {
		handleRevealIntegration(w, r, caller, id)
		return
	}

	switch r.Method {
	case http.MethodGet:
		if id != "" {
			in, found := GetIntegration(id)
			if !found {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			responseJSON(w, redactIntegration(in))
			return
		}
		list := ListIntegrations()
		out := make([]integrationOut, len(list))
		for i, in := range list {
			out[i] = redactIntegration(in)
		}
		responseJSON(w, out)

	case http.MethodPost:
		var req struct {
			Name        string `json:"name"`
			AllowedPath string `json:"allowed_path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		in, key, err := CreateIntegration(req.Name, req.AllowedPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		resp := map[string]any{
			"integration": redactIntegration(in),
			"key":         key,
		}
		responseJSON(w, resp)

	case http.MethodPut:
		if id == "" {
			http.Error(w, "integration id required in path", http.StatusBadRequest)
			return
		}
		var req struct {
			Name        string `json:"name"`
			AllowedPath string `json:"allowed_path"`
			Enabled     bool   `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		in, err := UpdateIntegration(id, req.Name, req.AllowedPath, req.Enabled)
		if err != nil {
			status := http.StatusBadRequest
			if err == errIntegrationNotFound {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		responseJSON(w, redactIntegration(in))

	case http.MethodDelete:
		if id == "" {
			http.Error(w, "integration id required", http.StatusBadRequest)
			return
		}
		if err := DeleteIntegration(id); err != nil {
			status := http.StatusInternalServerError
			if err == errIntegrationNotFound {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleRevealIntegration re-reveals an existing integration's plaintext
// key. Gated behind the caller re-entering their OWN login password (not
// just "is currently an admin") — same step-up principle as
// changePasswordHandler's CurrentPassword check — and throttled through the
// exact same brute-force lockout state as POST /api/auth/login, keyed by
// "user:"+caller's username, so repeated wrong-password guesses here count
// toward (and can trigger) the same account lockout a login attack would.
func handleRevealIntegration(w http.ResponseWriter, r *http.Request, caller *User, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if id == "" {
		http.Error(w, "integration id required", http.StatusBadRequest)
		return
	}
	in, found := GetIntegration(id)
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	userKey := "user:" + strings.ToLower(caller.Username)
	if locked, remaining := loginLocked(userKey); locked {
		respondLoginLocked(w, remaining)
		return
	}
	if _, ok := Authenticate(caller.Username, req.Password); !ok {
		if recordLoginFailure(userKey) {
			onLoginLockout("user", caller.Username)
		}
		http.Error(w, "incorrect password", http.StatusUnauthorized)
		return
	}
	recordLoginSuccess(userKey)

	key, err := RevealIntegrationKey(id)
	if err != nil {
		status := http.StatusInternalServerError
		if err == errIntegrationKeyUnavailable {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}

	ip := clientIP(r)
	raiseAlert("integration_key_revealed", AlertSeverityMedium, caller.Username, ip,
		"Xem lại API key của tích hợp \""+in.Name+"\".")
	responseJSON(w, map[string]string{"key": key})
}
