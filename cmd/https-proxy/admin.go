package main

import (
	"context"
	"crypto/x509"
	_ "embed"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

//go:embed admin.html
var adminHTML []byte

var adminMux = http.NewServeMux()

func init() {
	adminMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
		_, _ = w.Write(adminHTML)
	})
	adminMux.HandleFunc("/api/login", apiLogin)
	adminMux.HandleFunc("/api/logout", apiLogout)
	adminMux.HandleFunc("/api/me", requireAuth(apiMe))
	adminMux.HandleFunc("/api/config", requireAuth(apiConfig))
	adminMux.HandleFunc("/api/settings", requireAuth(apiSettings))
	adminMux.HandleFunc("/api/sites", requireAuth(apiSites))
	adminMux.HandleFunc("/api/status", requireAuth(apiStatus))
	adminMux.HandleFunc("/api/log", requireAuth(apiLog))
	adminMux.HandleFunc("/api/password", requireAuth(apiPassword))
	adminMux.HandleFunc("/api/import", requireAuth(apiImport))
}

// ── Sessions & login ─────────────────────────────────────────────

const sessionCookie = "hp_session"

var (
	sessMu   sync.Mutex
	sessions = map[string]time.Time{} // token → expiry
	failures = map[string]*loginFail{}
)

type loginFail struct {
	n     int
	until time.Time
}

func clientIP(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

func requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || !validSession(c.Value) {
			jsonError(w, http.StatusUnauthorized, "chưa đăng nhập")
			return
		}
		// CSRF: state-changing calls must come from our own page's fetch()
		if r.Method != http.MethodGet && r.Header.Get("X-HP") != "1" {
			jsonError(w, http.StatusForbidden, "thiếu header X-HP")
			return
		}
		next(w, r)
	}
}

func validSession(tok string) bool {
	sessMu.Lock()
	defer sessMu.Unlock()
	exp, ok := sessions[tok]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(sessions, tok)
		return false
	}
	return true
}

func apiLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ip := clientIP(r)

	sessMu.Lock()
	f := failures[ip]
	if f != nil && time.Now().Before(f.until) {
		sessMu.Unlock()
		jsonError(w, http.StatusTooManyRequests, "đăng nhập sai quá nhiều lần, thử lại sau ít phút")
		return
	}
	sessMu.Unlock()

	var req struct{ User, Password string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "dữ liệu không hợp lệ")
		return
	}

	cfgMu.Lock()
	user, hash := cfg.AdminUser, cfg.AdminPassHash
	cfgMu.Unlock()

	if req.User != user || bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) != nil {
		sessMu.Lock()
		if f == nil {
			f = &loginFail{}
			failures[ip] = f
		}
		f.n++
		if f.n >= 5 {
			f.until = time.Now().Add(5 * time.Minute)
			f.n = 0
		}
		sessMu.Unlock()
		logf("[admin] đăng nhập sai từ %s (user %q)", ip, req.User)
		jsonError(w, http.StatusUnauthorized, "sai tên đăng nhập hoặc mật khẩu")
		return
	}

	tok := randID(32)
	sessMu.Lock()
	delete(failures, ip)
	for t, exp := range sessions {
		if time.Now().After(exp) {
			delete(sessions, t)
		}
	}
	sessions[tok] = time.Now().Add(12 * time.Hour)
	sessMu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true,
		Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 12 * 3600,
	})
	logf("[admin] %s đăng nhập từ %s", user, ip)
	jsonOK(w, map[string]any{"ok": true})
}

func apiLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		sessMu.Lock()
		delete(sessions, c.Value)
		sessMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	jsonOK(w, map[string]any{"ok": true})
}

func apiMe(w http.ResponseWriter, _ *http.Request) {
	cfgMu.Lock()
	user := cfg.AdminUser
	cfgMu.Unlock()
	_, initial := os.Stat(initialPasswordFile())
	jsonOK(w, map[string]any{"user": user, "initial_password": initial == nil})
}

func apiPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Old  string `json:"old"`
		New  string `json:"new"`
		User string `json:"user"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "dữ liệu không hợp lệ")
		return
	}
	if len(req.New) < 8 {
		jsonError(w, http.StatusBadRequest, "mật khẩu mới cần ít nhất 8 ký tự")
		return
	}
	cfgMu.Lock()
	defer cfgMu.Unlock()
	if bcrypt.CompareHashAndPassword([]byte(cfg.AdminPassHash), []byte(req.Old)) != nil {
		jsonError(w, http.StatusForbidden, "mật khẩu hiện tại không đúng")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.New), bcrypt.DefaultCost)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	n := cloneConfig(cfg)
	n.AdminPassHash = string(hash)
	if req.User != "" {
		n.AdminUser = req.User
	}
	if err = saveConfig(n); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cfg = n
	_ = os.Remove(initialPasswordFile())
	logf("[admin] đã đổi mật khẩu quản trị")
	jsonOK(w, map[string]any{"ok": true})
}

func initialPasswordFile() string {
	return filepath.Join(filepath.Dir(cfgPath), "https-proxy-initial-password.txt")
}

// ensureAdminPassword creates a random admin password on first run and
// writes it next to the config (and to the log/console).
func ensureAdminPassword(c *Config) error {
	if c.AdminPassHash != "" {
		return nil
	}
	pass := randID(6)
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	c.AdminPassHash = string(hash)
	if c.AdminUser == "" {
		c.AdminUser = "admin"
	}
	msg := "Tài khoản quản trị https-proxy\r\nUser: " + c.AdminUser + "\r\nMật khẩu: " + pass +
		"\r\n\r\nFile này sẽ tự xoá sau khi bạn đổi mật khẩu trên trang quản trị.\r\n"
	_ = os.WriteFile(initialPasswordFile(), []byte(msg), 0600)
	logf("[admin] lần chạy đầu: user %q, mật khẩu %q (đã lưu trong %s)", c.AdminUser, pass, initialPasswordFile())
	return nil
}

// ── Config API ───────────────────────────────────────────────────

func publicConfig(c *Config) map[string]any {
	return map[string]any{
		"http_listen":  c.HTTPListen,
		"https_listen": c.HTTPSListen,
		"admin_listen": c.AdminListen,
		"admin_domain": c.AdminDomain,
		"acme_email":   c.ACMEEmail,
		"cert_dir":     c.CertDir,
		"default_site": c.DefaultSite,
		"admin_user":   c.AdminUser,
		"sites":        c.Sites,
		"config_path":  cfgPath,
	}
}

func apiConfig(w http.ResponseWriter, _ *http.Request) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	jsonOK(w, publicConfig(cfg))
}

// update applies fn to a copy of the config, validates, saves and makes it
// live. relisten=true also restarts listeners (after the response is sent,
// since the admin listener itself may be among them).
func update(w http.ResponseWriter, fn func(c *Config) error, relisten bool) bool {
	cfgMu.Lock()
	n := cloneConfig(cfg)
	if err := fn(n); err != nil {
		cfgMu.Unlock()
		jsonError(w, http.StatusBadRequest, err.Error())
		return false
	}
	if err := validateConfig(n); err != nil {
		cfgMu.Unlock()
		jsonError(w, http.StatusBadRequest, err.Error())
		return false
	}
	if err := saveConfig(n); err != nil {
		cfgMu.Unlock()
		jsonError(w, http.StatusInternalServerError, "không lưu được cấu hình: "+err.Error())
		return false
	}
	cfg = n
	current.Store(buildRoutes(n))
	out := publicConfig(n)
	cfgMu.Unlock()

	if relisten {
		go func() {
			time.Sleep(500 * time.Millisecond)
			applyListeners(n)
		}()
	}
	jsonOK(w, out)
	return true
}

func apiSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		HTTPListen  string `json:"http_listen"`
		HTTPSListen string `json:"https_listen"`
		AdminListen string `json:"admin_listen"`
		AdminDomain string `json:"admin_domain"`
		ACMEEmail   string `json:"acme_email"`
		CertDir     string `json:"cert_dir"`
		DefaultSite string `json:"default_site"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "dữ liệu không hợp lệ")
		return
	}
	ok := update(w, func(c *Config) error {
		c.HTTPListen = req.HTTPListen
		c.HTTPSListen = req.HTTPSListen
		c.AdminListen = req.AdminListen
		c.AdminDomain = req.AdminDomain
		c.ACMEEmail = req.ACMEEmail
		c.CertDir = req.CertDir
		if c.CertDir == "" {
			c.CertDir = "certs"
		}
		c.DefaultSite = req.DefaultSite
		return nil
	}, true)
	if ok {
		logf("[admin] đã lưu cài đặt chung")
	}
}

