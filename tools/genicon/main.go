// Command genicon draws the placeholder app icon (a monitor on a blue tile)
// as a 256x256 PNG. Usage: go run ./tools/genicon OUT.png
package main

import (
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: genicon OUT.png")
	}
	const n = 256
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	bg := color.NRGBA{0x1f, 0x6f, 0xd1, 0xff}
	fg := color.NRGBA{0xff, 0xff, 0xff, 0xff}
	screen := color.NRGBA{0x9c, 0xd2, 0xff, 0xff}

	inRounded := func(x, y, x0, y0, x1, y1, r int) bool {
		if x < x0 || x >= x1 || y < y0 || y >= y1 {
			return false
		}
		cx, cy := x, y
		switch {
		case x < x0+r:
			cx = x0 + r
		case x >= x1-r:
			cx = x1 - r - 1
		}
		switch {
		case y < y0+r:
			cy = y0 + r
		case y >= y1-r:
			cy = y1 - r - 1
		}
		dx, dy := x-cx, y-cy
		return dx*dx+dy*dy <= r*r
	}

	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			switch {
			case inRounded(x, y, 56, 64, 200, 160, 10): // monitor bezel
				img.SetNRGBA(x, y, fg)
				if inRounded(x, y, 66, 74, 190, 150, 4) {
					img.SetNRGBA(x, y, screen)
				}
			case x >= 116 && x < 140 && y >= 160 && y < 184: // stand
				img.SetNRGBA(x, y, fg)
			case inRounded(x, y, 88, 184, 168, 196, 6): // foot
				img.SetNRGBA(x, y, fg)
			case inRounded(x, y, 8, 8, 248, 248, 48): // tile
				img.SetNRGBA(x, y, bg)
			}
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
