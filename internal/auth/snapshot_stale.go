package auth

// snapshot_stale.go — the "camera disconnected" placeholder shown by
// proxyFrameHandler in place of an on-disk snapshot that's older than
// snapshotStaleThreshold(). Without this, a camera that drops offline keeps
// silently serving its last-known-good frame from disk forever — a stale
// image and a live one look identical to a viewer, so nothing signals that
// the camera actually needs attention.
//
// Drawn entirely with the stdlib image package plus golang.org/x/image's
// tiny bitmap font (already a transitive dependency here) rather than
// embedding an asset file: no image editing tooling was available to
// produce one, and a warning triangle + "!" is simple enough to rasterize
// directly. The "!" mark is drawn as plain filled rectangles rather than
// text so it renders identically regardless of font/locale support; the
// caption text is kept to plain ASCII for the same reason — basicfont's
// bitmap glyphs don't cover Vietnamese diacritics.

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// disconnectedPlaceholderJPEG is generated once at startup (deterministic,
// no per-request cost) and served as-is.
var disconnectedPlaceholderJPEG = buildDisconnectedPlaceholder()

func buildDisconnectedPlaceholder() []byte {
	const w, h = 320, 180

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bg := color.RGBA{R: 30, G: 18, B: 18, A: 255} // dark red-brown, distinct from the neutral gray "not fetched yet" placeholder
	draw.Draw(img, img.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)

	amber := color.RGBA{R: 245, G: 166, B: 35, A: 255}
	amberEdge := color.RGBA{R: 120, G: 80, B: 15, A: 255}
	drawWarningTriangle(img, w/2, 30, 60, bg, amber, amberEdge)

	drawCenteredText(img, "CAMERA OFFLINE", 130, color.RGBA{230, 230, 230, 255})
	drawCenteredText(img, "no recent signal", 148, color.RGBA{190, 150, 110, 255})

	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 70})
	return buf.Bytes()
}

// drawWarningTriangle fills an isoceles triangle (apex at (cx, topY), base
// 2*size wide at y = topY+size) with fill/edge colors, then cuts an "!" mark
// out of it in cutColor (the background color, so it reads as a hole).
func drawWarningTriangle(img *image.RGBA, cx, topY, size int, cutColor, fill, edge color.RGBA) {
	baseY := topY + size
	for y := topY; y <= baseY; y++ {
		frac := float64(y-topY) / float64(size)
		half := int(frac * float64(size) * 0.95)
		edgeRow := y == baseY
		for x := cx - half; x <= cx+half; x++ {
			c := fill
			if edgeRow || x == cx-half || x == cx+half {
				c = edge
			}
			img.SetRGBA(x, y, c)
		}
	}

	barTop := topY + size/4
	barBottom := topY + size - size/4
	for y := barTop; y <= barBottom-6; y++ {
		for x := cx - 3; x <= cx+3; x++ {
			img.SetRGBA(x, y, cutColor)
		}
	}
	for y := barBottom - 4; y <= barBottom; y++ {
		for x := cx - 3; x <= cx+3; x++ {
			img.SetRGBA(x, y, cutColor)
		}
	}
}

// drawCenteredText horizontally centers s (ASCII only — see file comment)
// at baseline y using golang.org/x/image's built-in bitmap font.
func drawCenteredText(img *image.RGBA, s string, y int, c color.RGBA) {
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(c),
		Face: basicfont.Face7x13,
	}
	x := (img.Bounds().Dx() - d.MeasureString(s).Round()) / 2
	d.Dot = fixed.Point26_6{X: fixed.I(x), Y: fixed.I(y)}
	d.DrawString(s)
}
