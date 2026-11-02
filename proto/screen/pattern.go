package screen

import (
	"image"
	"math"
)

type rgba [4]byte

var (
	colWhite   = rgba{235, 235, 235, 255}
	colBlack   = rgba{16, 16, 16, 255}
	colPanel   = rgba{24, 24, 32, 255}
	colSegOn   = rgba{255, 64, 48, 255}
	colSegOff  = rgba{56, 20, 20, 255}
	colLabel   = rgba{255, 255, 255, 255}
	colBoxEdge = rgba{0, 0, 0, 255}
	bars       = [...]rgba{
		{192, 192, 192, 255}, // white
		{192, 192, 0, 255},   // yellow
		{0, 192, 192, 255},   // cyan
		{0, 192, 0, 255},     // green
		{192, 0, 192, 255},   // magenta
		{192, 0, 0, 255},     // red
		{0, 0, 192, 255},     // blue
		{16, 16, 16, 255},    // black
	}
)

// TestPattern renders the phase 2 test pattern for time t (seconds since the
// session started): color bars, a moving box, a clock-like counter, and the
// user@machine label, so the helper can see that frames flow and how fast.
//
// The whole image is redrawn with row copies, and only the counter digits and
// the box change from one call to the next, so Diff of two consecutive
// patterns yields few tiles. Lower-case letters in label are drawn as upper
// case; characters outside the built-in font are drawn as '?'.
func TestPattern(dst *image.RGBA, t float64, label string) {
	if dst == nil {
		return
	}
	b := dst.Rect
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return
	}
	if math.IsNaN(t) || t < 0 {
		t = 0
	}
	if t > 1e9 {
		t = 1e9
	}
	at := func(x, y, rw, rh int) image.Rectangle {
		return image.Rect(b.Min.X+x, b.Min.Y+y, b.Min.X+x+rw, b.Min.Y+y+rh)
	}

	// Color bars over the whole image.
	for i, c := range bars {
		x0 := i * w / len(bars)
		x1 := (i + 1) * w / len(bars)
		fill(dst, at(x0, 0, x1-x0, h), c)
	}

	// Label band at the top.
	scale := max(1, h/160)
	bandH := 7*scale + 4*scale
	fill(dst, at(0, 0, w, bandH), colPanel)
	drawText(dst, b.Min.X+2*scale, b.Min.Y+2*scale, scale, label, colLabel)

	// Counter panel in the middle: HH:MM:SS.cc in seven-segment digits.
	dh := max(14, h/6)
	dw := dh / 2
	th := max(2, dh/10)
	gap := max(2, dw/4)
	sep := th * 3
	const text = "00:00:00.00"
	panelW := 0
	for _, ch := range text {
		if ch == ':' || ch == '.' {
			panelW += sep
		} else {
			panelW += dw + gap
		}
	}
	panelW += gap // leading gap; every digit carries its trailing one
	panelH := dh + 2*gap
	px := (w - panelW) / 2
	py := bandH + (h-bandH)/2 - panelH
	fill(dst, at(px, py, panelW, panelH), colBlack)
	cs := int64(t * 100)
	digits := [...]int{
		int(cs / 360000 % 100 / 10), int(cs / 360000 % 10),
		int(cs / 6000 % 60 / 10), int(cs / 6000 % 10),
		int(cs / 100 % 60 / 10), int(cs / 100 % 10),
		int(cs % 100 / 10), int(cs % 10),
	}
	x := b.Min.X + px + gap
	y := b.Min.Y + py + gap
	d := 0
	for _, ch := range text {
		switch ch {
		case ':':
			fill(dst, image.Rect(x+th, y+dh/3-th/2, x+2*th, y+dh/3+th/2+1), colSegOn)
			fill(dst, image.Rect(x+th, y+2*dh/3-th/2, x+2*th, y+2*dh/3+th/2+1), colSegOn)
			x += sep
		case '.':
			fill(dst, image.Rect(x+th, y+dh-th, x+2*th, y+dh), colSegOn)
			x += sep
		default:
			drawDigit(dst, x, y, dw, dh, th, digits[d])
			d++
			x += dw + gap
		}
	}

	// A box bouncing left and right below the counter, crossing the screen
	// in about four seconds.
	box := max(8, h/8)
	span := w - box
	bx := 0
	if span > 0 {
		pos := int64(t*float64(w)/4) % int64(2*span)
		if pos > int64(span) {
			pos = int64(2*span) - pos
		}
		bx = int(pos)
	}
	by := py + panelH + max(4, (h-(py+panelH)-box)/2)
	edge := max(1, box/16)
	fill(dst, at(bx, by, box, box), colBoxEdge)
	fill(dst, at(bx+edge, by+edge, box-2*edge, box-2*edge), colWhite)
	fill(dst, at(bx+box/3, by+box/3, box-2*(box/3), box-2*(box/3)), colSegOn)
}

