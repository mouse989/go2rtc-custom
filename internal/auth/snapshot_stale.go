package auth

// snapshot_stale.go — the "camera disconnected" placeholder shown by
// proxyFrameHandler in place of an on-disk snapshot that's older than
// snapshotStaleThreshold(). Without this, a camera that drops offline keeps
// silently serving its last-known-good frame from disk forever — a stale
// image and a live one look identical to a viewer, so nothing signals that
// the camera actually needs attention.
//
// The warning icon (assets/warning.png) is the ⚠️ Unicode emoji, rendered
// once offline via a local headless-Chromium screenshot of Noto Color Emoji
// (the font already present on this build host) and committed as a static
// asset — no network fetch happens at build or run time. It's embedded with
// go:embed, decoded and alpha-composited onto the canvas once at process
// startup (deterministic, no per-request cost) using golang.org/x/image/draw
// (already a transitive dependency here) for the scale step. The caption
// text is kept to plain ASCII because basicfont's bitmap glyphs — used for
// the caption only, not the icon — don't cover Vietnamese diacritics.

import (
	"bytes"
	_ "embed"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

//go:embed assets/warning.png
var warningIconPNG []byte

// disconnectedPlaceholderJPEG is generated once at startup (deterministic,
// no per-request cost) and served as-is.
var disconnectedPlaceholderJPEG = buildDisconnectedPlaceholder()

func buildDisconnectedPlaceholder() []byte {
	const w, h = 320, 180

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bg := color.RGBA{R: 30, G: 18, B: 18, A: 255} // dark red-brown, distinct from the neutral gray "not fetched yet" placeholder
	draw.Draw(img, img.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)

	drawWarningIcon(img, w/2, 8, 64)

	drawCenteredText(img, "CAMERA OFFLINE", 130, color.RGBA{230, 230, 230, 255})
	drawCenteredText(img, "no recent signal", 148, color.RGBA{190, 150, 110, 255})

	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 70})
	return buf.Bytes()
}

// drawWarningIcon decodes the embedded ⚠️ emoji PNG, scales it to `size`
// pixels tall (preserving aspect ratio), and alpha-composites it onto img
// centered horizontally at cx with its top edge at topY.
func drawWarningIcon(img *image.RGBA, cx, topY, size int) {
	icon, err := png.Decode(bytes.NewReader(warningIconPNG))
	if err != nil {
		return // can't happen for the embedded asset; skip the icon rather than panic
	}

	srcB := icon.Bounds()
	destW := size * srcB.Dx() / srcB.Dy()
	scaled := image.NewRGBA(image.Rect(0, 0, destW, size))
	xdraw.CatmullRom.Scale(scaled, scaled.Bounds(), icon, srcB, xdraw.Src, nil)

	x0 := cx - destW/2
	dstRect := image.Rect(x0, topY, x0+destW, topY+size)
	draw.Draw(img, dstRect, scaled, image.Point{}, draw.Over)
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
