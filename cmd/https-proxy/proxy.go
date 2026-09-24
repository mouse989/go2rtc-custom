package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// runtimeSite is a Site plus its ready-to-use reverse proxy.
type runtimeSite struct {
	*Site
	rp *httputil.ReverseProxy
}

// routes is an immutable snapshot of the routing table; handlers read it
// via an atomic pointer so applying new settings never blocks traffic.
type routes struct {
	byDomain    map[string]*runtimeSite
	wildcards   []*runtimeSite // "*.example.com"
	defaultSite *runtimeSite
	adminDomain string
	httpsPort   string // for http→https redirects ("" = 443)
	acme        *autocert.Manager
	acmeAllowed map[string]bool
}

var current atomic.Pointer[routes]

func buildRoutes(c *Config) *routes {
	rt := &routes{byDomain: map[string]*runtimeSite{}, adminDomain: c.AdminDomain}

	if addrs := splitAddrs(c.HTTPSListen); len(addrs) > 0 {
		if _, port, err := net.SplitHostPort(addrs[0]); err == nil && port != "443" {
			rt.httpsPort = port
		}
	}

	for _, s := range c.Sites {
		if !s.Enabled {
			continue
		}
		rs := &runtimeSite{Site: s, rp: newReverseProxy(s)}
		if strings.HasPrefix(s.Domain, "*.") {
			rt.wildcards = append(rt.wildcards, rs)
		} else {
			rt.byDomain[s.Domain] = rs
		}
		if s.ID == c.DefaultSite {
			rt.defaultSite = rs
		}
	}

	rt.acme = newACME(c, rt)
	return rt
}

func (rt *routes) isAdmin(host string) bool {
	return rt.adminDomain != "" && host == rt.adminDomain
}

func (rt *routes) lookup(host string) *runtimeSite {
	if s := rt.byDomain[host]; s != nil {
		return s
	}
	for _, s := range rt.wildcards {
		if strings.HasSuffix(host, s.Domain[1:]) { // ".example.com"
			return s
		}
	}
	return nil
}

var (
	acmeMu  sync.Mutex
	acmeMgr *autocert.Manager
	acmeKey string
)

// newACME returns the Let's Encrypt manager, reusing the existing one while
// the cache dir and email stay the same so in-flight issuance survives a
// settings change. Which domains may get certificates is read live from the
// current routes.
func newACME(c *Config, rt *routes) *autocert.Manager {
	rt.acmeAllowed = map[string]bool{}
	for d, s := range rt.byDomain {
		if s.TLS == "acme" {
			rt.acmeAllowed[d] = true
		}
	}
	if c.AdminDomain != "" && rt.byDomain[c.AdminDomain] == nil {
		rt.acmeAllowed[c.AdminDomain] = true
	}
	if len(rt.acmeAllowed) == 0 {
		return nil
	}

	dir := resolvePath(c.CertDir)
	acmeMu.Lock()
	defer acmeMu.Unlock()
	if key := dir + "|" + c.ACMEEmail; acmeMgr != nil && key == acmeKey {
		return acmeMgr
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		logf("[acme] không tạo được thư mục chứng chỉ %s: %v", dir, err)
		return nil
	}
	acmeKey = dir + "|" + c.ACMEEmail
	acmeMgr = &autocert.Manager{
		Prompt: autocert.AcceptTOS,
		Cache:  autocert.DirCache(dir),
		Email:  c.ACMEEmail,
		HostPolicy: func(_ context.Context, host string) error {
			if rt := current.Load(); rt != nil && rt.acmeAllowed[host] {
				return nil
			}
			return fmt.Errorf("acme: domain %q chưa được cấu hình dùng Let's Encrypt", host)
		},
	}
	return acmeMgr
}

// ── Reverse proxy ────────────────────────────────────────────────

