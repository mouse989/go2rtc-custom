package rtsp

import (
	"encoding/json"
	"net/http"

	"github.com/AlexxIT/go2rtc/internal/auth"
)

// registerHandlers exposes the RTSP server's Basic-auth credentials to
// admins only, so they can copy them into third-party viewers, remote
// worker configs (RTSPBase), or NAT/firewall documentation.
func registerHandlers() {
	http.HandleFunc("/api/rtsp/credentials", handleCredentials)
	http.HandleFunc("/api/rtsp/credentials/rotate", handleRotateCredentials)
}

func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user.Role != auth.RoleAdmin {
		http.Error(w, "admin only", http.StatusForbidden)
		return false
	}
	return true
}

func handleCredentials(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	username, password := CurrentCredentials()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"username": username,
		"password": password,
		"port":     Port,
		"tls_port": TLSPort,
	})
}

func handleRotateCredentials(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	username, password, err := RotateCredentials()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"username": username,
		"password": password,
		"port":     Port,
		"tls_port": TLSPort,
	})
}
