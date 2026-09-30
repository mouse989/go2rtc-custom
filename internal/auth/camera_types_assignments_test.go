package auth

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestGetCameraTypeAssignmentsPrunesOrphanedStreams(t *testing.T) {
	if err := initCameraTypes(filepath.Join(t.TempDir(), "camera_types.json")); err != nil {
		t.Fatalf("initCameraTypes: %v", err)
	}
	ctStore.data.Assignments = map[string]string{
		"cam-still-here": "bosch",
		"cam-removed-1":  "bosch", // no longer in go2rtc.yaml
		"cam-removed-2":  "axis",  // no longer in go2rtc.yaml
		"cam-also-here":  "axis",
	}

	withMockStreams(t, map[string][]string{
		"cam-still-here": {"rtsp://u:p@10.0.0.1/x"},
		"cam-also-here":  {"rtsp://u:p@10.0.0.2/x"},
	})

	got := getCameraTypeAssignments()
	want := map[string]string{
		"cam-still-here": "bosch",
		"cam-also-here":  "axis",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected orphaned entries pruned, got %v, want %v", got, want)
	}
}

func TestGetCameraTypeAssignmentsNoStreamProviderReturnsUnfiltered(t *testing.T) {
	if err := initCameraTypes(filepath.Join(t.TempDir(), "camera_types.json")); err != nil {
		t.Fatalf("initCameraTypes: %v", err)
	}
	ctStore.data.Assignments = map[string]string{"cam-a": "bosch"}

	savedNames := getStreamNames
	getStreamNames = nil
	t.Cleanup(func() { getStreamNames = savedNames })

	got := getCameraTypeAssignments()
	want := map[string]string{"cam-a": "bosch"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected unfiltered map when no stream provider is wired, got %v", got)
	}
}