// fill paints r (clipped to dst) with c, using row doubling and row copies
// rather than per-pixel stores.
func fill(dst *image.RGBA, r image.Rectangle, c rgba) {
	r = r.Intersect(dst.Rect)
	if r.Empty() {
		return
	}
	i0 := dst.PixOffset(r.Min.X, r.Min.Y)
	rowLen := r.Dx() * 4
	row := dst.Pix[i0 : i0+rowLen]
	copy(row, c[:])
	for n := 4; n < rowLen; n *= 2 {
		copy(row[n:], row[:n])
	}
	for y := 1; y < r.Dy(); y++ {
		off := i0 + y*dst.Stride
		copy(dst.Pix[off:off+rowLen], row)
	}
}

// Seven-segment bits: a top, b top right, c bottom right, d bottom,
// e bottom left, f top left, g middle.
var segments = [10]uint8{0x3F, 0x06, 0x5B, 0x4F, 0x66, 0x6D, 0x7D, 0x07, 0x7F, 0x6F}

func drawDigit(dst *image.RGBA, x, y, dw, dh, th, v int) {
	half := dh / 2
	rects := [7]image.Rectangle{
		image.Rect(x, y, x+dw, y+th),                     // a
		image.Rect(x+dw-th, y, x+dw, y+half),             // b
		image.Rect(x+dw-th, y+half, x+dw, y+dh),          // c
		image.Rect(x, y+dh-th, x+dw, y+dh),               // d
		image.Rect(x, y+half, x+th, y+dh),                // e
		image.Rect(x, y, x+th, y+half),                   // f
		image.Rect(x, y+(dh-th)/2, x+dw, y+(dh-th)/2+th), // g
	}
	// Unlit segments first, so they never cover the corners of lit ones.
	for i, r := range rects {
		if segments[v]&(1<<i) == 0 {
			fill(dst, r, colSegOff)
		}
	}
	for i, r := range rects {
		if segments[v]&(1<<i) != 0 {
			fill(dst, r, colSegOn)
		}
	}
}

// drawText draws s in the 5x7 font, each font pixel scale x scale, with one
// font pixel of spacing between characters.
func drawText(dst *image.RGBA, x, y, scale int, s string, c rgba) {
	for _, ch := range s {
		if x >= dst.Rect.Max.X {
			return
		}
		g := glyph(ch)
		for row := 0; row < 7; row++ {
			bits := g[row]
			for col := 0; col < 5; col++ {
				if bits&(0x10>>col) != 0 {
					px, py := x+col*scale, y+row*scale
					fill(dst, image.Rect(px, py, px+scale, py+scale), c)
				}
			}
		}
		x += 6 * scale
	}
}

func glyph(ch rune) [7]uint8 {
	if ch >= 'a' && ch <= 'z' {
		ch -= 'a' - 'A'
	}
	if g, ok := font5x7[ch]; ok {
		return g
	}
	return font5x7['?']
}

