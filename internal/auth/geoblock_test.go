package auth

import (
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupGeoBlockTest(t *testing.T) {
	t.Helper()
	if err := initSettings(filepath.Join(t.TempDir(), "settings.json")); err != nil {
		t.Fatalf("initSettings: %v", err)
	}
	geoBlockMu.Lock()
	geoBlockCached = geoBlockCache{}
	geoBlockMatch = nil
	geoBlockMu.Unlock()
	geoBlockTimerMu.Lock()
	if geoBlockTimer != nil {
		geoBlockTimer.Stop()
		geoBlockTimer = nil
	}
	geoBlockTimerMu.Unlock()
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("ParseAddr(%q): %v", s, err)
	}
	return a
}

func TestGeoBlockDisabledAllowsEverything(t *testing.T) {
	setupGeoBlockTest(t)
	// GeoBlockEnabled defaults to false — even an IP matching nothing
	// should be allowed.
	if !geoBlockAllows("8.8.8.8") {
		t.Error("expected allow when the feature is disabled")
	}
}

func TestGeoBlockEmptyMatcherFailsOpen(t *testing.T) {
	setupGeoBlockTest(t)
	s := GetSettings()
	s.GeoBlockEnabled = true
	if err := UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	// Enabled, but nothing has ever been fetched/configured — must fail
	// open (allow), never lock out every visitor because the list is empty.
	if !geoBlockAllows("8.8.8.8") {
		t.Error("expected fail-open (allow) when the matcher has nothing loaded")
	}
}

func TestGeoBlockBlocksNonMatchingPublicIP(t *testing.T) {
	setupGeoBlockTest(t)
	s := GetSettings()
	s.GeoBlockEnabled = true
	s.GeoBlockManualCIDRs = []string{"14.160.0.0/12"} // a real VNPT allocation, used here only as a test fixture
	if err := UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	geoBlockOnSettingsSaved()

	if geoBlockAllows("8.8.8.8") {
		t.Error("expected a public IP outside the configured range to be blocked")
	}
	if !geoBlockAllows("14.160.1.1") {
		t.Error("expected an IP inside the configured range to be allowed")
	}
}

func TestGeoBlockExemptsPrivateAndLoopbackIPs(t *testing.T) {
	setupGeoBlockTest(t)
	s := GetSettings()
	s.GeoBlockEnabled = true
	s.GeoBlockManualCIDRs = []string{"14.160.0.0/12"}
	if err := UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	geoBlockOnSettingsSaved()

	for _, ip := range []string{"127.0.0.1", "::1", "192.168.1.5", "10.0.0.1", "172.16.5.5", "100.64.0.1", "169.254.1.1", "fc00::1"} {
		if !geoBlockAllows(ip) {
			t.Errorf("expected %s to be exempt (private/loopback/link-local/CGNAT), got blocked", ip)
		}
	}
}

func TestGeoBlockUnparseableIPFailsOpen(t *testing.T) {
	setupGeoBlockTest(t)
	s := GetSettings()
	s.GeoBlockEnabled = true
	s.GeoBlockManualCIDRs = []string{"14.160.0.0/12"}
	if err := UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	geoBlockOnSettingsSaved()

	if !geoBlockAllows("not-an-ip") {
		t.Error("expected fail-open (allow) when the client IP can't be parsed")
	}
}

func TestCompileGeoBlockMatcherMergesOverlappingRanges(t *testing.T) {
	// Two overlapping/adjacent /24s should merge into one contiguous range
	// so contains() (binary search over disjoint intervals) stays correct.
	m := compileGeoBlockMatcher([]string{"1.2.3.0/24", "1.2.4.0/24"}, nil, nil)
	if len(m.v4) != 1 {
		t.Fatalf("expected the two adjacent /24s to merge into 1 range, got %d: %+v", len(m.v4), m.v4)
	}
	if !m.contains(mustAddr(t, "1.2.3.1")) || !m.contains(mustAddr(t, "1.2.4.254")) {
		t.Error("expected both original ranges to still be covered after merging")
	}
	if m.contains(mustAddr(t, "1.2.5.1")) {
		t.Error("did not expect an address just past the merged range to match")
	}
}

func TestCompileGeoBlockMatcherToleratesBadLines(t *testing.T) {
	m := compileGeoBlockMatcher([]string{"", "# comment", "not-a-cidr", "14.160.0.0/12"}, nil, nil)
	if len(m.v4) != 1 {
		t.Fatalf("expected only the one valid line to compile, got %d ranges", len(m.v4))
	}
}

func TestCompileGeoBlockMatcherHandlesIPv6(t *testing.T) {
	m := compileGeoBlockMatcher(nil, []string{"2402:800::/32"}, nil)
	if !m.contains(mustAddr(t, "2402:800::1")) {
		t.Error("expected an address inside the IPv6 prefix to match")
	}
	if m.contains(mustAddr(t, "2001:4860:4860::8888")) {
		t.Error("did not expect Google DNS's IPv6 address to match a Vietnam-only prefix")
	}
}