func newReverseProxy(s *Site) *httputil.ReverseProxy {
	target, _ := url.Parse(s.Target) // validated before
	timeout := time.Duration(s.TimeoutSec) * time.Second
	if timeout == 0 {
		timeout = 120 * time.Second
	}

	transport := &http.Transport{
		Proxy: nil, // never route backend traffic through a system proxy
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		ResponseHeaderTimeout: timeout,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: s.InsecureSkipVerify},
	}

	name := s.Domain
	rewriteHost := s.RewriteHost

	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			if !rewriteHost {
				pr.Out.Host = pr.In.Host
			}
			// X-Forwarded-For/Host/Proto are rebuilt from the real
			// connection (Rewrite drops whatever the client sent), so the
			// backend can trust them.
			pr.SetXForwarded()
			// go2rtc trusts this header from loopback — never pass it on.
			pr.Out.Header.Del("X-Internal")
		},
		Transport:     transport,
		FlushInterval: -1, // stream immediately (MJPEG, HLS, SSE, long polls)
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				return // visitor went away
			}
			logf("[proxy] %s → %s: %v", name, target.Host, err)
			status := http.StatusBadGateway
			msg := "Máy chủ phía sau không phản hồi"
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				status = http.StatusGatewayTimeout
				msg = "Máy chủ phía sau phản hồi quá lâu"
			}
			errorPage(w, status, msg, name)
		},
	}
}

