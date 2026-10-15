package auth

import "testing"

// TestUserCanAccessPathMapRoute is a regression test: map.html's "🛣 Theo
// tuyến" tool is gated client-side by canMapRoute() (AllowMapRoute), but
// userCanAccessPath had no matching server-side allow for
// /api/vietmap/route — every viewer request 403'd regardless of the flag,
// since the path sat in neither viewerDefaultPaths nor
// tabExtraPaths[TabMap] (no such key existed). Admin never hit this,
// because the role check short-circuits above it.
func TestUserCanAccessPathMapRoute(t *testing.T) {
	gated := &User{Role: RoleViewer, AllowMapRoute: true}
	if !userCanAccessPath(gated, "/api/vietmap/route?point=10.1,106.1&point=10.2,106.2") {
		t.Fatal("a viewer with AllowMapRoute should reach /api/vietmap/route")
	}
	ungated := &User{Role: RoleViewer, AllowMapRoute: false}
	if userCanAccessPath(ungated, "/api/vietmap/route?point=10.1,106.1&point=10.2,106.2") {
		t.Fatal("a viewer without AllowMapRoute must not reach /api/vietmap/route")
	}
}

// TestUserCanAccessPathMapTrafficFlow mirrors
// TestUserCanAccessPathMapRoute above — added at the same time as
// AllowMapTrafficFlow itself specifically so this permission never ships
// with the "frontend gate exists, server-side allow forgotten" bug that
// AllowMapRoute shipped with.
func TestUserCanAccessPathMapTrafficFlow(t *testing.T) {
	gated := &User{Role: RoleViewer, AllowMapTrafficFlow: true}
	if !userCanAccessPath(gated, "/api/trafficflow/latest") {
		t.Fatal("a viewer with AllowMapTrafficFlow should reach /api/trafficflow/latest")
	}
	ungated := &User{Role: RoleViewer, AllowMapTrafficFlow: false}
	if userCanAccessPath(ungated, "/api/trafficflow/latest") {
		t.Fatal("a viewer without AllowMapTrafficFlow must not reach /api/trafficflow/latest")
	}
}

// TestUserCanAccessPathDashboardRoute: the Dashboard's travel-time route
// detail map draws the real road-following path via /api/vietmap/route —
// same endpoint as map.html's "🛣 Theo tuyến" tool, but a Dashboard viewer
// needs it regardless of whether they also have the separate AllowMapRoute
// flag (see tabExtraPaths[TabDashboard]'s own comment).
func TestUserCanAccessPathDashboardRoute(t *testing.T) {
	dashViewer := &User{Role: RoleViewer, Tabs: []string{TabDashboard}, AllowMapRoute: false}
	if !userCanAccessPath(dashViewer, "/api/vietmap/route?point=10.1,106.1&point=10.2,106.2") {
		t.Fatal("a viewer with the Dashboard tab should reach /api/vietmap/route even without AllowMapRoute")
	}
	noDash := &User{Role: RoleViewer, Tabs: []string{TabMap}, AllowMapRoute: false}
	if userCanAccessPath(noDash, "/api/vietmap/route?point=10.1,106.1&point=10.2,106.2") {
		t.Fatal("a viewer without the Dashboard tab and without AllowMapRoute must not reach /api/vietmap/route")
	}
}