func TestParseCIDROrIPAcceptsBareIP(t *testing.T) {
	p, err := parseCIDROrIP("1.2.3.4")
	if err != nil {
		t.Fatalf("parseCIDROrIP: %v", err)
	}
	if p.Bits() != 32 {
		t.Errorf("expected a bare IPv4 to become a /32, got /%d", p.Bits())
	}
	p6, err := parseCIDROrIP("2001:db8::1")
	if err != nil {
		t.Fatalf("parseCIDROrIP(v6): %v", err)
	}
	if p6.Bits() != 128 {
		t.Errorf("expected a bare IPv6 to become a /128, got /%d", p6.Bits())
	}
}

func TestValidateGeoBlockManualCIDRsRejectsGarbage(t *testing.T) {
	if err := validateGeoBlockManualCIDRs([]string{"14.160.0.0/12", "", "   "}); err != nil {
		t.Errorf("expected valid CIDRs (plus blank lines) to pass, got %v", err)
	}
	if err := validateGeoBlockManualCIDRs([]string{"not-a-real-range"}); err == nil {
		t.Error("expected an invalid entry to be rejected")
	}
}

// A failed re-fetch must keep whatever was previously cached — a transient
// upstream outage must never turn into "block all of Vietnam".
func TestFetchAndStoreGeoBlockListsKeepsPreviousListOnFailure(t *testing.T) {
	setupGeoBlockTest(t)
	geoBlockFile = filepath.Join(t.TempDir(), "geoblock_cache.json")
	geoBlockMu.Lock()
	geoBlockCached = geoBlockCache{SourceV4: []string{"14.160.0.0/12"}, FetchedAt: time.Now().Add(-time.Hour)}
	geoBlockMu.Unlock()
	recompileGeoBlockMatcher()

	s := GetSettings()
	s.GeoBlockEnabled = true
	s.GeoBlockSourceURLv4 = "http://127.0.0.1:1/unreachable"
	s.GeoBlockSourceURLv6 = "" // skip IPv6 so only the v4 fetch is exercised
	if err := UpdateSettings(s); err != nil {
		t.Fatal(err)
	}

	if err := fetchAndStoreGeoBlockLists(); err == nil {
		t.Fatal("expected the unreachable source to return an error")
	}

	geoBlockMu.RLock()
	kept := geoBlockCached.SourceV4
	lastErr := geoBlockCached.LastError
	geoBlockMu.RUnlock()
	if len(kept) != 1 || kept[0] != "14.160.0.0/12" {
		t.Errorf("expected the previous list to survive a failed fetch, got %v", kept)
	}
	if lastErr == "" {
		t.Error("expected LastError to be recorded")
	}
	// And the matcher built from the still-good cached list must still work.
	if !geoBlockAllows("14.160.1.1") {
		t.Error("expected the previously cached range to still allow a matching IP after a failed re-fetch")
	}
}

func TestGeoBlockStatusReportsCounts(t *testing.T) {
	setupGeoBlockTest(t)
	s := GetSettings()
	s.GeoBlockEnabled = true
	s.GeoBlockManualCIDRs = []string{"14.160.0.0/12", "1.2.3.4"}
	if err := UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	geoBlockOnSettingsSaved()

	status := GeoBlockStatus()
	if !status.Enabled {
		t.Error("expected Enabled=true")
	}
	if status.ManualCount != 2 {
		t.Errorf("expected ManualCount=2, got %d", status.ManualCount)
	}
	if status.CompiledCount == 0 {
		t.Error("expected a non-zero compiled range count")
	}
}

// Sanity check that the blocked response actually carries the exact
// required Vietnamese message, for both the API (plain text) and page
// (HTML) response shapes.
func TestRespondGeoBlockedCarriesExactMessage(t *testing.T) {
	wAPI := httptest.NewRecorder()
	rAPI := httptest.NewRequest("GET", "/api/incidents/active", nil)
	respondGeoBlocked(wAPI, rAPI)
	if wAPI.Code != 403 {
		t.Errorf("expected 403, got %d", wAPI.Code)
	}
	if wAPI.Body.String() != geoBlockMessageVI {
		t.Errorf("expected exact message in API response body, got %q", wAPI.Body.String())
	}

	wPage := httptest.NewRecorder()
	rPage := httptest.NewRequest("GET", "/map.html", nil)
	respondGeoBlocked(wPage, rPage)
	if wPage.Code != 403 {
		t.Errorf("expected 403, got %d", wPage.Code)
	}
	if !strings.Contains(wPage.Body.String(), geoBlockMessageVI) {
		t.Error("expected the HTML page to contain the exact message")
	}
}
