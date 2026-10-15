package gis

import (
	"testing"

	"github.com/AlexxIT/go2rtc/internal/auth"
)

func TestUserCanSeeLayer(t *testing.T) {
	admin := &auth.User{Role: auth.RoleAdmin}
	unrestricted := &auth.User{Role: auth.RoleViewer, AllowMapGIS: true}
	restricted := &auth.User{Role: auth.RoleViewer, AllowMapGIS: true, GisLayerIDs: []string{"layer_a", "layer_b"}}

	if !userCanSeeLayer(admin, "layer_a") {
		t.Error("admin must see every layer, regardless of GisLayerIDs")
	}
	if !userCanSeeLayer(unrestricted, "layer_zzz") {
		t.Error("a viewer with no GisLayerIDs set must see every layer (default/original behavior)")
	}
	if !userCanSeeLayer(restricted, "layer_a") {
		t.Error("a restricted viewer must see a layer explicitly listed in GisLayerIDs")
	}
	if userCanSeeLayer(restricted, "layer_c") {
		t.Error("a restricted viewer must NOT see a layer not listed in GisLayerIDs")
	}
}
