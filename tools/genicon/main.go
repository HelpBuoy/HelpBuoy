// Command genicon draws the app icon (a red and white lifebuoy) as a square
// PNG with a transparent background, 256 pixels unless SIZE is given. The
// shapes are drawn at the requested size, not scaled down, and every pixel is
// supersampled, so the small sizes stay sharp.
//
// Usage: go run ./tools/genicon OUT.png [SIZE]
package main

import (
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"strconv"
)

const (
	units = 256 // shape coordinates are in 1/256 of the icon size
	ss    = 8   // subsamples per pixel along each axis

	center  = 128.0
	outerR  = 124.0 // outer edge of the ring
	innerR  = 62.0  // edge of the hole
	edge    = 6.0   // width of the darker rim on both edges
	band    = 24.0  // half-angle of each red band, in degrees
	ropeW   = 6.5   // half-width of the rope
	ropeSag = 32.0  // how far the rope hangs out from the ring between bands
)

var (
	red      = color.NRGBA{0xe8, 0x11, 0x23, 0xff} // Windows red
	redRim   = color.NRGBA{0xa8, 0x0a, 0x16, 0xff}
	white    = color.NRGBA{0xff, 0xff, 0xff, 0xff}
	whiteRim = color.NRGBA{0xa9, 0xb4, 0xc0, 0xff}
	rope     = color.NRGBA{0xf2, 0xc1, 0x4e, 0xff} // gold, as the otp4win key
	ropeRim  = color.NRGBA{0xb5, 0x86, 0x1c, 0xff}
)

func main() {
	if len(os.Args) != 2 && len(os.Args) != 3 {
		log.Fatal("usage: genicon OUT.png [SIZE]")
	}
	size := units
	if len(os.Args) == 3 {
		n, err := strconv.Atoi(os.Args[2])
		if err != nil || n < 1 {
			log.Fatalf("bad size %q", os.Args[2])
		}
		size = n
	}
	scale := float64(units) / float64(size)
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/ss) * scale
					y := (float64(py) + (float64(sy)+0.5)/ss) * scale
					c, ok := shade(x, y)
					if !ok {
						continue
					}
					r += float64(c.R)
					g += float64(c.G)
					b += float64(c.B)
					a++
				}
			}
			if a == 0 {
				continue
			}
			img.SetNRGBA(px, py, color.NRGBA{
				R: uint8(r/a + 0.5),
				G: uint8(g/a + 0.5),
				B: uint8(b/a + 0.5),
				A: uint8(a/(ss*ss)*255 + 0.5),
			})
		}
	}

	f, err := os.Create(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
}

// shade returns the colour at (x, y), or false where the icon is transparent.
// The ring is white with four red bands at the top, bottom and sides. A gold rope runs
// around the outside, held under each red band and looping out into the corners.
func shade(x, y float64) (color.NRGBA, bool) {
	dx, dy := x-center, y-center
	d := math.Hypot(dx, dy)

	// Angle from the nearest band centre (on an axis), 0..90 degrees.
	phi := math.Mod(math.Atan2(dy, dx)*180/math.Pi+360, 90)

	if d <= outerR && d >= innerR {
		rim := d > outerR-edge || d < innerR+edge
		if math.Abs(phi-45) >= 45-band {
			if rim {
				return redRim, true
			}
			return red, true
		}
		if rim {
			return whiteRim, true
		}
		return white, true
	}

	// The rope runs under each red band and swings out between them, up to
	// ropeSag beyond the ring halfway along each white part.
	ropeR := outerR - ropeW
	if t := (phi - band) / (90 - 2*band); t > 0 && t < 1 {
		ropeR += (ropeSag + ropeW) * math.Sin(math.Pi*t)
	}
	if r := math.Abs(d - ropeR); r <= ropeW && d > outerR {
		if r > ropeW-2.5 {
			return ropeRim, true
		}
		return rope, true
	}
	return color.NRGBA{}, false
}
