package auth

// geoblock.go — "block access from outside Vietnam" feature: every request
// to the HTTP API/web UI is checked (see Middleware in middleware.go)
// against an allow-list of Vietnam IPv4/IPv6 CIDR ranges before anything
// else runs. A request from outside Vietnam gets a fixed "Platform not
// available in your region" page instead of whatever it asked for.
//
// The IP-range list comes from two places, merged together:
//   - Auto-fetch (GeoBlockAutoUpdate): periodically downloads plain-text,
//     one-CIDR-per-line country IP lists from an admin-configured URL —
//     ipdeny.com's free, no-auth-required per-country zone files by
//     default (see defaultGeoBlockSourceURLv4/v6). Requires this server to
//     have outbound internet access to that URL.
//   - Manual (GeoBlockManualCIDRs): CIDRs an admin typed in by hand —
//     always included, on top of whatever auto-fetch produced, so a gap in
//     the upstream list (or a single known-good address outside it) can be
//     patched immediately without waiting on the next scheduled fetch.
//
// A request is NEVER blocked just because the feature has nothing loaded
// yet (first boot before the first successful fetch, or every fetch so far
// has failed) — that fails OPEN (allows the request) rather than closed,
// because failing closed here means "the whole platform is down for
// everyone" instead of "the one new feature silently isn't blocking
// anyone yet". Likewise a private/loopback/link-local/CGNAT address is
// always exempt — those have no real-world geography, and blocking them
// would break LAN/VPN access and this server's own loopback calls
// (internal/auth's counting-subsystem bypass, proxied requests from a
// reverse proxy on the same host, …) regardless of where the public
// internet path to this server happens to originate.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// defaultGeoBlockSourceURLv4/v6: ipdeny.com publishes free, no-auth,
	// plain-text one-CIDR-per-line country IP block lists — a long-
	// established source several "block by country" setups already use
	// (e.g. nginx's geo module configs). Admin can override either with
	// GeoBlockSourceURLv4/v6 to point at another source serving the same
	// plain-text format.
	defaultGeoBlockSourceURLv4 = "https://www.ipdeny.com/ipblocks/data/countries/vn.zone"
	defaultGeoBlockSourceURLv6 = "https://www.ipdeny.com/ipv6/ipaddresses/aggregated/vn-aggregated.zone"

	defaultGeoBlockUpdateIntervalHours = 168 // weekly — allocated ranges change rarely
	minGeoBlockUpdateIntervalHours     = 1

	geoBlockFetchTimeout = 30 * time.Second
	geoBlockMaxBodyBytes = 8 << 20 // 8MB — a country zone file is a few hundred KB at most; this just bounds a broken/malicious source
	geoBlockMessageVI    = "Hiện tại, Platform chưa khả dụng ở khu vực của bạn."
)

