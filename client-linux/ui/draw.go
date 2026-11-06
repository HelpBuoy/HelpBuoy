package ui

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Colors, close to the classic Windows look of the Windows client.
var (
	colBg         = color.RGBA{0xf0, 0xf0, 0xf0, 0xff}
	colText       = color.RGBA{0x00, 0x00, 0x00, 0xff}
	colGray       = color.RGBA{0x6d, 0x6d, 0x6d, 0xff}
	colDisabled   = color.RGBA{0xa0, 0xa0, 0xa0, 0xff}
	colError      = color.RGBA{170, 20, 20, 0xff}
	colWhite      = color.RGBA{0xff, 0xff, 0xff, 0xff}
	colBtn        = color.RGBA{0xe1, 0xe1, 0xe1, 0xff}
	colBtnHover   = color.RGBA{0xe5, 0xf1, 0xfb, 0xff}
	colBtnPressed = color.RGBA{0xcc, 0xe4, 0xf7, 0xff}
	colBorder     = color.RGBA{0xad, 0xad, 0xad, 0xff}
	colEditBorder = color.RGBA{0x7a, 0x7a, 0x7a, 0xff}
	colAccent     = color.RGBA{0x00, 0x78, 0xd7, 0xff}
	colSelection  = color.RGBA{0x99, 0xc9, 0xef, 0xff}
	colLink       = color.RGBA{0x00, 0x66, 0xcc, 0xff}
	colWarning    = color.RGBA{0xe8, 0xa3, 0x17, 0xff}
	colVerified   = color.RGBA{0x1e, 0x7b, 0x34, 0xff} // the verified server badge only
)

// checkMark draws a check mark inside r in col.
func checkMark(c *canvas, r image.Rectangle, col color.RGBA) {
	w := float64(r.Dx())
	pts := [][2]float64{{0.26, 0.52}, {0.43, 0.69}, {0.75, 0.33}}
	th := max(2, int(w*0.11))
	for i := 0; i+1 < len(pts); i++ {
		x0, y0 := pts[i][0]*w, pts[i][1]*w
		x1, y1 := pts[i+1][0]*w, pts[i+1][1]*w
		n := int(math.Hypot(x1-x0, y1-y0)) * 2
		for k := 0; k <= n; k++ {
			t := float64(k) / float64(n)
			x := r.Min.X + int(x0+(x1-x0)*t) - th/2
			y := r.Min.Y + int(y0+(y1-y0)*t) - th/2
			c.fill(image.Rect(x, y, x+th, y+th), col)
		}
	}
}

// fontKind selects one of the window's fonts.
type fontKind int

const (
	fontNormal fontKind = iota
	fontHeading
	fontBig
)

// fontPx are the font sizes in 96-DPI pixels.
var fontPx = [...]float64{13, 17, 20}

// fonts holds the faces at the window's scale.
type fonts struct {
	faces [3]font.Face
}

func newFonts(scale float64) (*fonts, error) {
	reg, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return nil, err
	}
	bold, err := opentype.Parse(gobold.TTF)
	if err != nil {
		return nil, err
	}
	f := &fonts{}
	for i, px := range fontPx {
		src := reg
		if fontKind(i) != fontNormal {
			src = bold
		}
		f.faces[i], err = opentype.NewFace(src, &opentype.FaceOptions{Size: px * scale, DPI: 72, Hinting: font.HintingFull})
		if err != nil {
			return nil, err
		}
	}
	return f, nil
}

func (f *fonts) face(k fontKind) font.Face { return f.faces[k] }

// lineHeight is the distance between baselines.
func (f *fonts) lineHeight(k fontKind) int {
	m := f.faces[k].Metrics()
	return (m.Height + fixed.I(1)).Ceil()
}

func (f *fonts) width(k fontKind, s string) int {
	return font.MeasureString(f.faces[k], s).Ceil()
}

// wrap breaks s into lines no wider than w pixels: at "\n", between words,
// and inside words that are wider than a line.
func (f *fonts) wrap(k fontKind, s string, w int) []string {
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		line := ""
		for _, word := range words {
			try := word
			if line != "" {
				try = line + " " + word
			}
			if f.width(k, try) <= w {
				line = try
				continue
			}
			if line != "" {
				lines = append(lines, line)
				line = ""
			}
			// Break a long word (a key line) by characters.
			for f.width(k, word) > w {
				n := 1
				rs := []rune(word)
				for n < len(rs) && f.width(k, string(rs[:n+1])) <= w {
					n++
				}
				lines = append(lines, string(rs[:n]))
				word = string(rs[n:])
			}
			line = word
		}
		lines = append(lines, line)
	}
	return lines
}

// canvas draws into the window's back buffer.
type canvas struct {
	img   *image.RGBA
	fonts *fonts
}

func (c *canvas) fill(r image.Rectangle, col color.Color) {
	draw.Draw(c.img, r, image.NewUniform(col), image.Point{}, draw.Src)
}

// frame draws a one pixel border inside r.
func (c *canvas) frame(r image.Rectangle, col color.Color, width int) {
	c.fill(image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+width), col)
	c.fill(image.Rect(r.Min.X, r.Max.Y-width, r.Max.X, r.Max.Y), col)
	c.fill(image.Rect(r.Min.X, r.Min.Y, r.Min.X+width, r.Max.Y), col)
	c.fill(image.Rect(r.Max.X-width, r.Min.Y, r.Max.X, r.Max.Y), col)
}

// text draws one line with its top at y.
func (c *canvas) text(k fontKind, s string, x, y int, col color.Color) {
	face := c.fonts.face(k)
	d := font.Drawer{Dst: c.img, Src: image.NewUniform(col), Face: face,
		Dot: fixed.P(x, y+face.Metrics().Ascent.Ceil())}
	d.DrawString(s)
}

// align is the horizontal alignment of text in its box.
type align int

const (
	alignLeft align = iota
	alignCenter
	alignRight
)

// textBox draws s wrapped into r and returns the height used.
func (c *canvas) textBox(k fontKind, s string, r image.Rectangle, a align, col color.Color) int {
	lh := c.fonts.lineHeight(k)
	y := r.Min.Y
	for _, line := range c.fonts.wrap(k, s, r.Dx()) {
		x := r.Min.X
		switch a {
		case alignCenter:
			x += (r.Dx() - c.fonts.width(k, line)) / 2
		case alignRight:
			x += r.Dx() - c.fonts.width(k, line)
		}
		c.text(k, line, x, y, col)
		y += lh
	}
	return y - r.Min.Y
}

// disc draws a filled circle in r with antialiased edges.
func (c *canvas) disc(r image.Rectangle, col color.RGBA) {
	cx, cy := float64(r.Min.X+r.Max.X)/2, float64(r.Min.Y+r.Max.Y)/2
	rad := float64(r.Dx()) / 2
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			d := rad - math.Sqrt(dx*dx+dy*dy)
			if d <= -0.5 {
				continue
			}
			a := min(1, d+0.5)
			bg := c.img.RGBAAt(x, y)
			mix := func(f, b uint8) uint8 { return uint8(float64(f)*a + float64(b)*(1-a)) }
			c.img.SetRGBA(x, y, color.RGBA{mix(col.R, bg.R), mix(col.G, bg.G), mix(col.B, bg.B), 0xff})
		}
	}
}
