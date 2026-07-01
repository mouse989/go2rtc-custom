package api

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/auth"
	"github.com/rs/zerolog"
	"golang.org/x/crypto/acme/autocert"
)

func Init() {
	var cfg struct {
		Mod struct {
			Listen     string `yaml:"listen"`
			BasePath   string `yaml:"base_path"`
			StaticDir  string `yaml:"static_dir"`
			Origin     string `yaml:"origin"`
			TLSListen  string `yaml:"tls_listen"`
			TLSCert    string `yaml:"tls_cert"`
			TLSKey     string `yaml:"tls_key"`
			UnixListen string `yaml:"unix_listen"`

			// ACME / Let's Encrypt auto-cert.
			// Set acme_domain + acme_email to enable. A temporary HTTP server
			// on :80 handles the HTTP-01 challenge; certs are stored in
			// <config_dir>/certs/ and auto-renewed before expiry.
			ACMEDomain string `yaml:"acme_domain"` // e.g. "nvr.example.com"
			ACMEEmail  string `yaml:"acme_email"`  // contact email for Let's Encrypt

			AllowPaths []string `yaml:"allow_paths"`
		} `yaml:"api"`
	}

	// default config
	cfg.Mod.Listen = ":1984"

	// load config from YAML
	app.LoadConfig(&cfg)

	if cfg.Mod.Listen == "" && cfg.Mod.UnixListen == "" && cfg.Mod.TLSListen == "" {
		return
	}

	allowPaths = cfg.Mod.AllowPaths
	basePath = cfg.Mod.BasePath
	log = app.GetLogger("api")

	initStatic(cfg.Mod.StaticDir)

	HandleFunc("api", apiHandler)
	HandleFunc("api/config", configHandler)
	HandleFunc("api/exit", exitHandler)
	HandleFunc("api/restart", restartHandler)
	HandleFunc("api/log", logHandler)

	Handler = http.DefaultServeMux // 4th

	if cfg.Mod.Origin == "*" {
		Handler = middlewareCORS(Handler) // 3rd
	}

	Handler = auth.Middleware(Handler) // 2nd - JWT auth + permission

	// Tell proxy handlers where to forward loopback requests
	auth.SetListenAddr(cfg.Mod.Listen)

	if log.Trace().Enabled() {
		Handler = middlewareLog(Handler) // 1st
	}

	if cfg.Mod.Listen != "" {
		_, port, _ := net.SplitHostPort(cfg.Mod.Listen)
		Port, _ = strconv.Atoi(port)
		go listen("tcp", cfg.Mod.Listen)
	}

	if cfg.Mod.UnixListen != "" {
		_ = syscall.Unlink(cfg.Mod.UnixListen)
		go listen("unix", cfg.Mod.UnixListen)
	}

	// Initialize the HTTPS server
	if cfg.Mod.TLSListen != "" && cfg.Mod.TLSCert != "" && cfg.Mod.TLSKey != "" {
		go tlsListen("tcp", cfg.Mod.TLSListen, cfg.Mod.TLSCert, cfg.Mod.TLSKey)
	}

	// ACME / Let's Encrypt auto-cert (takes priority over manual tls_cert/tls_key).
	if cfg.Mod.ACMEDomain != "" {
		if m := newACMEManager(cfg.Mod.ACMEDomain, cfg.Mod.ACMEEmail); m != nil {
			acmeManager = m
			go acmeListen(m, cfg.Mod.TLSListen)
		}
	}
}

func listen(network, address string) {
	ln, err := net.Listen(network, address)
	if err != nil {
		log.Error().Err(err).Msg("[api] listen")
		return
	}

	log.Info().Str("addr", address).Msg("[api] listen")

	server := http.Server{
		Handler:           Handler,
		ReadHeaderTimeout: 5 * time.Second, // Example: Set to 5 seconds
	}
	if err = server.Serve(ln); err != nil {
		log.Fatal().Err(err).Msg("[api] serve")
	}
}

func tlsListen(network, address, certFile, keyFile string) {
	var cert tls.Certificate
	var err error
	if strings.IndexByte(certFile, '\n') < 0 && strings.IndexByte(keyFile, '\n') < 0 {
		// check if file path
		cert, err = tls.LoadX509KeyPair(certFile, keyFile)
	} else {
		// if text file content
		cert, err = tls.X509KeyPair([]byte(certFile), []byte(keyFile))
	}
	if err != nil {
		log.Error().Err(err).Caller().Send()
		return
	}

	ln, err := net.Listen(network, address)
	if err != nil {
		log.Error().Err(err).Msg("[api] tls listen")
		return
	}

	log.Info().Str("addr", address).Msg("[api] tls listen")

	server := &http.Server{
		Handler:           Handler,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}},
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err = server.ServeTLS(ln, "", ""); err != nil {
		log.Fatal().Err(err).Msg("[api] tls serve")
	}
}

// acmeManager is set once ACME is configured, so other TLS listeners in the
// process (e.g. the RTSPS server) can reuse the same Let's Encrypt
// account/cert cache/renewal instead of needing their own cert files.
var acmeManager *autocert.Manager

// ACMEManager returns the shared autocert.Manager if api.acme_domain is
// configured, or nil otherwise.
func ACMEManager() *autocert.Manager {
	return acmeManager
}

