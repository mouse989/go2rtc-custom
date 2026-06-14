package counting

import (
	"image"
	"image/color"
)

// bgModel implements a running-average background subtraction model.
// Background is updated as: bg = (1-alpha)*bg + alpha*frame
// Foreground mask: |frame - bg| > threshold
type bgModel struct {
	bg        []float32 // grayscale background values [0-255]
	width     int
	height    int
	alpha     float32 // learning rate
	threshold float32
	ready     bool // true after first frame
}

func newBGModel(alpha, threshold float32) *bgModel {
	return &bgModel{alpha: alpha, threshold: threshold}
}

// apply processes a grayscale frame and returns the foreground binary mask.
// Returns nil if the model is not yet initialised.
func (m *bgModel) apply(gray []byte, w, h int) []byte {
	if !m.ready || m.width != w || m.height != h {
		m.bg = make([]float32, w*h)
		for i, v := range gray {
			m.bg[i] = float32(v)
		}
		m.width = w
		m.height = h
		m.ready = true
		return nil // first frame: initialise only
	}

	mask := make([]byte, w*h)
	for i, v := range gray {
		pix := float32(v)
		diff := pix - m.bg[i]
		if diff < 0 {
			diff = -diff
		}
		if diff > m.threshold {
			mask[i] = 255
		}
		// Update background
		m.bg[i] = m.bg[i]*(1-m.alpha) + pix*m.alpha
	}
	return mask
}

// toGrayscale converts an RGBA/NRGBA image to a flat byte slice (one byte per pixel).
func toGrayscale(img image.Image) ([]byte, int, int) {
	b := img.Bounds()
	w := b.Max.X - b.Min.X
	h := b.Max.Y - b.Min.Y
	gray := make([]byte, w*h)

	switch src := img.(type) {
	case *image.Gray:
		copy(gray, src.Pix)
	case *image.YCbCr:
		// Fast path for JPEG-decoded images (YCbCr)
		for y := 0; y < h; y++ {
			srcRow := src.Y[src.YOffset(b.Min.X, b.Min.Y+y):]
			dstRow := gray[y*w:]
			copy(dstRow[:w], srcRow[:w])
		}
	default:
		// Slow path: convert pixel by pixel
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				c := color.GrayModel.Convert(img.At(x, y)).(color.Gray)
				gray[(y-b.Min.Y)*w+(x-b.Min.X)] = c.Y
			}
		}
	}
	return gray, w, h
}

// erode3x3 applies a 3x3 erosion to remove noise from a binary mask.
func erode3x3(mask []byte, w, h int) []byte {
	out := make([]byte, w*h)
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			idx := y*w + x
			if mask[idx] == 0 {
				continue
			}
			// Only set if all 8 neighbours are foreground
			if mask[idx-w-1] == 255 && mask[idx-w] == 255 && mask[idx-w+1] == 255 &&
				mask[idx-1] == 255 && mask[idx+1] == 255 &&
				mask[idx+w-1] == 255 && mask[idx+w] == 255 && mask[idx+w+1] == 255 {
				out[idx] = 255
			}
		}
	}
	return out
}

// dilate3x3 applies a 3x3 dilation to fill gaps in blobs.
func dilate3x3(mask []byte, w, h int) []byte {
	out := make([]byte, w*h)
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			idx := y*w + x
			if mask[idx] == 255 ||
				mask[idx-w-1] == 255 || mask[idx-w] == 255 || mask[idx-w+1] == 255 ||
				mask[idx-1] == 255 || mask[idx+1] == 255 ||
				mask[idx+w-1] == 255 || mask[idx+w] == 255 || mask[idx+w+1] == 255 {
				out[idx] = 255
			}
		}
	}
	return out
}

// morphOpen applies erosion then dilation to remove small noise blobs.
func morphOpen(mask []byte, w, h int) []byte {
	return dilate3x3(erode3x3(mask, w, h), w, h)
}
