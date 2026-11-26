package traffic

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// handleWardBoundaries requires any authenticated user (TabTtiSuggest
// itself is enforced earlier, in middleware — see
// heatmap_replay_api_test.go's own identical note on why that branch
// isn't reachable from this package's tests);
// handleWardBoundariesImport requires the admin role specifically
// (requireTrafficAccess, same gate persistent_export.go's admin-only
// endpoints use) — an unauthenticated request fails that check too, just
// with a different status code.
func TestHandleWardBoundariesRequiresAuth(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/tti-suggest/wards", nil)
	rec := httptest.NewRecorder()
	handleWardBoundaries(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for request with no authenticated user, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleWardBoundariesImportRequiresAdmin(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/tti-suggest/wards/import", strings.NewReader(`[]`))
	rec := httptest.NewRecorder()
	handleWardBoundariesImport(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for request with no authenticated admin, got %d: %s", rec.Code, rec.Body.String())
	}
}

func resetWardBoundaries(t *testing.T) {
	t.Helper()
	resetStorageState(t) // gives dataDir() a fresh temp directory
	initWardBoundaries(dataDir())
}

func TestWardSlugStripsDiacriticsAndNormalizes(t *testing.T) {
	cases := map[string]string{
		"An Đông":           "an-dong",
		"Phường 7":          "phuong-7",
		"Thủ Đức":           "thu-duc",
		"  Nhà Bè  ":        "nha-be",
		"Bà Rịa - Vũng Tàu": "ba-ria-vung-tau",
	}
	for in, want := range cases {
		if got := wardSlug(in); got != want {
			t.Errorf("wardSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReplaceWardBoundariesAssignsStableIDsAndPersists(t *testing.T) {
	resetWardBoundaries(t)

	in := []*WardBoundary{
		{Name: "An Đông", Type: "Phường", Rings: []Ring{{{10, 106}, {10, 107}, {11, 107}}}},
		{Name: "An Đông", Type: "Phường", Rings: []Ring{{{20, 106}, {20, 107}, {21, 107}}}}, // duplicate name -> falls back to positional ID
	}
	if err := replaceWardBoundaries(in); err != nil {
		t.Fatalf("replaceWardBoundaries: %v", err)
	}

	list := listWardBoundaries()
	if len(list) != 2 {
		t.Fatalf("expected 2 wards, got %d", len(list))
	}
	if list[0].ID != "an-dong" {
		t.Errorf("first ward ID = %q, want %q", list[0].ID, "an-dong")
	}
	if list[1].ID == "an-dong" || list[1].ID == "" {
		t.Errorf("second ward (duplicate name) ID = %q, want a distinct fallback ID", list[1].ID)
	}

	// Persisted to disk — re-init from the same dir must reload it.
	initWardBoundaries(dataDir())
	reloaded := listWardBoundaries()
	if len(reloaded) != 2 {
		t.Fatalf("expected 2 wards after reload, got %d", len(reloaded))
	}
}

func TestPointInWardsNoSelectionPassesEverything(t *testing.T) {
	if !pointInWards(0, 0, nil) {
		t.Error("nil selectedWards must pass every point (no boundary filter)")
	}
}

func TestPointInWardsFiltersByRing(t *testing.T) {
	square := &WardBoundary{ID: "sq", Name: "Square", Rings: []Ring{{
		{10.0, 106.0}, {10.0, 106.1}, {10.1, 106.1}, {10.1, 106.0},
	}}}
	selected := []*WardBoundary{square}

	if !pointInWards(10.05, 106.05, selected) {
		t.Error("expected point inside the square to pass")
	}
	if pointInWards(10.20, 106.20, selected) {
		t.Error("expected point outside the square to be excluded")
	}
}

func TestComputeHotspotsFiltersByWardBoundary(t *testing.T) {
	resetFlowCaptureDB(t)
	resetWardBoundaries(t)
	ensureTravelTimeInit(t)

	inWard := &WardBoundary{Name: "Trong Ranh Gioi", Rings: []Ring{{
		{10.0, 106.0}, {10.0, 106.1}, {10.1, 106.1}, {10.1, 106.0},
	}}}
	otherWard := &WardBoundary{Name: "Phuong Khac", Rings: []Ring{{
		{20.0, 107.0}, {20.0, 107.1}, {20.1, 107.1}, {20.1, 107.0},
	}}}
	if err := replaceWardBoundaries([]*WardBoundary{inWard, otherWard}); err != nil {
		t.Fatalf("replaceWardBoundaries: %v", err)
	}

	base := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	// "Đường Trong": inside inWard's square.
	for i := 0; i < 4; i++ {
		seedFlowScan(t, "Quận 1", "Đường Trong", base.Add(time.Duration(i)*5*time.Minute),
			2.0, 8.0, 8.5, 10.05, 106.05, 10.06, 106.06)
	}
	// "Đường Ngoai": outside both wards entirely.
	for i := 0; i < 4; i++ {
		seedFlowScan(t, "Quận 1", "Đường Ngoai", base.Add(time.Duration(i)*5*time.Minute),
			2.0, 8.0, 8.5, 50.0, 50.0, 50.01, 50.01)
	}

	from, to := base.Add(-time.Hour), base.Add(time.Hour)

	// No ward filter: both roads appear.
	all, err := computeHotspots(from, to, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("computeHotspots (no filter): %v", err)
	}
	if len(all.Hotspots) != 2 {
		t.Fatalf("expected 2 hotspots with no ward filter, got %d: %+v", len(all.Hotspots), all.Hotspots)
	}

	// Filtered to inWard only: only Đường Trong should appear.
	filtered, err := computeHotspots(from, to, 0, 0, 0, 0, []string{inWard.ID})
	if err != nil {
		t.Fatalf("computeHotspots (ward filter): %v", err)
	}
	if len(filtered.Hotspots) != 1 || filtered.Hotspots[0].Description != "Đường Trong" {
		t.Errorf("expected only Đường Trong with the ward filter applied, got %+v", filtered.Hotspots)
	}

	// Filtered to otherWard only: neither road is inside it.
	none, err := computeHotspots(from, to, 0, 0, 0, 0, []string{otherWard.ID})
	if err != nil {
		t.Fatalf("computeHotspots (otherWard filter): %v", err)
	}
	if len(none.Hotspots) != 0 {
		t.Errorf("expected 0 hotspots filtered to a ward neither road is in, got %+v", none.Hotspots)
	}
}