// geoBlockCache is what's persisted to disk between restarts, so a reboot
// doesn't lose the last successfully fetched list while waiting for the
// next scheduled update. A failed fetch never clears a previously good
// list here — only LastError/LastErrorAt change — so a transient upstream
// outage can't turn into "block all of Vietnam".
type geoBlockCache struct {
	FetchedAt   time.Time `json:"fetched_at,omitempty"`
	SourceV4    []string  `json:"source_v4,omitempty"`
	SourceV6    []string  `json:"source_v6,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	LastErrorAt time.Time `json:"last_error_at,omitempty"`
}

type ipv4Range struct{ start, end uint32 }

// geoBlockMatcher is an immutable compiled snapshot — recompiled (never
// mutated) whenever the fetched lists or the manual list change, and
// swapped in under geoBlockMu so lookups never block a concurrent rebuild.
type geoBlockMatcher struct {
	v4 []ipv4Range    // sorted, non-overlapping, binary-searched
	v6 []netip.Prefix // typically far fewer entries — linear scan via Contains
}

func (m *geoBlockMatcher) empty() bool {
	return m == nil || (len(m.v4) == 0 && len(m.v6) == 0)
}

func (m *geoBlockMatcher) contains(addr netip.Addr) bool {
	if m == nil {
		return false
	}
	if addr.Is4() || addr.Is4In6() {
		a4 := addr.As4()
		ip := uint32(a4[0])<<24 | uint32(a4[1])<<16 | uint32(a4[2])<<8 | uint32(a4[3])
		i := sort.Search(len(m.v4), func(i int) bool { return m.v4[i].start > ip })
		return i > 0 && m.v4[i-1].end >= ip
	}
	for _, p := range m.v6 {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

var (
	geoBlockMu     sync.RWMutex
	geoBlockFile   string
	geoBlockCached geoBlockCache
	geoBlockMatch  *geoBlockMatcher

	geoBlockTimerMu sync.Mutex
	geoBlockTimer   *time.Timer
)

// initGeoBlock loads any persisted cache, compiles the initial matcher, and
// arms the auto-update scheduler if enabled. Non-fatal — a missing/corrupt
// cache file just means "nothing loaded yet", same as a brand-new install.
func initGeoBlock(dir string) {
	geoBlockFile = filepath.Join(dir, "geoblock_cache.json")
	if data, err := os.ReadFile(geoBlockFile); err == nil {
		var c geoBlockCache
		if json.Unmarshal(data, &c) == nil {
			geoBlockCached = c
		}
	}
	recompileGeoBlockMatcher()
	rearmGeoBlockScheduler()
}

func recompileGeoBlockMatcher() {
	s := GetSettings()
	geoBlockMu.Lock()
	m := compileGeoBlockMatcher(geoBlockCached.SourceV4, geoBlockCached.SourceV6, s.GeoBlockManualCIDRs)
	geoBlockMatch = m
	geoBlockMu.Unlock()
}

func compileGeoBlockMatcher(v4, v6, manual []string) *geoBlockMatcher {
	var ranges []ipv4Range
	var v6prefixes []netip.Prefix

	addOne := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || strings.HasPrefix(s, "#") {
			return
		}
		p, err := parseCIDROrIP(s)
		if err != nil {
			return // tolerate stray bad lines in an upstream/admin list rather than failing the whole thing
		}
		if p.Addr().Is4() || p.Addr().Is4In6() {
			a4 := p.Masked().Addr().As4()
			start := uint32(a4[0])<<24 | uint32(a4[1])<<16 | uint32(a4[2])<<8 | uint32(a4[3])
			size := uint32(1) << uint(32-p.Bits())
			if p.Bits() == 32 {
				size = 1
			}
			ranges = append(ranges, ipv4Range{start: start, end: start + size - 1})
		} else {
			v6prefixes = append(v6prefixes, p.Masked())
		}
	}
	for _, s := range v4 {
		addOne(s)
	}
	for _, s := range v6 {
		addOne(s)
	}
	for _, s := range manual {
		addOne(s)
	}

	sort.Slice(ranges, func(i, j int) bool { return ranges[i].start < ranges[j].start })
	// Merge overlapping/adjacent ranges so the binary search in contains()
	// is correct even if the source data has overlaps (sorted-by-start
	// binary search alone only works for disjoint intervals).
	merged := ranges[:0]
	for _, r := range ranges {
		if n := len(merged); n > 0 && r.start <= merged[n-1].end+1 {
			if r.end > merged[n-1].end {
				merged[n-1].end = r.end
			}
			continue
		}
		merged = append(merged, r)
	}

	return &geoBlockMatcher{v4: merged, v6: v6prefixes}
}

// parseCIDROrIP accepts either CIDR notation or a bare IP (treated as a
// /32 or /128) — admin-entered manual ranges are commonly just one address.
func parseCIDROrIP(s string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p, nil
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	bits := 32
	if addr.Is6() && !addr.Is4In6() {
		bits = 128
	}
	return netip.PrefixFrom(addr, bits), nil
}

var cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")

// isExemptFromGeoBlock reports whether addr should always be allowed
// regardless of the Vietnam list — private/loopback/link-local/CGNAT
// addresses have no real-world geography, so blocking them would break
// LAN/VPN access and this server's own internal calls.
func isExemptFromGeoBlock(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true // can't determine origin at all — fail open, never block blind
	}
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsUnspecified() {
		return true
	}
	if addr.Is4() && cgnatPrefix.Contains(addr) {
		return true
	}
	return false
}

// geoBlockAllows reports whether remoteIP may proceed. Called on every
// request (see Middleware) — must stay cheap: a settings copy + an RLock +
// a parse + an exemption check + a lookup, no I/O.
func geoBlockAllows(remoteIP string) bool {
	s := GetSettings()
	if !s.GeoBlockEnabled {
		return true
	}
	addr, err := netip.ParseAddr(remoteIP)
	if err != nil {
		return true // couldn't parse the client's address — fail open
	}
	if isExemptFromGeoBlock(addr) {
		return true
	}
	geoBlockMu.RLock()
	m := geoBlockMatch
	geoBlockMu.RUnlock()
	if m.empty() {
		return true // nothing loaded yet — fail open rather than lock out everyone
	}
	return m.contains(addr)
}

// ── Fetch + scheduler ────────────────────────────────────────────────────

func geoBlockUpdateInterval(s AppSettings) time.Duration {
	h := s.GeoBlockUpdateIntervalHours
	if h < minGeoBlockUpdateIntervalHours {
		h = defaultGeoBlockUpdateIntervalHours
	}
	return time.Duration(h) * time.Hour
}

// rearmGeoBlockScheduler (re)schedules the next auto-update fetch according
// to current settings — called at startup and whenever GeoBlock settings
// are saved, so toggling auto-update on/off or changing the interval takes
// effect immediately rather than only after a restart.
func rearmGeoBlockScheduler() {
	s := GetSettings()
	geoBlockTimerMu.Lock()
	if geoBlockTimer != nil {
		geoBlockTimer.Stop()
		geoBlockTimer = nil
	}
	if !s.GeoBlockAutoUpdate {
		geoBlockTimerMu.Unlock()
		return
	}
	interval := geoBlockUpdateInterval(s)
	geoBlockTimer = time.AfterFunc(interval, geoBlockTick)
	geoBlockTimerMu.Unlock()

	// First time auto-update is turned on (or the cache is still empty for
	// any other reason), don't make the admin wait up to a week for the
	// feature to actually start working.
	geoBlockMu.RLock()
	empty := len(geoBlockCached.SourceV4) == 0 && len(geoBlockCached.SourceV6) == 0
	geoBlockMu.RUnlock()
	if empty {
		go func() { _ = fetchAndStoreGeoBlockLists() }()
	}
}

func geoBlockTick() {
	_ = fetchAndStoreGeoBlockLists()
	rearmGeoBlockScheduler() // reschedule for the next cycle (also picks up an interval change)
}

// fetchAndStoreGeoBlockLists synchronously re-fetches the configured
// source URL(s), persists the result (cache file), and recompiles the
// matcher. A failed fetch keeps the previous good list — only the error
// fields are updated — so a transient upstream outage never clears a
// working allow-list. Exported behavior via TriggerGeoBlockUpdateNow.
func fetchAndStoreGeoBlockLists() error {
	s := GetSettings()
	urlV4 := s.GeoBlockSourceURLv4
	if urlV4 == "" {
		urlV4 = defaultGeoBlockSourceURLv4
	}
	urlV6 := s.GeoBlockSourceURLv6
	if urlV6 == "" {
		urlV6 = defaultGeoBlockSourceURLv6
	}

	var errs []string
	v4, err := fetchCIDRList(urlV4)
	if err != nil {
		errs = append(errs, fmt.Sprintf("IPv4 (%s): %v", urlV4, err))
	}
	var v6 []string
	if urlV6 != "" {
		v6, err = fetchCIDRList(urlV6)
		if err != nil {
			errs = append(errs, fmt.Sprintf("IPv6 (%s): %v", urlV6, err))
		}
	}

	geoBlockMu.Lock()
	if len(v4) > 0 || urlV4 == "" {
		geoBlockCached.SourceV4 = v4
	}
	if len(v6) > 0 || urlV6 == "" {
		geoBlockCached.SourceV6 = v6
	}
	if len(errs) > 0 {
		geoBlockCached.LastError = strings.Join(errs, "; ")
		geoBlockCached.LastErrorAt = time.Now()
	} else {
		geoBlockCached.LastError = ""
		geoBlockCached.LastErrorAt = time.Time{}
		geoBlockCached.FetchedAt = time.Now()
	}
	cacheCopy := geoBlockCached
	geoBlockMu.Unlock()

	if data, err := json.MarshalIndent(cacheCopy, "", "  "); err == nil {
		_ = os.WriteFile(geoBlockFile, data, 0644)
	}
	recompileGeoBlockMatcher()

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// fetchCIDRList downloads url and returns its non-blank, non-comment lines.
// Invalid individual lines are tolerated (skipped at compile time, not
// here) since an upstream source occasionally has stray formatting this
// feature doesn't need to be strict about.
func fetchCIDRList(url string) ([]string, error) {
	client := &http.Client{Timeout: geoBlockFetchTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var lines []string
	sc := bufio.NewScanner(io.LimitReader(resp.Body, geoBlockMaxBodyBytes))
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("empty response")
	}
	return lines, nil
}

// TriggerGeoBlockUpdateNow synchronously re-fetches the configured source(s)
// — used by the admin "Cập nhật ngay" button, independent of the scheduled
// interval.
func TriggerGeoBlockUpdateNow() error {
	return fetchAndStoreGeoBlockLists()
}

// validateGeoBlockManualCIDRs rejects a save outright if any manual entry
// doesn't parse — better to force the admin to fix a typo than to silently
// drop it (which could mean "the IP I meant to allow is still blocked").
func validateGeoBlockManualCIDRs(cidrs []string) error {
	for _, s := range cidrs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, err := parseCIDROrIP(s); err != nil {
			return fmt.Errorf("dải IP không hợp lệ: %q", s)
		}
	}
	return nil
}

// geoBlockOnSettingsSaved recompiles the matcher and re-arms the scheduler
// after any /api/settings save — called unconditionally (cheap) since
// GeoBlock fields could have changed along with anything else in the same
// save.
func geoBlockOnSettingsSaved() {
	recompileGeoBlockMatcher()
	rearmGeoBlockScheduler()
}

// GeoBlockStatusOut is the shape returned by GET /api/geoblock/status.
type GeoBlockStatusOut struct {
	Enabled       bool      `json:"enabled"`
	AutoUpdate    bool      `json:"auto_update"`
	IntervalHours int       `json:"interval_hours"`
	FetchedAt     time.Time `json:"fetched_at,omitempty"`
	V4Count       int       `json:"v4_count"`
	V6Count       int       `json:"v6_count"`
	ManualCount   int       `json:"manual_count"`
	CompiledCount int       `json:"compiled_count"` // merged ranges actually loaded in the matcher — 0 means "would fail open right now"
	LastError     string    `json:"last_error,omitempty"`
	LastErrorAt   time.Time `json:"last_error_at,omitempty"`
}

// GeoBlockStatus reports the feature's current state for the admin UI.
func GeoBlockStatus() GeoBlockStatusOut {
	s := GetSettings()
	geoBlockMu.RLock()
	defer geoBlockMu.RUnlock()
	out := GeoBlockStatusOut{
		Enabled:       s.GeoBlockEnabled,
		AutoUpdate:    s.GeoBlockAutoUpdate,
		IntervalHours: int(geoBlockUpdateInterval(s).Hours()),
		FetchedAt:     geoBlockCached.FetchedAt,
		V4Count:       len(geoBlockCached.SourceV4),
		V6Count:       len(geoBlockCached.SourceV6),
		ManualCount:   len(s.GeoBlockManualCIDRs),
		LastError:     geoBlockCached.LastError,
		LastErrorAt:   geoBlockCached.LastErrorAt,
	}
	if geoBlockMatch != nil {
		out.CompiledCount = len(geoBlockMatch.v4) + len(geoBlockMatch.v6)
	}
	return out
}

// ── Blocked response ─────────────────────────────────────────────────────

// respondGeoBlocked writes the fixed "not available in your region"
// response — plain text for API callers (www/app.js's apiFetch reads a
// non-OK response body directly as the error message, no JSON needed) and
// a small standalone HTML page for an actual browser navigation, same
// convention isPublicPath already uses to tell the two apart (everything
// under /api/ vs everything else).
func respondGeoBlocked(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusForbidden)
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(geoBlockMessageVI))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(geoBlockedPageHTML))
}

const geoBlockedPageHTML = `<!DOCTYPE html>
<html lang="vi">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>UTMC PLATFORM</title>
<style>
  body { margin:0; min-height:100vh; display:flex; align-items:center; justify-content:center;
    background:#050b16; color:#fff; font-family:'Segoe UI',Arial,sans-serif; text-align:center; padding:1.5rem; }
  .card { max-width:480px; padding:2.5rem 2rem; border-radius:22px; background:rgba(10,19,35,.82);
    border:1px solid rgba(0,240,255,.35); box-shadow:0 0 40px rgba(0,240,255,.08); }
  .icon { font-size:2.6rem; margin-bottom:1rem; }
  h1 { font-size:1.15rem; margin:0 0 .6rem; color:#fff; }
  p { color:#94a3b8; font-size:.92rem; line-height:1.6; margin:0; }
</style>
</head>
<body>
  <div class="card">
    <div class="icon">🌐</div>
    <h1>UTMC Platform</h1>
    <p>Hiện tại, Platform chưa khả dụng ở khu vực của bạn.</p>
  </div>
</body>
</html>`
