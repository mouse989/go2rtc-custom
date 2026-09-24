package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Site is one website served over the shared HTTPS port: requests whose
// Host header matches Domain are forwarded to Target.
type Site struct {
	ID      string `json:"id"`
	Name    string `json:"name"`   // free-text label, e.g. "go2rtc"
	Domain  string `json:"domain"` // e.g. "cam.example.com"
	Target  string `json:"target"` // e.g. "http://127.0.0.1:1984"
	Enabled bool   `json:"enabled"`

	// TLS: "acme" (Let's Encrypt, automatic), "manual" (CertFile/KeyFile)
	// or "none" (plain HTTP only).
	TLS      string `json:"tls"`
	CertFile string `json:"cert_file,omitempty"`
	KeyFile  string `json:"key_file,omitempty"`

	AllowHTTP          bool `json:"allow_http"`           // serve plain HTTP too instead of redirecting to HTTPS
	RewriteHost        bool `json:"rewrite_host"`         // send the target's host as Host header (default: keep the visitor's)
	InsecureSkipVerify bool `json:"insecure_skip_verify"` // for https:// targets with self-signed certificates
	TimeoutSec         int  `json:"timeout_sec"`          // max wait for backend response headers; 0 = 120s
}

type Config struct {
	HTTPListen  string `json:"http_listen"`  // ":80" (comma-separated list allowed)
	HTTPSListen string `json:"https_listen"` // ":443" (comma-separated list allowed)
	AdminListen string `json:"admin_listen"` // "127.0.0.1:8090"
	AdminDomain string `json:"admin_domain"` // optional: also serve this admin page over HTTPS at this domain

	ACMEEmail   string `json:"acme_email"`
	CertDir     string `json:"cert_dir"`     // ACME certificate cache, relative to the config file
	DefaultSite string `json:"default_site"` // site ID used for requests with an unknown Host / by IP

	AdminUser     string `json:"admin_user"`
	AdminPassHash string `json:"admin_password_hash"`

	Sites []*Site `json:"sites"`
}

var (
	cfgMu   sync.Mutex
	cfg     *Config
	cfgPath string
)

func defaultConfig() *Config {
	return &Config{
		HTTPListen:  ":80",
		HTTPSListen: ":443",
		AdminListen: "127.0.0.1:8090",
		CertDir:     "certs",
		AdminUser:   "admin",
	}
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := defaultConfig()
	if err = json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, s := range c.Sites {
		normalizeSite(s)
	}
	return c, nil
}

// saveConfig writes c atomically (temp file + rename) so a crash mid-write
// never leaves a truncated config behind.
func saveConfig(c *Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := cfgPath + ".tmp"
	if err = os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, cfgPath)
}

// cloneConfig returns a deep copy, so request handlers can build a modified
// config and validate it before swapping it in.
func cloneConfig(c *Config) *Config {
	n := *c
	n.Sites = make([]*Site, len(c.Sites))
	for i, s := range c.Sites {
		cp := *s
		n.Sites[i] = &cp
	}
	return &n
}

// resolvePath makes p relative to the config file's directory.
func resolvePath(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(filepath.Dir(cfgPath), p)
}

func normalizeSite(s *Site) {
	s.Name = strings.TrimSpace(s.Name)
	s.Domain = strings.ToLower(strings.TrimSpace(s.Domain))
	s.Target = strings.TrimSpace(s.Target)
	if s.Target != "" && !strings.Contains(s.Target, "://") {
		s.Target = "http://" + s.Target
	}
	s.CertFile = strings.TrimSpace(s.CertFile)
	s.KeyFile = strings.TrimSpace(s.KeyFile)
	switch s.TLS {
	case "acme", "manual", "none":
	default:
		s.TLS = "acme"
	}
}

var domainRe = regexp.MustCompile(`^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

func validateSite(s *Site) error {
	if s.Domain == "" {
		return errors.New("thiếu domain")
	}
	if !domainRe.MatchString(s.Domain) {
		return fmt.Errorf("domain không hợp lệ: %q", s.Domain)
	}
	if strings.HasPrefix(s.Domain, "*.") && s.TLS == "acme" {
		return errors.New("Let's Encrypt (HTTP-01) không cấp chứng chỉ wildcard — dùng chứng chỉ thủ công")
	}
	u, err := url.Parse(s.Target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("target không hợp lệ: %q (ví dụ http://127.0.0.1:1984)", s.Target)
	}
	if s.TLS == "manual" && (s.CertFile == "" || s.KeyFile == "") {
		return errors.New("chứng chỉ thủ công cần đường dẫn file cert và key")
	}
	if s.TimeoutSec < 0 || s.TimeoutSec > 3600 {
		return errors.New("timeout phải từ 0 đến 3600 giây")
	}
	return nil
}

func validateConfig(c *Config) error {
	for _, l := range []struct{ name, v string }{
		{"HTTP", c.HTTPListen}, {"HTTPS", c.HTTPSListen}, {"trang quản trị", c.AdminListen},
	} {
		for _, a := range splitAddrs(l.v) {
			if _, _, err := net.SplitHostPort(a); err != nil {
				return fmt.Errorf("địa chỉ lắng nghe %s không hợp lệ: %q (ví dụ :443 hoặc 0.0.0.0:443)", l.name, a)
			}
		}
	}
	if len(splitAddrs(c.AdminListen)) != 1 {
		return errors.New("trang quản trị cần đúng một địa chỉ lắng nghe")
	}
	c.AdminDomain = strings.ToLower(strings.TrimSpace(c.AdminDomain))
	if c.AdminDomain != "" && !domainRe.MatchString(c.AdminDomain) {
		return fmt.Errorf("domain trang quản trị không hợp lệ: %q", c.AdminDomain)
	}

	seen := map[string]bool{}
	if c.AdminDomain != "" {
		seen[c.AdminDomain] = true
	}
	defaultFound := c.DefaultSite == ""
	for _, s := range c.Sites {
		normalizeSite(s)
		if err := validateSite(s); err != nil {
			return err
		}
		if seen[s.Domain] {
			return fmt.Errorf("domain %q bị trùng", s.Domain)
		}
		seen[s.Domain] = true
		if s.ID == c.DefaultSite {
			defaultFound = true
		}
	}
	if !defaultFound {
		c.DefaultSite = ""
	}
	return nil
}

func splitAddrs(s string) []string {
	var out []string
	for _, a := range strings.Split(s, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

func randID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// importGo2rtcSites reads go2rtc's old reverse_proxy.json (the embedded
// proxy this program replaces) and appends its sites.
func importGo2rtcSites(c *Config, path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var list []struct {
		Domain  string `json:"domain"`
		Target  string `json:"target"`
		Enabled bool   `json:"enabled"`
	}
	if err = json.Unmarshal(data, &list); err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	existing := map[string]bool{}
	for _, s := range c.Sites {
		existing[s.Domain] = true
	}
	n := 0
	for _, it := range list {
		s := &Site{ID: randID(6), Domain: it.Domain, Target: it.Target, Enabled: it.Enabled, TLS: "acme"}
		normalizeSite(s)
		if existing[s.Domain] || validateSite(s) != nil {
			continue
		}
		existing[s.Domain] = true
		c.Sites = append(c.Sites, s)
		n++
	}
	return n, nil
}
