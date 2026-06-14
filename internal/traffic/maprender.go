package traffic

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg" // register JPEG decoder for image.Decode
	"image/png"    // register PNG decoder + provides Encode
	"math"
	"net/http"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// ── Web Mercator projection ───────────────────────────────────────────────

// lngLatToWorld converts WGS-84 to world pixel coordinates at zoom level z.
func lngLatToWorld(lat, lng float64, z int) (float64, float64) {
	scale := float64(256) * math.Pow(2, float64(z))
	x := (lng + 180) / 360 * scale
	sinLat := math.Sin(lat * math.Pi / 180)
	sinLat = math.Max(-0.9999, math.Min(0.9999, sinLat))
	y := (0.5 - math.Log((1+sinLat)/(1-sinLat))/(4*math.Pi)) * scale
	return x, y
}

// computeZoomToFit finds the max zoom where the bbox fits in (w-pad*2)×(h-pad*2) pixels.
func computeZoomToFit(minLat, minLng, maxLat, maxLng float64, w, h, pad int) int {
	for z := 17; z >= 1; z-- {
		x0, y0 := lngLatToWorld(maxLat, minLng, z)
		x1, y1 := lngLatToWorld(minLat, maxLng, z)
		dx := x1 - x0
		dy := y1 - y0
		if dx > 0 && dy > 0 && int(dx) <= w-2*pad && int(dy) <= h-2*pad {
			return z
		}
	}
	return 1
}

// ── Color helpers ─────────────────────────────────────────────────────────

// parseHex parses a "#rrggbb" color string.
func parseHex(hex string) color.RGBA {
	if len(hex) < 7 {
		return color.RGBA{255, 107, 53, 255}
	}
	var r, g, b uint8
	fmt.Sscanf(hex[1:], "%02x%02x%02x", &r, &g, &b)
	return color.RGBA{r, g, b, 255}
}

// ── Tile fetching ─────────────────────────────────────────────────────────

// tileClient has no Timeout — we use per-request context deadlines instead.
var tileClient = &http.Client{}

// fetchTile downloads an OSM tile with a context deadline. Returns nil on error/timeout.
func fetchTile(ctx context.Context, z, tx, ty int) image.Image {
	maxT := 1 << uint(z)
	tx = ((tx % maxT) + maxT) % maxT
	if ty < 0 || ty >= maxT {
		return nil
	}
	subs := [3]string{"a", "b", "c"}
	sub := subs[(tx+ty)%3]
	url := fmt.Sprintf("https://%s.tile.openstreetmap.org/%d/%d/%d.png", sub, z, tx, ty)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "go2rtc-traffic-monitor/1.0 (traffic jam monitoring tool)")
	resp, err := tileClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return nil
	}
	return img
}

// ── Drawing primitives ────────────────────────────────────────────────────

// blendPixel composites c over the existing pixel at (x,y) using src-over.
func blendPixel(img *image.RGBA, x, y int, c color.RGBA) {
	b := img.Bounds()
	if x < b.Min.X || x >= b.Max.X || y < b.Min.Y || y >= b.Max.Y {
		return
	}
	if c.A == 255 {
		img.SetRGBA(x, y, c)
		return
	}
	a := float32(c.A) / 255
	d := img.RGBAAt(x, y)
	img.SetRGBA(x, y, color.RGBA{
		R: uint8(float32(c.R)*a + float32(d.R)*(1-a)),
		G: uint8(float32(c.G)*a + float32(d.G)*(1-a)),
		B: uint8(float32(c.B)*a + float32(d.B)*(1-a)),
		A: 255,
	})
}

// fillCircle draws a filled circle with radius r centered at (cx, cy).
// r is float64 to allow fractional sizes (e.g. 5.5) without anti-aliasing.
func fillCircle(img *image.RGBA, cx, cy int, r float64, c color.RGBA) {
	r2 := r * r
	ri := int(math.Ceil(r))
	for dy := -ri; dy <= ri; dy++ {
		for dx := -ri; dx <= ri; dx++ {
			if float64(dx*dx+dy*dy) <= r2 {
				blendPixel(img, cx+dx, cy+dy, c)
			}
		}
	}
}