// newACMEManager builds the autocert.Manager backing ACME auto-certs.
// Certificates are cached in <config_dir>/certs/ so they survive restarts.
// Renewal happens automatically in the background ~30 days before expiry.
func newACMEManager(domain, email string) *autocert.Manager {
	cacheDir := "certs"
	if d := app.ConfigPath; d != "" {
		cacheDir = filepath.Join(filepath.Dir(d), "certs")
	}
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		log.Error().Err(err).Msg("[acme] cannot create cert cache dir")
		return nil
	}

	return &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(domain),
		Cache:      autocert.DirCache(cacheDir),
		Email:      email,
	}
}

// acmeListen serves HTTPS on httpsAddr (default ":443") using m, and runs a
// temporary HTTP server on :80 solely to answer HTTP-01 ACME challenges.
func acmeListen(m *autocert.Manager, httpsAddr string) {
	if httpsAddr == "" {
		httpsAddr = ":443"
	}

	// HTTP server on :80 — serves ONLY ACME challenge tokens; all other paths
	// return 301 redirect to HTTPS. Windows Firewall must allow port 80 inbound.
	httpSrv := &http.Server{
		Addr:              ":80",
		Handler:           m.HTTPHandler(http.HandlerFunc(redirectHTTPS)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Info().Msg("[acme] HTTP challenge listener :80")
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("[acme] HTTP :80 failed — ACME challenges will not work; ensure port 80 is open")
		}
	}()

	tlsCfg := m.TLSConfig()
	tlsCfg.MinVersion = tls.VersionTLS12

	ln, err := net.Listen("tcp", httpsAddr)
	if err != nil {
		log.Error().Err(err).Str("addr", httpsAddr).Msg("[acme] HTTPS listen failed")
		return
	}

	log.Info().Str("addr", httpsAddr).Msg("[acme] HTTPS server ready")

	srv := &http.Server{
		Handler:           Handler,
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err = srv.ServeTLS(ln, "", ""); err != nil {
		log.Fatal().Err(err).Msg("[acme] HTTPS serve")
	}
}

func redirectHTTPS(w http.ResponseWriter, r *http.Request) {
	target := "https://" + r.Host + r.RequestURI
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

var Port int

const (
	MimeJSON = "application/json"
	MimeText = "text/plain"
)

var Handler http.Handler

// HandleFunc handle pattern with relative path:
// - "api/streams" => "{basepath}/api/streams"
// - "/streams"    => "/streams"
func HandleFunc(pattern string, handler http.HandlerFunc) {
	if len(pattern) == 0 || pattern[0] != '/' {
		pattern = basePath + "/" + pattern
	}
	if allowPaths != nil && !slices.Contains(allowPaths, pattern) {
		log.Trace().Str("path", pattern).Msg("[api] ignore path not in allow_paths")
		return
	}
	log.Trace().Str("path", pattern).Msg("[api] register path")
	http.HandleFunc(pattern, handler)
}

// ResponseJSON important always add Content-Type
// so go won't need to call http.DetectContentType
func ResponseJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", MimeJSON)
	_ = json.NewEncoder(w).Encode(v)
}

func ResponsePrettyJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", MimeJSON)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func Response(w http.ResponseWriter, body any, contentType string) {
	w.Header().Set("Content-Type", contentType)

	switch v := body.(type) {
	case []byte:
		_, _ = w.Write(v)
	case string:
		_, _ = w.Write([]byte(v))
	default:
		_, _ = fmt.Fprint(w, body)
	}
}

const StreamNotFound = "stream not found"

var allowPaths []string
var basePath string
var log zerolog.Logger

func middlewareLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Trace().Msgf("[api] %s %s %s", r.Method, r.URL, r.RemoteAddr)
		next.ServeHTTP(w, r)
	})
}

func middlewareCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		next.ServeHTTP(w, r)
	})
}

var mu sync.Mutex

func apiHandler(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	app.Info["host"] = r.Host
	mu.Unlock()

	ResponseJSON(w, app.Info)
}

func exitHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "", http.StatusBadRequest)
		return
	}

	s := r.URL.Query().Get("code")
	code, err := strconv.Atoi(s)

	// https://pubs.opengroup.org/onlinepubs/9699919799/utilities/V3_chap02.html#tag_18_08_02
	if err != nil || code < 0 || code > 125 {
		http.Error(w, "Code must be in the range [0, 125]", http.StatusBadRequest)
		return
	}

	os.Exit(code)
}

func restartHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "", http.StatusBadRequest)
		return
	}

	path, err := os.Executable()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Debug().Msgf("[api] restart %s", path)

	go syscall.Exec(path, os.Args, os.Environ())
}

func logHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		// Send current state of the log file immediately
		w.Header().Set("Content-Type", "application/jsonlines")
		_, _ = app.MemoryLog.WriteTo(w)
	case "DELETE":
		app.MemoryLog.Reset()
		Response(w, "OK", "text/plain")
	default:
		http.Error(w, "Method not allowed", http.StatusBadRequest)
	}
}

type Source struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	Info     string `json:"info,omitempty"`
	URL      string `json:"url,omitempty"`
	Location string `json:"location,omitempty"`
}

func ResponseSources(w http.ResponseWriter, sources []*Source) {
	if len(sources) == 0 {
		http.Error(w, "no sources", http.StatusNotFound)
		return
	}

	var response = struct {
		Sources []*Source `json:"sources"`
	}{
		Sources: sources,
	}
	ResponseJSON(w, response)
}

func Error(w http.ResponseWriter, err error) {
	log.Error().Err(err).Caller(1).Send()

	http.Error(w, err.Error(), http.StatusInsufficientStorage)
}
