package counting

// LineCrossCounter detects when tracked vehicles cross a virtual line.
// For horizontal lines (axis="h"): counts when a track crosses linePos*frameHeight on the Y axis.
// For vertical lines   (axis="v"): counts when a track crosses linePos*frameWidth  on the X axis.
type LineCrossCounter struct {
	LinePos  float64 // 0.0-1.0
	LineAxis string  // "h" or "v"
	Total    int
}

// Process checks all active tracks for line crossings and returns the number of new crossings.
func (c *LineCrossCounter) Process(tracks []*Track, frameW, frameH int) int {
	n := 0
	for _, tr := range tracks {
		if tr.Crossed || tr.Missed > 0 {
			continue
		}
		crossed := false
		if c.LineAxis == "v" {
			line := c.LinePos * float64(frameW)
			crossed = (tr.PrevX < line && tr.X >= line) ||
				(tr.PrevX > line && tr.X <= line)
		} else {
			// default: horizontal
			line := c.LinePos * float64(frameH)
			crossed = (tr.PrevY < line && tr.Y >= line) ||
				(tr.PrevY > line && tr.Y <= line)
		}
		if crossed {
			tr.Crossed = true
			c.Total++
			n++
		}
	}
	return n
}