// fillRect draws a filled axis-aligned rectangle.
func fillRect(img *image.RGBA, x, y, w, h int, c color.RGBA) {
	b := img.Bounds()
	for dy := 0; dy < h; dy++ {
		for dx := 0; dx < w; dx++ {
			px, py := x+dx, y+dy
			if px >= b.Min.X && px < b.Max.X && py >= b.Min.Y && py < b.Max.Y {
				blendPixel(img, px, py, c)
			}
		}
	}
}

// ── Text rendering (basicfont.Face7x13) ──────────────────────────────────

const fontAscent = 11 // pixels above baseline for basicfont.Face7x13

// drawStr draws s with its baseline at (x, y).
func drawStr(dst *image.RGBA, x, y int, s string, c color.Color) {
	d := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(c),
		Face: basicfont.Face7x13,
		Dot:  fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6(y * 64)},
	}
	d.DrawString(s)
}

// measureStr returns the pixel width of s in basicfont.Face7x13.
func measureStr(s string) int {
	d := &font.Drawer{Face: basicfont.Face7x13}
	return int(d.MeasureString(s) >> 6)
}

// drawStrCentered draws s centered at (cx, cy) (vertically centered around the text midline).
func drawStrCentered(dst *image.RGBA, cx, cy int, s string, c color.Color) {
	w := measureStr(s)
	// Baseline = cy + ascent/2 so text is vertically centered
	baseline := cy + fontAscent/2
	drawStr(dst, cx-w/2, baseline, s, c)
}

// ── Jam factor color scale ────────────────────────────────────────────────

// jamFactorColor returns a color matching the severity of the jam factor,
// mirroring the original project's flowColor() function.
func jamFactorColor(jf float64) color.RGBA {
	switch {
	case jf < 4:
		return color.RGBA{34, 197, 94, 255}   // #22c55e green  — free flow
	case jf < 7.5:
		return color.RGBA{250, 204, 21, 255}  // #facc15 yellow — moderate
	case jf < 9:
		return color.RGBA{249, 115, 22, 255}  // #f97316 orange — heavy
	default:
		return color.RGBA{239, 68, 68, 255}   // #ef4444 red    — severe
	}
}

// ── Main render function ──────────────────────────────────────────────────