// font5x7 holds one 5x7 bitmap per character; each row uses the low five
// bits, bit 4 being the leftmost pixel.
var font5x7 = map[rune][7]uint8{
	' ':  {},
	'0':  {0x0E, 0x11, 0x13, 0x15, 0x19, 0x11, 0x0E},
	'1':  {0x04, 0x0C, 0x04, 0x04, 0x04, 0x04, 0x0E},
	'2':  {0x0E, 0x11, 0x01, 0x02, 0x04, 0x08, 0x1F},
	'3':  {0x1F, 0x02, 0x04, 0x02, 0x01, 0x11, 0x0E},
	'4':  {0x02, 0x06, 0x0A, 0x12, 0x1F, 0x02, 0x02},
	'5':  {0x1F, 0x10, 0x1E, 0x01, 0x01, 0x11, 0x0E},
	'6':  {0x06, 0x08, 0x10, 0x1E, 0x11, 0x11, 0x0E},
	'7':  {0x1F, 0x01, 0x02, 0x04, 0x08, 0x08, 0x08},
	'8':  {0x0E, 0x11, 0x11, 0x0E, 0x11, 0x11, 0x0E},
	'9':  {0x0E, 0x11, 0x11, 0x0F, 0x01, 0x02, 0x0C},
	'A':  {0x0E, 0x11, 0x11, 0x11, 0x1F, 0x11, 0x11},
	'B':  {0x1E, 0x11, 0x11, 0x1E, 0x11, 0x11, 0x1E},
	'C':  {0x0E, 0x11, 0x10, 0x10, 0x10, 0x11, 0x0E},
	'D':  {0x1C, 0x12, 0x11, 0x11, 0x11, 0x12, 0x1C},
	'E':  {0x1F, 0x10, 0x10, 0x1E, 0x10, 0x10, 0x1F},
	'F':  {0x1F, 0x10, 0x10, 0x1E, 0x10, 0x10, 0x10},
	'G':  {0x0E, 0x11, 0x10, 0x17, 0x11, 0x11, 0x0F},
	'H':  {0x11, 0x11, 0x11, 0x1F, 0x11, 0x11, 0x11},
	'I':  {0x0E, 0x04, 0x04, 0x04, 0x04, 0x04, 0x0E},
	'J':  {0x07, 0x02, 0x02, 0x02, 0x02, 0x12, 0x0C},
	'K':  {0x11, 0x12, 0x14, 0x18, 0x14, 0x12, 0x11},
	'L':  {0x10, 0x10, 0x10, 0x10, 0x10, 0x10, 0x1F},
	'M':  {0x11, 0x1B, 0x15, 0x15, 0x11, 0x11, 0x11},
	'N':  {0x11, 0x11, 0x19, 0x15, 0x13, 0x11, 0x11},
	'O':  {0x0E, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0E},
	'P':  {0x1E, 0x11, 0x11, 0x1E, 0x10, 0x10, 0x10},
	'Q':  {0x0E, 0x11, 0x11, 0x11, 0x15, 0x12, 0x0D},
	'R':  {0x1E, 0x11, 0x11, 0x1E, 0x14, 0x12, 0x11},
	'S':  {0x0F, 0x10, 0x10, 0x0E, 0x01, 0x01, 0x1E},
	'T':  {0x1F, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04},
	'U':  {0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0E},
	'V':  {0x11, 0x11, 0x11, 0x11, 0x11, 0x0A, 0x04},
	'W':  {0x11, 0x11, 0x11, 0x15, 0x15, 0x15, 0x0A},
	'X':  {0x11, 0x11, 0x0A, 0x04, 0x0A, 0x11, 0x11},
	'Y':  {0x11, 0x11, 0x11, 0x0A, 0x04, 0x04, 0x04},
	'Z':  {0x1F, 0x01, 0x02, 0x04, 0x08, 0x10, 0x1F},
	'.':  {0, 0, 0, 0, 0, 0x0C, 0x0C},
	',':  {0, 0, 0, 0, 0x0C, 0x04, 0x08},
	'-':  {0, 0, 0, 0x1F, 0, 0, 0},
	'_':  {0, 0, 0, 0, 0, 0, 0x1F},
	'@':  {0x0E, 0x11, 0x01, 0x0D, 0x15, 0x15, 0x0E},
	':':  {0, 0x0C, 0x0C, 0, 0x0C, 0x0C, 0},
	'/':  {0, 0x01, 0x02, 0x04, 0x08, 0x10, 0},
	'\\': {0, 0x10, 0x08, 0x04, 0x02, 0x01, 0},
	'(':  {0x02, 0x04, 0x08, 0x08, 0x08, 0x04, 0x02},
	')':  {0x08, 0x04, 0x02, 0x02, 0x02, 0x04, 0x08},
	'!':  {0x04, 0x04, 0x04, 0x04, 0, 0, 0x04},
	'?':  {0x0E, 0x11, 0x01, 0x02, 0x04, 0, 0x04},
	'\'': {0x0C, 0x04, 0x08, 0, 0, 0, 0},
	'+':  {0, 0x04, 0x04, 0x1F, 0x04, 0x04, 0},
	'=':  {0, 0, 0x1F, 0, 0x1F, 0, 0},
	'#':  {0x0A, 0x0A, 0x1F, 0x0A, 0x1F, 0x0A, 0x0A},
}
