package counting

// Blob represents a detected foreground region in a frame.
type Blob struct {
	CX, CY float64 // centroid
	Area   int     // number of foreground pixels
}

// findBlobs detects connected foreground regions using BFS flood fill.
func findBlobs(mask []byte, w, h, minArea, maxArea int) []Blob {
	visited := make([]bool, len(mask))
	var blobs []Blob

	queue := make([]int, 0, 256) // reuse allocation

	for i, v := range mask {
		if v == 0 || visited[i] {
			continue
		}
		// BFS flood fill
		queue = queue[:0]
		queue = append(queue, i)
		visited[i] = true

		sumX, sumY, area := 0, 0, 0

		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]

			cy := cur / w
			cx := cur % w
			sumX += cx
			sumY += cy
			area++

			// 4-connected neighbours
			for _, nb := range [4]int{cur - w, cur + w, cur - 1, cur + 1} {
				if nb < 0 || nb >= len(mask) {
					continue
				}
				// Prevent wrap-around on left/right edges
				if (cur%w == 0 && nb == cur-1) || (cur%w == w-1 && nb == cur+1) {
					continue
				}
				if !visited[nb] && mask[nb] == 255 {
					visited[nb] = true
					queue = append(queue, nb)
				}
			}
		}

		if area >= minArea && area <= maxArea {
			blobs = append(blobs, Blob{
				CX:   float64(sumX) / float64(area),
				CY:   float64(sumY) / float64(area),
				Area: area,
			})
		}
	}
	return blobs
}