func errorPage(w http.ResponseWriter, status int, msg, site string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>%d</title>
<body style="font-family:system-ui;background:#0f172a;color:#e2e8f0;display:grid;place-items:center;height:100vh;margin:0">
<div style="text-align:center"><h1 style="font-size:3rem;margin:0">%d</h1><p>%s</p>
<p style="color:#64748b;font-size:.85rem">%s</p></div>`, status, status, html.EscapeString(msg), html.EscapeString(site))
}

func requestHost(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// ── Handlers ─────────────────────────────────────────────────────

func httpsHandler(w http.ResponseWriter, r *http.Request) {
	rt := current.Load()
	host := requestHost(r)
	if rt.isAdmin(host) {
		adminMux.ServeHTTP(w, r)
		return
	}
	site := rt.lookup(host)
	if site == nil {
		site = rt.defaultSite
	}
	if site == nil {
		errorPage(w, http.StatusNotFound, "Domain chưa được cấu hình", host)
		return
	}
	w.Header().Set("Strict-Transport-Security", "max-age=31536000")
	site.rp.ServeHTTP(w, r)
}

// httpHandler serves port 80: ACME HTTP-01 challenges, redirects to HTTPS,
// and plain-HTTP proxying for sites that allow it.
func httpHandler(w http.ResponseWriter, r *http.Request) {
	rt := current.Load()
	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := requestHost(r)
		site := rt.lookup(host)
		if site == nil && !rt.isAdmin(host) {
			site = rt.defaultSite
		}
		if site != nil && (site.AllowHTTP || site.TLS == "none") {
			site.rp.ServeHTTP(w, r)
			return
		}
		if site == nil && !rt.isAdmin(host) {
			errorPage(w, http.StatusNotFound, "Domain chưa được cấu hình", host)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "use HTTPS", http.StatusBadRequest)
			return
		}
		target := "https://" + host
		if rt.httpsPort != "" {
			target += ":" + rt.httpsPort
		}
		http.Redirect(w, r, target+r.URL.RequestURI(), http.StatusMovedPermanently)
	})
	if rt.acme != nil {
		rt.acme.HTTPHandler(fallback).ServeHTTP(w, r)
		return
	}
	fallback.ServeHTTP(w, r)
}

// ── Certificates ─────────────────────────────────────────────────

func getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	rt := current.Load()
	name := strings.ToLower(strings.TrimSuffix(hello.ServerName, "."))

	var site *runtimeSite
	if name != "" {
		site = rt.lookup(name)
	}
	if site == nil && !rt.isAdmin(name) {
		site = rt.defaultSite
	}
	if site != nil && site.TLS == "manual" {
		return manualCerts.get(site.CertFile, site.KeyFile)
	}
	if rt.acme == nil {
		return nil, fmt.Errorf("no certificate for %q", name)
	}
	if site != nil && (name == "" || site.Domain != name) && !strings.HasPrefix(site.Domain, "*.") {
		// visitor used an IP or unknown name: present the default site's cert
		hello.ServerName = site.Domain
	}
	return rt.acme.GetCertificate(hello)
}

type certEntry struct {
	cert    *tls.Certificate
	modTime time.Time
	checked time.Time
}

// certCache loads manual certificate files and re-reads them when they
// change on disk (e.g. renewed by win-acme), checking at most once a minute.
type certCache struct {
	mu sync.Mutex
	m  map[string]*certEntry
}

var manualCerts = &certCache{m: map[string]*certEntry{}}

func (c *certCache) get(certFile, keyFile string) (*tls.Certificate, error) {
	certFile, keyFile = resolvePath(certFile), resolvePath(keyFile)
	key := certFile + "|" + keyFile

	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.m[key]
	if e != nil && time.Since(e.checked) < time.Minute {
		return e.cert, nil
	}
	fi, err := os.Stat(certFile)
	if err != nil {
		if e != nil {
			return e.cert, nil
		}
		return nil, err
	}
	if e != nil && fi.ModTime().Equal(e.modTime) {
		e.checked = time.Now()
		return e.cert, nil
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		logf("[tls] lỗi đọc chứng chỉ %s: %v", certFile, err)
		if e != nil {
			return e.cert, nil
		}
		return nil, err
	}
	c.m[key] = &certEntry{cert: &cert, modTime: fi.ModTime(), checked: time.Now()}
	return &cert, nil
}

func tlsConfig() *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: getCertificate,
		NextProtos:     []string{"h2", "http/1.1", acme.ALPNProto},
	}
}

// ── Listeners ────────────────────────────────────────────────────

type listener struct {
	kind string // "http", "https", "admin"
	addr string
	srv  *http.Server
	err  error
}

var (
	lnMu      sync.Mutex
	listeners = map[string]*listener{} // key: kind+" "+addr
)

// applyListeners starts servers for newly configured addresses and stops
// those no longer configured; unchanged ones keep running untouched.
func applyListeners(c *Config) {
	want := map[string]*listener{}
	for _, a := range splitAddrs(c.HTTPListen) {
		want["http "+a] = &listener{kind: "http", addr: a}
	}
	for _, a := range splitAddrs(c.HTTPSListen) {
		want["https "+a] = &listener{kind: "https", addr: a}
	}
	for _, a := range splitAddrs(c.AdminListen) {
		want["admin "+a] = &listener{kind: "admin", addr: a}
	}

	lnMu.Lock()
	defer lnMu.Unlock()
	for k, l := range listeners {
		if _, ok := want[k]; ok && l.err == nil {
			continue
		}
		if l.srv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = l.srv.Shutdown(ctx)
			cancel()
		}
		delete(listeners, k)
	}
	for k, l := range want {
		if _, ok := listeners[k]; ok {
			continue
		}
		startListener(l)
		listeners[k] = l
	}
}

func startListener(l *listener) {
	srv := &http.Server{
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          newServerErrorLog(),
	}
	switch l.kind {
	case "http":
		srv.Handler = http.HandlerFunc(httpHandler)
	case "https":
		srv.Handler = http.HandlerFunc(httpsHandler)
		srv.TLSConfig = tlsConfig()
	case "admin":
		srv.Handler = adminMux
	}

	ln, err := net.Listen("tcp", l.addr)
	if err != nil {
		l.err = err
		logf("[%s] KHÔNG mở được cổng %s: %v", l.kind, l.addr, err)
		return
	}
	l.srv = srv
	logf("[%s] lắng nghe %s", l.kind, l.addr)
	go func() {
		var err error
		if l.kind == "https" {
			err = srv.ServeTLS(ln, "", "")
		} else {
			err = srv.Serve(ln)
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logf("[%s] %s dừng: %v", l.kind, l.addr, err)
		}
	}()
}

func listenerStatus() []map[string]string {
	lnMu.Lock()
	defer lnMu.Unlock()
	out := make([]map[string]string, 0, len(listeners))
	for _, l := range listeners {
		m := map[string]string{"kind": l.kind, "addr": l.addr}
		if l.err != nil {
			m["error"] = l.err.Error()
		}
		out = append(out, m)
	}
	return out
}

// apply makes c the running configuration.
func apply(c *Config) {
	current.Store(buildRoutes(c))
	applyListeners(c)
}
