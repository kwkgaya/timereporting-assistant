// Command genicon generates the app icon (PNG + ICO) for timereporting-assistant
// and writes them to internal/trayapp/assets/ and internal/web/assets/.
//
// Run from the repo root:
//
//	go run ./cmd/genicon
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

func main() {
	sizes := []int{32, 16}
	var pngs [][]byte
	for _, sz := range sizes {
		pngs = append(pngs, renderIcon(sz))
	}

	// Write 32px PNG to tray assets.
	must(writeFile("internal/trayapp/assets/icon.png", pngs[0]))
	// Write 32px PNG as favicon for the web server.
	must(writeFile("internal/web/assets/favicon.png", pngs[0]))
	// Write multi-size ICO (32 + 16).
	must(writeFile("internal/trayapp/assets/icon.ico", buildICO(pngs)))

	println("icon.png and icon.ico written")
}

// renderIcon draws the timereporting-assistant icon at the given square size.
//
// Design: circular badge, blue gradient background, white clock face.
func renderIcon(size int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	cx, cy := float64(size)/2, float64(size)/2
	r := float64(size)/2 - 0.5

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx := float64(x) + 0.5 - cx
			dy := float64(y) + 0.5 - cy
			dist := math.Sqrt(dx*dx + dy*dy)
			if dist > r {
				img.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 0}) // transparent outside circle
				continue
			}
			// Blue gradient: lerp from #2684FF (top) to #0052CC (bottom).
			t := float64(y) / float64(size)
			bg := lerpColor(
				color.NRGBA{0x26, 0x84, 0xFF, 0xFF},
				color.NRGBA{0x00, 0x52, 0xCC, 0xFF},
				t,
			)
			img.SetNRGBA(x, y, bg)
		}
	}

	// Draw white clock elements scaled to icon size.
	// Clock outer circle ring.
	clockR := r * 0.62
	drawCircleRing(img, cx, cy, clockR, 1.3, color.NRGBA{255, 255, 255, 255})

	// Hour hand: points to ~10 o'clock.
	hourLen := clockR * 0.52
	hourAngle := -math.Pi/2 - math.Pi/3 // 10 o'clock
	drawLine(img, cx, cy,
		cx+hourLen*math.Cos(hourAngle),
		cy+hourLen*math.Sin(hourAngle),
		1.4, color.NRGBA{255, 255, 255, 255})

	// Minute hand: points to ~12 o'clock (straight up).
	minLen := clockR * 0.72
	minAngle := -math.Pi / 2 // 12 o'clock
	drawLine(img, cx, cy,
		cx+minLen*math.Cos(minAngle),
		cy+minLen*math.Sin(minAngle),
		1.4, color.NRGBA{255, 255, 255, 255})

	// Center dot.
	dotR := clockR * 0.14
	fillCircle(img, cx, cy, dotR, color.NRGBA{255, 255, 255, 255})

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// ── Drawing helpers ────────────────────────────────────────────────────────

func lerpColor(a, b color.NRGBA, t float64) color.NRGBA {
	lerp := func(x, y uint8, f float64) uint8 {
		return uint8(float64(x)*(1-f) + float64(y)*f)
	}
	return color.NRGBA{lerp(a.R, b.R, t), lerp(a.G, b.G, t), lerp(a.B, b.B, t), 255}
}

// drawCircleRing draws an anti-aliased ring of given radius and stroke width.
func drawCircleRing(img *image.NRGBA, cx, cy, r, stroke float64, c color.NRGBA) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dx := float64(x) + 0.5 - cx
			dy := float64(y) + 0.5 - cy
			d := math.Abs(math.Sqrt(dx*dx+dy*dy) - r)
			if d <= stroke {
				alpha := float64(c.A) * math.Max(0, 1-d/stroke*0.6)
				blend(img, x, y, color.NRGBA{c.R, c.G, c.B, uint8(alpha)})
			}
		}
	}
}

func drawLine(img *image.NRGBA, x0, y0, x1, y1, width float64, c color.NRGBA) {
	b := img.Bounds()
	dx, dy := x1-x0, y1-y0
	length := math.Sqrt(dx*dx + dy*dy)
	if length < 0.001 {
		return
	}
	// Unit perpendicular.
	nx, ny := -dy/length, dx/length
	steps := int(length * 4)
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		px, py := x0+t*dx, y0+t*dy
		for ix := b.Min.X; ix < b.Max.X; ix++ {
			for iy := b.Min.Y; iy < b.Max.Y; iy++ {
				fx, fy := float64(ix)+0.5-px, float64(iy)+0.5-py
				cross := math.Abs(fx*nx + fy*ny)
				if cross <= width*0.9 {
					alpha := float64(c.A) * math.Max(0, 1-cross/(width*0.9)*0.5)
					blend(img, ix, iy, color.NRGBA{c.R, c.G, c.B, uint8(alpha)})
				}
			}
		}
	}
}

func fillCircle(img *image.NRGBA, cx, cy, r float64, c color.NRGBA) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dx := float64(x) + 0.5 - cx
			dy := float64(y) + 0.5 - cy
			d := math.Sqrt(dx*dx + dy*dy)
			if d <= r {
				blend(img, x, y, c)
			}
		}
	}
}

func blend(img *image.NRGBA, x, y int, c color.NRGBA) {
	if !image.Pt(x, y).In(img.Bounds()) || img.NRGBAAt(x, y).A == 0 {
		return
	}
	src := img.NRGBAAt(x, y)
	fa := float64(c.A) / 255
	img.SetNRGBA(x, y, color.NRGBA{
		uint8(float64(c.R)*fa + float64(src.R)*(1-fa)),
		uint8(float64(c.G)*fa + float64(src.G)*(1-fa)),
		uint8(float64(c.B)*fa + float64(src.B)*(1-fa)),
		src.A,
	})
}

// ── ICO encoder ───────────────────────────────────────────────────────────
// Windows ICO with PNG images inside (Vista+ format).

func buildICO(pngs [][]byte) []byte {
	var buf bytes.Buffer
	n := len(pngs)
	// ICONDIR header: reserved(2) type(2) count(2)
	binary.Write(&buf, binary.LittleEndian, uint16(0))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint16(n))
	// Each ICONDIRENTRY is 16 bytes; image data starts after header + all entries.
	offset := 6 + n*16
	for _, p := range pngs {
		img, _, _ := image.DecodeConfig(bytes.NewReader(p))
		w, h := img.Width, img.Height
		if w >= 256 {
			w = 0
		}
		if h >= 256 {
			h = 0
		}
		buf.WriteByte(byte(w))
		buf.WriteByte(byte(h))
		buf.WriteByte(0) // color count
		buf.WriteByte(0) // reserved
		binary.Write(&buf, binary.LittleEndian, uint16(1)) // planes
		binary.Write(&buf, binary.LittleEndian, uint16(32)) // bit count
		binary.Write(&buf, binary.LittleEndian, uint32(len(p)))
		binary.Write(&buf, binary.LittleEndian, uint32(offset))
		offset += len(p)
	}
	for _, p := range pngs {
		buf.Write(p)
	}
	return buf.Bytes()
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
