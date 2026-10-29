package traveltime

import (
	"path/filepath"
	"testing"
)

func resetRoutesState(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	initRoutes(filepath.Join(dir, "travel_routes.json"))
	initRegions(filepath.Join(dir, "travel_route_regions.json"))
}

func TestCreateRouteKeepsRegionAndRoadType(t *testing.T) {
	resetRoutesState(t)
	if err := SetRegions([]string{"Quận 1"}); err != nil {
		t.Fatalf("SetRegions: %v", err)
	}

	created, err := createRoute(&Route{Name: "R1", Origin: "10,106", Destination: "11,107", Region: "Quận 1", RoadType: "Quốc lộ"})
	if err != nil {
		t.Fatalf("createRoute: %v", err)
	}
	if created.Region != "Quận 1" || created.RoadType != "Quốc lộ" {
		t.Fatalf("expected Region/RoadType preserved, got %+v", created)
	}

	list := ListRoutes()
	if len(list) != 1 || list[0].Region != "Quận 1" || list[0].RoadType != "Quốc lộ" {
		t.Fatalf("ListRoutes didn't reflect the created route's tags: %+v", list)
	}
}

func TestIsValidRegionAcceptsEmptyAndConfiguredOnly(t *testing.T) {
	resetRoutesState(t)
	if err := SetRegions([]string{"Quận 1", "TP. Thủ Đức"}); err != nil {
		t.Fatalf("SetRegions: %v", err)
	}

	if !IsValidRegion("") {
		t.Error("expected empty Region (chưa phân loại) to be valid")
	}
	if !IsValidRegion("Quận 1") {
		t.Error("expected a configured region to be valid")
	}
	if IsValidRegion("Quận 99") {
		t.Error("expected an unconfigured region to be invalid")
	}
}

func TestIsValidRoadTypeAcceptsEmptyAndFixedListOnly(t *testing.T) {
	resetRoutesState(t)
	if !IsValidRoadType("") {
		t.Error("expected empty RoadType (chưa phân loại) to be valid")
	}
	for _, rt := range RoadTypes {
		if !IsValidRoadType(rt) {
			t.Errorf("expected fixed road type %q to be valid", rt)
		}
	}
	if IsValidRoadType("Đường làng") {
		t.Error("expected an unlisted road type to be invalid")
	}
}

func TestSetRegionsTrimsDedupesAndDropsEmpty(t *testing.T) {
	resetRoutesState(t)
	if err := SetRegions([]string{" Quận 1 ", "Quận 1", "", "  ", "Quận 3"}); err != nil {
		t.Fatalf("SetRegions: %v", err)
	}
	got := ListRegions()
	want := []string{"Quận 1", "Quận 3"}
	if len(got) != len(want) {
		t.Fatalf("ListRegions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListRegions = %v, want %v", got, want)
		}
	}
}

func TestValidateRouteTagsRejectsUnknownRegionOrRoadType(t *testing.T) {
	resetRoutesState(t)
	if err := SetRegions([]string{"Quận 1"}); err != nil {
		t.Fatalf("SetRegions: %v", err)
	}

	if err := validateRouteTags(&Route{Region: "Quận 1", RoadType: "Quốc lộ"}); err != nil {
		t.Errorf("expected valid tags to pass, got %v", err)
	}
	if err := validateRouteTags(&Route{}); err != nil {
		t.Errorf("expected empty (chưa phân loại) tags to pass, got %v", err)
	}
	if err := validateRouteTags(&Route{Region: "Quận 99"}); err == nil {
		t.Error("expected an unconfigured region to be rejected")
	}
	if err := validateRouteTags(&Route{RoadType: "Đường làng"}); err == nil {
		t.Error("expected an unlisted road type to be rejected")
	}
}

func TestSetRegionsPersistsAcrossInitRegions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "travel_route_regions.json")
	initRegions(path)
	if err := SetRegions([]string{"Quận 1"}); err != nil {
		t.Fatalf("SetRegions: %v", err)
	}

	// Simulate a restart: re-init from the same file.
	initRegions(path)
	got := ListRegions()
	if len(got) != 1 || got[0] != "Quận 1" {
		t.Fatalf("expected region list to persist across restart, got %v", got)
	}
}