// renderMapImage composes an OSM-tiled map with point markers and returns PNG bytes.
//
//	pts:      points to mark
//	regions:  used as fallback bounds when pts is empty
//	title:    label shown in top-left box (e.g. "Raw Data")
//	hexColor: marker fill color (e.g. "#60a5fa"); ignored when dotMode=true
//	dotMode:  true → small dots colored per jam-factor (for persistent image)
//	          false → large circles with JF label (for raw/filtered)
//	w, h:     output image size in pixels
//
// Tile fetching is bounded by a 40-second total deadline. Tiles that fail or
// timeout are skipped gracefully (the background colour shows instead).
func renderMapImage(pts []Point, regions []Region, title, hexColor string, dotMode bool, w, h int) ([]byte, error) {
	// Overall deadline for all tile HTTP requests
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	const tilePad = 60

	// ── 1. Compute bounds ──────────────────────────────────────────────
	var minLat, minLng, maxLat, maxLng float64

	if len(pts) == 0 {
		first := true
		for _, r := range regions {
			if !r.Enabled || len(r.Coords) == 0 {
				continue
			}
			for _, coord := range r.Coords {
				lat, lng := coord[0], coord[1]
				if first {
					minLat, maxLat, minLng, maxLng = lat, lat, lng, lng
					first = false
				} else {
					if lat < minLat {
						minLat = lat
					}
					if lat > maxLat {
						maxLat = lat
					}
					if lng < minLng {
						minLng = lng
					}
					if lng > maxLng {
						maxLng = lng
					}
				}
			}
		}
		if first {
			// Default: HCMC
			minLat, maxLat, minLng, maxLng = 10.60, 10.95, 106.55, 106.85
		}
	} else if len(pts) == 1 {
		d := 0.01
		minLat, maxLat = pts[0].Lat-d, pts[0].Lat+d
		minLng, maxLng = pts[0].Lng-d, pts[0].Lng+d
	} else {
		minLat, maxLat = pts[0].Lat, pts[0].Lat
		minLng, maxLng = pts[0].Lng, pts[0].Lng
		for _, p := range pts[1:] {
			if p.Lat < minLat {
				minLat = p.Lat
			}
			if p.Lat > maxLat {
				maxLat = p.Lat
			}
			if p.Lng < minLng {
				minLng = p.Lng
			}
			if p.Lng > maxLng {
				maxLng = p.Lng
			}
		}
		padLat := (maxLat - minLat) * 0.12
		padLng := (maxLng - minLng) * 0.12
		if padLat < 0.005 {
			padLat = 0.005
		}
		if padLng < 0.005 {
			padLng = 0.005
		}
		minLat -= padLat
		maxLat += padLat
		minLng -= padLng
		maxLng += padLng
	}

	// ── 2. Compute zoom ────────────────────────────────────────────────
	zoom := computeZoomToFit(minLat, minLng, maxLat, maxLng, w, h, tilePad)
	if zoom < 11 {
		zoom = 11
	}
	if zoom > 17 {
		zoom = 17
	}

	// ── 3. Canvas origin (world pixel of top-left corner) ──────────────
	centerLat := (minLat + maxLat) / 2
	centerLng := (minLng + maxLng) / 2
	cwx, cwy := lngLatToWorld(centerLat, centerLng, zoom)
	originX := cwx - float64(w)/2
	originY := cwy - float64(h)/2

	// ── 4. Create canvas ───────────────────────────────────────────────
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bg := color.RGBA{240, 242, 247, 255} // #f0f2f7
	draw.Draw(img, img.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)

	// ── 5. Fetch tiles in parallel ─────────────────────────────────────
	const TILE = 256
	minTX := int(math.Floor(originX / TILE))
	maxTX := int(math.Floor((originX + float64(w)) / TILE))
	minTY := int(math.Floor(originY / TILE))
	maxTY := int(math.Floor((originY + float64(h)) / TILE))

	type tileDraw struct {
		img   image.Image
		drawX int
		drawY int
	}

	tileCount := (maxTX - minTX + 1) * (maxTY - minTY + 1)
	addLog("info", fmt.Sprintf("fetching %d tiles at zoom %d for %q", tileCount, zoom, title))

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6) // max 6 concurrent tile requests
	fetched := make([]tileDraw, 0, tileCount)

	for tx := minTX; tx <= maxTX; tx++ {
		for ty := minTY; ty <= maxTY; ty++ {
			// Bail early if context already cancelled
			if ctx.Err() != nil {
				break
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(tx, ty int) {
				defer wg.Done()
				defer func() { <-sem }()
				tImg := fetchTile(ctx, zoom, tx, ty)
				if tImg != nil {
					dx := int(math.Round(float64(tx*TILE) - originX))
					dy := int(math.Round(float64(ty*TILE) - originY))
					mu.Lock()
					fetched = append(fetched, tileDraw{tImg, dx, dy})
					mu.Unlock()
				}
			}(tx, ty)
		}
	}
	wg.Wait()

	if ctx.Err() != nil {
		addLog("warn", fmt.Sprintf("tile fetch timeout for %q, proceeding with %d/%d tiles", title, len(fetched), tileCount))
	}

	// Draw tiles (sequential after all fetches)
	for _, t := range fetched {
		draw.Draw(img, image.Rect(t.drawX, t.drawY, t.drawX+TILE, t.drawY+TILE),
			t.img, image.Point{}, draw.Over)
	}

	// ── 6. Draw markers ────────────────────────────────────────────────
	markerColor := parseHex(hexColor)

	if dotMode {
		// Dot mode (persistent): small dots colored per jam-factor severity,
		// sorted low→high so severe dots render on top.
		sorted := make([]Point, len(pts))
		copy(sorted, pts)
		for i := 0; i < len(sorted)-1; i++ {
			for j := i + 1; j < len(sorted); j++ {
				if sorted[j].JamFactor < sorted[i].JamFactor {
					sorted[i], sorted[j] = sorted[j], sorted[i]
				}
			}
		}
		for _, p := range sorted {
			wx, wy := lngLatToWorld(p.Lat, p.Lng, zoom)
			cx := int(math.Round(wx - originX))
			cy := int(math.Round(wy - originY))

			dc := jamFactorColor(p.JamFactor)
			halo := color.RGBA{dc.R, dc.G, dc.B, 50}

			fillCircle(img, cx, cy, 5.5, halo)                            // outer halo
			fillCircle(img, cx, cy, 4.5, color.RGBA{255, 255, 255, 200}) // white border
			fillCircle(img, cx, cy, 3.5, dc)                             // solid fill
		}
	} else {
		// Standard marker mode (raw / filtered): large circle with JF label
		haloColor := color.RGBA{markerColor.R, markerColor.G, markerColor.B, 45}
		for _, p := range pts {
			wx, wy := lngLatToWorld(p.Lat, p.Lng, zoom)
			cx := int(math.Round(wx - originX))
			cy := int(math.Round(wy - originY))

			// Halo (semi-transparent)
			fillCircle(img, cx, cy, 18, haloColor)
			// White ring
			fillCircle(img, cx, cy, 14, color.RGBA{255, 255, 255, 255})
			// Colored fill
			fillCircle(img, cx, cy, 12, markerColor)
			// JF label
			label := fmt.Sprintf("%.1f", p.JamFactor)
			if p.JamFactor >= 10.0 {
				label = "10"
			}
			drawStrCentered(img, cx, cy, label, color.White)
		}
	}

	// ── 7. Title box (top-left) ────────────────────────────────────────
	now := time.Now().Format("15:04:05 02/01/2006")
	subtitle := fmt.Sprintf("%s - %d diem", now, len(pts))
	titlePW := measureStr(title)
	subtitlePW := measureStr(subtitle)
	boxW := titlePW
	if subtitlePW > boxW {
		boxW = subtitlePW
	}
	boxW += 28
	lineH := fontAscent + 5 // line height ≈ 16px
	boxH := lineH*2 + 10

	// White semi-transparent background
	fillRect(img, 12, 12, boxW, boxH, color.RGBA{255, 255, 255, 220})
	// Colored left accent bar
	fillRect(img, 12, 12, 4, boxH, markerColor)
	// Title (bold appearance via drawing twice with 1px offset)
	titleBaseY := 12 + 8 + fontAscent
	drawStr(img, 20, titleBaseY, title, color.RGBA{26, 34, 54, 255})
	drawStr(img, 21, titleBaseY, title, color.RGBA{26, 34, 54, 255}) // pseudo-bold
	// Subtitle
	subtitleBaseY := titleBaseY + lineH
	drawStr(img, 20, subtitleBaseY, subtitle, color.RGBA{74, 85, 120, 255})

	// ── 8. Dot-mode legend (bottom-left) ──────────────────────────────
	if dotMode {
		type legendItem struct {
			c   color.RGBA
			lbl string
		}
		legend := []legendItem{
			{color.RGBA{34, 197, 94, 255}, "< 4  free flow"},
			{color.RGBA{250, 204, 21, 255}, "4-7.5 moderate"},
			{color.RGBA{249, 115, 22, 255}, "7.5-9 heavy"},
			{color.RGBA{239, 68, 68, 255}, ">= 9  severe"},
		}
		rowH := fontAscent + 6
		legW := measureStr(">= 9  severe") + 22
		legH := len(legend)*rowH + 8
		legX, legY := 12, h-legH-12
		fillRect(img, legX, legY, legW, legH, color.RGBA{255, 255, 255, 210})
		for i, item := range legend {
			iy := legY + 4 + i*rowH
			fillCircle(img, legX+8, iy+fontAscent/2, 4, item.c)
			drawStr(img, legX+17, iy+fontAscent, item.lbl, color.RGBA{40, 40, 40, 255})
		}
	}

	// ── 9. Attribution (bottom-right) ─────────────────────────────────
	attrib := "© OpenStreetMap contributors"
	attribW := measureStr(attrib)
	fillRect(img, w-attribW-12, h-fontAscent-8, attribW+10, fontAscent+4,
		color.RGBA{255, 255, 255, 200})
	drawStr(img, w-attribW-7, h-6, attrib, color.RGBA{80, 80, 80, 255})

	// ── 10. Encode PNG ─────────────────────────────────────────────────
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
