package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/auth"
)

// ProxySite is one "other website on this server" that go2rtc terminates
// HTTPS for (reusing its own ACME certificate) and forwards to a local
// backend by Host header — so that site never needs its own SSL setup.
type ProxySite struct {
	ID      string `json:"id"`
	Domain  string `json:"domain"` // e.g. "app1.example.com" — matched against the request Host header
	Target  string `json:"target"` // e.g. "127.0.0.1:3000" or "http://127.0.0.1:3000"
	Enabled bool   `json:"enabled"`
}

var (
	proxyMu       sync.RWMutex
	proxySites    = map[string]*ProxySite{} // by ID
	proxyByDomain = map[string]*ProxySite{} // by domain
	proxyHandlers = map[string]*httputil.ReverseProxy{}
	proxySiteFile string
)

func initReverseProxy() {
	proxySiteFile = "reverse_proxy.json"
	if app.ConfigPath != "" {
		proxySiteFile = filepath.Join(filepath.Dir(app.ConfigPath), "reverse_proxy.json")
	}
	if err := loadProxySites(); err != nil && !os.IsNotExist(err) {
		log.Warn().Err(err).Msg("[api] could not load reverse_proxy.json, starting empty")
	}
	HandleFunc("api/reverse-proxy", handleReverseProxySites)
}

func loadProxySites() error {
	data, err := os.ReadFile(proxySiteFile)
	if err != nil {
		return err
	}
	var list []*ProxySite
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	proxyMu.Lock()
	defer proxyMu.Unlock()
	proxySites = map[string]*ProxySite{}
	proxyByDomain = map[string]*ProxySite{}
	proxyHandlers = map[string]*httputil.ReverseProxy{}
	for _, s := range list {
		proxySites[s.ID] = s
		proxyByDomain[s.Domain] = s
	}
	return nil
}

// saveProxySitesLocked persists the current site list. Caller must hold proxyMu.
func saveProxySitesLocked() error {
	list := make([]*ProxySite, 0, len(proxySites))
	for _, s := range proxySites {
		list = append(list, s)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(proxySiteFile, data, 0644)
}

func listProxySites() []*ProxySite {
	proxyMu.RLock()
	defer proxyMu.RUnlock()
	out := make([]*ProxySite, 0, len(proxySites))
	for _, s := range proxySites {
		out = append(out, s)
	}
	return out
}

func upsertProxySite(s *ProxySite) error {
	s.Domain = strings.ToLower(strings.TrimSpace(s.Domain))
	s.Target = strings.TrimSpace(s.Target)
	if s.Domain == "" || s.Target == "" {
		return errors.New("domain and target are required")
	}
	if primaryACMEDomain != "" && s.Domain == strings.ToLower(primaryACMEDomain) {
		return errors.New("domain is this server's own Web UI domain (api.acme_domain) — proxying it elsewhere would lock you out")
	}

	proxyMu.Lock()
	defer proxyMu.Unlock()

	for id, other := range proxySites {
		if other.Domain == s.Domain && id != s.ID {
			return errors.New("domain already used by another site")
		}
	}

	if s.ID == "" {
		s.ID = randProxyID()
	} else if old, ok := proxySites[s.ID]; ok && old.Domain != s.Domain {
		delete(proxyByDomain, old.Domain)
		delete(proxyHandlers, old.Domain)
	}

	proxySites[s.ID] = s
	proxyByDomain[s.Domain] = s
	delete(proxyHandlers, s.Domain) // force rebuild with the (possibly new) target
	return saveProxySitesLocked()
}

func deleteProxySite(id string) error {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	s, ok := proxySites[id]
	if !ok {
		return errors.New("not found")
	}
	delete(proxySites, id)
	delete(proxyByDomain, s.Domain)
	delete(proxyHandlers, s.Domain)
	return saveProxySitesLocked()
}

// proxyDomains returns every enabled reverse-proxy domain, so the shared
// ACME manager knows to issue/renew certificates for them too.
func proxyDomains() []string {
	proxyMu.RLock()
	defer proxyMu.RUnlock()
	out := make([]string, 0, len(proxyByDomain))
	for d, s := range proxyByDomain {
		if s.Enabled {
			out = append(out, d)
		}
	}
	return out
}

func randProxyID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// reverseProxyMiddleware forwards requests for a configured Host straight to
// its backend target, bypassing go2rtc's own routing/auth entirely — those
// are unrelated apps, not go2rtc pages. Requests for any other Host fall
// through to next unchanged.
func reverseProxyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}

		proxyMu.RLock()
		site, ok := proxyByDomain[host]
		proxyMu.RUnlock()
		if !ok || !site.Enabled {
			next.ServeHTTP(w, r)
			return
		}

		rp := getOrBuildProxy(site)
		if rp == nil {
			http.Error(w, "bad gateway: invalid reverse-proxy target", http.StatusBadGateway)
			return
		}
		rp.ServeHTTP(w, r)
	})
}

func getOrBuildProxy(site *ProxySite) *httputil.ReverseProxy {
	proxyMu.RLock()
	rp := proxyHandlers[site.Domain]
	proxyMu.RUnlock()
	if rp != nil {
		return rp
	}

	target := site.Target
	if !strings.Contains(target, "://") {
		target = "http://" + target
	}
	u, err := url.Parse(target)
	if err != nil {
		log.Error().Err(err).Str("domain", site.Domain).Msg("[api] invalid reverse-proxy target")
		return nil
	}

	newRP := httputil.NewSingleHostReverseProxy(u)
	proxyMu.Lock()
	proxyHandlers[site.Domain] = newRP
	proxyMu.Unlock()
	return newRP
}

// HTTP API — admin only.

func requireProxyAdmin(w http.ResponseWriter, r *http.Request) bool {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user.Role != auth.RoleAdmin {
		http.Error(w, "admin only", http.StatusForbidden)
		return false
	}
	return true
}

func handleReverseProxySites(w http.ResponseWriter, r *http.Request) {
	if !requireProxyAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		ResponseJSON(w, listProxySites())

	case http.MethodPost, http.MethodPut:
		var s ProxySite
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := upsertProxySite(&s); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ResponseJSON(w, s)

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		if err := deleteProxySite(id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
