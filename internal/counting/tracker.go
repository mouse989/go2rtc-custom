package counting

import "math"

const (
	maxMissedFrames = 5   // remove track after missing this many frames
	maxMatchDist    = 60  // max centroid distance (pixels) to match detection
)

// Track represents a vehicle being tracked across frames.
type Track struct {
	ID       int
	X, Y     float64
	PrevX    float64
	PrevY    float64
	Missed   int  // consecutive frames without a matching detection
	CrossedH bool // counted crossing the horizontal line
	CrossedV bool // counted crossing the vertical line
}

// Tracker maintains vehicle identities across frames using nearest-centroid matching.
type Tracker struct {
	tracks []*Track
	nextID int
}

func newTracker() *Tracker {
	return &Tracker{nextID: 1}
}

// Update matches current-frame blobs to existing tracks and returns the active track list.
func (t *Tracker) Update(blobs []Blob) []*Track {
	matched := make([]bool, len(t.tracks))
	usedBlob := make([]bool, len(blobs))

	// Greedy nearest-neighbour matching
	for i, tr := range t.tracks {
		bestDist := math.MaxFloat64
		bestJ := -1
		for j, b := range blobs {
			if usedBlob[j] {
				continue
			}
			d := dist(tr.X, tr.Y, b.CX, b.CY)
			if d < bestDist {
				bestDist = d
				bestJ = j
			}
		}
		if bestJ >= 0 && bestDist <= maxMatchDist {
			tr.PrevX, tr.PrevY = tr.X, tr.Y
			tr.X, tr.Y = blobs[bestJ].CX, blobs[bestJ].CY
			tr.Missed = 0
			matched[i] = true
			usedBlob[bestJ] = true
		}
	}

	// Age unmatched tracks
	alive := t.tracks[:0]
	for i, tr := range t.tracks {
		if !matched[i] {
			tr.Missed++
		}
		if tr.Missed <= maxMissedFrames {
			alive = append(alive, tr)
		}
	}
	t.tracks = alive

	// Create new tracks for unmatched blobs
	for j, b := range blobs {
		if !usedBlob[j] {
			t.tracks = append(t.tracks, &Track{
				ID:    t.nextID,
				X:     b.CX,
				Y:     b.CY,
				PrevX: b.CX,
				PrevY: b.CY,
			})
			t.nextID++
		}
	}

	return t.tracks
}

func dist(x1, y1, x2, y2 float64) float64 {
	dx := x1 - x2
	dy := y1 - y2
	return math.Sqrt(dx*dx + dy*dy)
}
