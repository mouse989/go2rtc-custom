package counting

// CrossResult holds per-direction crossing counts from one frame.
type CrossResult struct {
	Down  int // top → bottom (horizontal line)
	Up    int // bottom → top (horizontal line)
	Right int // left → right (vertical line)
	Left  int // right → left (vertical line)
	Total int
}

// LineCrossCounter detects directional vehicle crossings over one or two virtual lines.
// LineHPos controls a horizontal line (detects CountDown / CountUp).
// LineVPos controls a vertical line (detects CountRight / CountLeft).
// A position of 0 means that line is disabled.
type LineCrossCounter struct {
	LineHPos   float64
	LineVPos   float64
	CountDown  bool
	CountUp    bool
	CountRight bool
	CountLeft  bool
}

// Process checks all active tracks for line crossings and returns per-direction counts.
func (c *LineCrossCounter) Process(tracks []*Track, frameW, frameH int) CrossResult {
	var res CrossResult
	for _, tr := range tracks {
		if tr.Missed > 0 {
			continue
		}
		if c.LineHPos > 0 && (c.CountDown || c.CountUp) && !tr.CrossedH {
			line := c.LineHPos * float64(frameH)
			if c.CountDown && tr.PrevY < line && tr.Y >= line {
				tr.CrossedH = true
				res.Down++
				res.Total++
			} else if c.CountUp && tr.PrevY > line && tr.Y <= line {
				tr.CrossedH = true
				res.Up++
				res.Total++
			}
		}
		if c.LineVPos > 0 && (c.CountRight || c.CountLeft) && !tr.CrossedV {
			line := c.LineVPos * float64(frameW)
			if c.CountRight && tr.PrevX < line && tr.X >= line {
				tr.CrossedV = true
				res.Right++
				res.Total++
			} else if c.CountLeft && tr.PrevX > line && tr.X <= line {
				tr.CrossedV = true
				res.Left++
				res.Total++
			}
		}
	}
	return res
}
