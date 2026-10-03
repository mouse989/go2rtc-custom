package auth

import "testing"

// TestPointInRegionBuffer verifies that pointInRegion treats BufferPolygons
// as an additional, OR'd membership area alongside Polygons — the behavior
// the buffer-zone feature (cross-team camera visibility at a region's edge)
// depends on. The buffer ring here is a larger square fully enclosing the
// base square, mirroring what map.html's Turf-computed buffer looks like.
func TestPointInRegionBuffer(t *testing.T) {
	base := Ring{{10, 10}, {20, 10}, {20, 20}, {10, 20}}
	buffer := Ring{{5, 5}, {25, 5}, {25, 25}, {5, 25}}

	region := &Region{
		ID:       "r1",
		Polygons: []Ring{base},
	}

	// No buffer configured yet (BufferKm=0, BufferPolygons empty): a point
	// outside the base polygon but inside where the buffer would be must
	// stay excluded.
	if pointInRegion(7, 7, region) {
		t.Fatal("expected point outside base polygon to be excluded when no buffer is configured")
	}
	if !pointInRegion(15, 15, region) {
		t.Fatal("expected point inside base polygon to be included")
	}

	region.BufferKm = 1
	region.BufferPolygons = []Ring{buffer}

	// Point inside the base polygon: still included (base alone suffices).
	if !pointInRegion(15, 15, region) {
		t.Fatal("expected point inside base polygon to remain included once a buffer is set")
	}
	// Point inside the buffer ring but outside the base polygon: included
	// only because of BufferPolygons.
	if !pointInRegion(7, 7, region) {
		t.Fatal("expected point inside buffer-only area to be included once BufferPolygons is set")
	}
	// Point outside both: excluded.
	if pointInRegion(30, 30, region) {
		t.Fatal("expected point outside both base and buffer to be excluded")
	}
}