func apiSites(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost, http.MethodPut:
		var s Site
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			jsonError(w, http.StatusBadRequest, "dữ liệu không hợp lệ")
			return
		}
		ok := update(w, func(c *Config) error {
			if s.ID == "" {
				s.ID = randID(6)
				c.Sites = append(c.Sites, &s)
				return nil
			}
			for i, old := range c.Sites {
				if old.ID == s.ID {
					c.Sites[i] = &s
					return nil
				}
			}
			return errors.New("không tìm thấy trang")
		}, false)
		if ok {
			logf("[admin] đã lưu trang %s → %s (bật=%v)", s.Domain, s.Target, s.Enabled)
		}

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		update(w, func(c *Config) error {
			for i, s := range c.Sites {
				if s.ID == id {
					c.Sites = append(c.Sites[:i], c.Sites[i+1:]...)
					logf("[admin] đã xoá trang %s", s.Domain)
					return nil
				}
			}
			return errors.New("không tìm thấy trang")
		}, false)

	default:
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func apiImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		jsonError(w, http.StatusBadRequest, "cần đường dẫn file reverse_proxy.json")
		return
	}
	var n int
	if update(w, func(c *Config) (err error) {
		n, err = importGo2rtcSites(c, req.Path)
		return err
	}, false) {
		logf("[admin] nhập %d trang từ %s", n, req.Path)
	}
}

// ── Status ───────────────────────────────────────────────────────

type siteStatus struct {
	ID        string `json:"id"`
	Up        bool   `json:"up"`
	LatencyMs int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
	CertUntil string `json:"cert_until,omitempty"`
	CertError string `json:"cert_error,omitempty"`
}

func apiStatus(w http.ResponseWriter, _ *http.Request) {
	cfgMu.Lock()
	c := cloneConfig(cfg)
	cfgMu.Unlock()

	out := make([]*siteStatus, len(c.Sites))
	var wg sync.WaitGroup
	for i, s := range c.Sites {
		st := &siteStatus{ID: s.ID}
		out[i] = st
		wg.Add(1)
		go func(s *Site) {
			defer wg.Done()
			checkBackend(s, st)
			checkCert(c, s, st)
		}(s)
	}
	wg.Wait()

	jsonOK(w, map[string]any{
		"sites":     out,
		"listeners": listenerStatus(),
		"time":      time.Now().Format(time.RFC3339),
	})
}

func checkBackend(s *Site, st *siteStatus) {
	u, err := url.Parse(s.Target)
	if err != nil {
		st.Error = err.Error()
		return
	}
	host := u.Host
	if u.Port() == "" {
		if u.Scheme == "https" {
			host = net.JoinHostPort(u.Hostname(), "443")
		} else {
			host = net.JoinHostPort(u.Hostname(), "80")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	t := time.Now()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", host)
	if err != nil {
		st.Error = err.Error()
		return
	}
	_ = conn.Close()
	st.Up = true
	st.LatencyMs = time.Since(t).Milliseconds()
}

func checkCert(c *Config, s *Site, st *siteStatus) {
	switch s.TLS {
	case "manual":
		cert, err := manualCerts.get(s.CertFile, s.KeyFile)
		if err != nil {
			st.CertError = err.Error()
			return
		}
		leaf := cert.Leaf
		if leaf == nil && len(cert.Certificate) > 0 {
			leaf, _ = x509.ParseCertificate(cert.Certificate[0])
		}
		if leaf != nil {
			st.CertUntil = leaf.NotAfter.Format("2006-01-02")
		}
	case "acme":
		// autocert.DirCache stores key+chain PEM in a file named after the domain
		data, err := os.ReadFile(filepath.Join(resolvePath(c.CertDir), s.Domain))
		if err != nil {
			st.CertError = "chưa có (sẽ tự xin khi có lượt truy cập HTTPS đầu tiên)"
			return
		}
		for len(data) > 0 {
			var b *pem.Block
			if b, data = pem.Decode(data); b == nil {
				break
			}
			if b.Type == "CERTIFICATE" {
				if leaf, err := x509.ParseCertificate(b.Bytes); err == nil {
					st.CertUntil = leaf.NotAfter.Format("2006-01-02")
				}
				break
			}
		}
	}
}

func apiLog(w http.ResponseWriter, _ *http.Request) {
	jsonOK(w, recentLog())
}

// ── helpers ──────────────────────────────────────────────────────

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
