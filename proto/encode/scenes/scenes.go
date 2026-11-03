// Package scenes generates deterministic synthetic screen sequences for the
// encoder tests and the helpbuoy-bench replay benchmark: typing in an editor,
// menus, scrolling a document, dragging a window, video and an idle screen.
// Every scenario renders the same pixels on every run.
package scenes

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"math/rand/v2"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Class says which PLAN.md 5.1 target a scenario measures.
type Class int

const (
	ClassTyping Class = iota // typing, menus, dialogs: 60 fps, <= 1 Mbit/s
	ClassScroll              // scrolling: >= 30 fps, mostly copy and cache
	ClassVideo               // full-screen video: >= 20 fps, lossy
	ClassIdle                // idle screen: < 2 % of a core
	ClassOther               // measured, no target
)

func (c Class) String() string {
	return [...]string{"typing", "scroll", "video", "idle", "other"}[c]
}

// Scenario is one synthetic sequence.
type Scenario struct {
	Name          string
	Class         Class
	FPS           int // capture rate the scenario models
	Frames        int
	Width, Height int
	// New returns a player that renders frame 0, 1, 2, ... in order into
	// dst (Width x Height). Players are independent of each other.
	New func() Player
}

// Player renders the frames of one run of a scenario.
type Player interface {
	// Frame renders frame i into dst; i counts up from 0 by one and dst is
	// the same image on every call, as players only draw what changes.
	Frame(i int, dst *image.RGBA)
}

// Default is the screen size of all scenarios.
const (
	W = 1920
	H = 1080
)

// All returns the scenarios in a fixed order.
func All() []Scenario {
	return []Scenario{
		{Name: "typing", Class: ClassTyping, FPS: 60, Frames: 300, Width: W, Height: H, New: newTyping},
		{Name: "menus", Class: ClassTyping, FPS: 60, Frames: 300, Width: W, Height: H, New: newMenus},
		{Name: "scroll", Class: ClassScroll, FPS: 60, Frames: 240, Width: W, Height: H, New: newScroll},
		{Name: "drag", Class: ClassOther, FPS: 60, Frames: 240, Width: W, Height: H, New: newDrag},
		{Name: "video", Class: ClassOther, FPS: 30, Frames: 120, Width: W, Height: H, New: newVideoWindow},
		{Name: "fullvideo", Class: ClassVideo, FPS: 30, Frames: 90, Width: W, Height: H, New: newFullVideo},
		{Name: "idle", Class: ClassIdle, FPS: 5, Frames: 50, Width: W, Height: H, New: newIdle},
	}
}

// Find returns the scenario called name.
func Find(name string) (Scenario, error) {
	for _, s := range All() {
		if s.Name == name {
			return s, nil
		}
	}
	return Scenario{}, fmt.Errorf("scenes: no scenario %q", name)
}

// Fonts are shared; font.Face is not safe for concurrent use, so drawing
// takes faceMu.
var (
	faceMu  sync.Mutex
	mono    font.Face
	sans    font.Face
	sansBig font.Face
)

func init() {
	func() {
		mk := func(ttf []byte, size float64) font.Face {
			f, err := opentype.Parse(ttf)
			if err != nil {
				panic(err)
			}
			face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 96, Hinting: font.HintingFull})
			if err != nil {
				panic(err)
			}
			return face
		}
		mono = mk(gomono.TTF, 11)
		sans = mk(goregular.TTF, 11)
		sansBig = mk(goregular.TTF, 18)
	}()
}

var (
	white    = color.RGBA{255, 255, 255, 255}
	black    = color.RGBA{0, 0, 0, 255}
	textGray = color.RGBA{30, 30, 30, 255}
	chrome   = color.RGBA{240, 240, 240, 255}
	border   = color.RGBA{160, 160, 160, 255}
	accent   = color.RGBA{0, 95, 184, 255}
)

func fill(dst *image.RGBA, r image.Rectangle, c color.RGBA) {
	draw.Draw(dst, r, image.NewUniform(c), image.Point{}, draw.Src)
}

// text draws s with its baseline at (x, y) and returns the x after it.
func text(dst *image.RGBA, face font.Face, x, y int, s string, c color.RGBA) int {
	faceMu.Lock()
	defer faceMu.Unlock()
	d := font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: face, Dot: fixed.P(x, y)}
	d.DrawString(s)
	return d.Dot.X.Round()
}

func monoAdvance() int {
	faceMu.Lock()
	defer faceMu.Unlock()
	a, _ := mono.GlyphAdvance('m')
	return a.Round()
}

// desktop draws a banded blue wallpaper, a few icons and a taskbar.
func desktop(dst *image.RGBA) {
	b := dst.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		band := uint8((y / 6) % 256)
		c := color.RGBA{20 + band/8, 60 + band/5, 120 + band/4, 255}
		fill(dst, image.Rect(b.Min.X, y, b.Max.X, y+1), c)
	}
	names := []string{"Computer", "Documents", "Recycle Bin", "Browser", "Mail", "Projects"}
	for i, n := range names {
		x, y := 30, 30+i*100
		fill(dst, image.Rect(x+12, y, x+60, y+48), color.RGBA{uint8(200 - i*25), uint8(120 + i*20), 60, 255})
		fill(dst, image.Rect(x+20, y+8, x+52, y+40), color.RGBA{250, 230, uint8(100 + i*20), 255})
		text(dst, sans, x, y+66, n, white)
	}
	tb := image.Rect(b.Min.X, b.Max.Y-40, b.Max.X, b.Max.Y)
	fill(dst, tb, color.RGBA{32, 32, 40, 255})
	fill(dst, image.Rect(8, tb.Min.Y+6, 100, tb.Max.Y-6), accent)
	text(dst, sans, 30, tb.Min.Y+26, "Start", white)
	text(dst, sans, b.Max.X-90, tb.Min.Y+26, "14:32", white)
}

// window draws a frame with a title bar and returns the computer rectangle.
func window(dst *image.RGBA, r image.Rectangle, title string) image.Rectangle {
	fill(dst, r, border)
	in := r.Inset(1)
	fill(dst, image.Rect(in.Min.X, in.Min.Y, in.Max.X, in.Min.Y+30), chrome)
	text(dst, sans, in.Min.X+10, in.Min.Y+20, title, textGray)
	for i, c := range []color.RGBA{{200, 200, 200, 255}, {200, 200, 200, 255}, {232, 17, 35, 255}} {
		x := in.Max.X - 46*(3-i)
		fill(dst, image.Rect(x+16, in.Min.Y+14, x+30, in.Min.Y+16), c)
	}
	computer := image.Rect(in.Min.X, in.Min.Y+30, in.Max.X, in.Max.Y)
	fill(dst, computer, white)
	return computer
}

// Source text typed and shown by the scenarios.
var sourceLines = strings.Split(`package main

import (
	"fmt"
	"os"
	"strings"
)

// wordCount returns how often every word occurs in s.
func wordCount(s string) map[string]int {
	counts := make(map[string]int)
	for _, w := range strings.Fields(s) {
		counts[strings.ToLower(w)]++
	}
	return counts
}

func main() {
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for w, n := range wordCount(string(data)) {
		fmt.Printf("%-20s %d\n", w, n)
	}
}`, "\n")

var prose = strings.Fields(`The server never sees the screen: everything inside the tunnel is
end-to-end encrypted between the computer and the techie. Screen updates are sent as
small rectangles, encoded losslessly when they contain text and user interface elements,
and as JPEG when they look like photos or video. Regions that stop changing are refined
so that the final image is exact. Scrolling is detected and sent as a copy of pixels the
techie already has, so only the newly exposed strip is encoded. A cache of recently sent
tiles lets the computer refer to pixels by slot number when a window is shown again.`)

// paragraph returns deterministic prose of n words starting at word k.
func paragraph(k, n int) []string {
	var lines []string
	var cur []string
	length := 0
	for i := range n {
		w := prose[(k+i*7)%len(prose)]
		if length+len(w) > 110 {
			lines = append(lines, strings.Join(cur, " "))
			cur, length = nil, 0
		}
		cur = append(cur, w)
		length += len(w) + 1
	}
	return append(lines, strings.Join(cur, " "))
}

// --- typing -----------------------------------------------------------

type typing struct {
	computer image.Rectangle
	lineH    int
	adv      int
	lines    []string // typed so far
	src      []rune   // all text to type
	pos      int      // runes typed
	top      int      // first visible line
	visible  int
	prevCur  [2]int
}

func newTyping() Player { return &typing{} }

func (p *typing) Frame(i int, dst *image.RGBA) {
	if i == 0 {
		desktop(dst)
		p.computer = window(dst, image.Rect(0, 0, W, H-40), "main.go - Editor")
		p.lineH, p.adv = 18, monoAdvance()
		p.visible = (p.computer.Dy() - 30) / p.lineH
		// Start with a screenful of code, then type the program again below.
		for k := 0; k < p.visible-6; k++ {
			p.lines = append(p.lines, sourceLines[k%len(sourceLines)])
		}
		p.lines = append(p.lines, "")
		p.src = []rune(strings.Join(sourceLines, "\n") + "\n" + strings.Join(sourceLines, "\n"))
		p.renderAll(dst)
	}
	// Type a character every second frame (30 characters per second).
	if i%2 == 1 && p.pos < len(p.src) {
		c := p.src[p.pos]
		p.pos++
		last := len(p.lines) - 1
		if c == '\n' {
			p.lines = append(p.lines, "")
			if len(p.lines)-p.top > p.visible {
				p.top++
				p.renderAll(dst)
			}
		} else {
			if c == '\t' {
				p.lines[last] += "    "
			} else {
				p.lines[last] += string(c)
			}
		}
	}
	// Repaint the line of the old and the new cursor position and the
	// status bar; the cursor blinks every 30 frames.
	cur := [2]int{len(p.lines) - 1, len([]rune(p.lines[len(p.lines)-1]))}
	if p.prevCur[0] >= p.top && p.prevCur[0] < len(p.lines) {
		p.renderLine(dst, p.prevCur[0])
	}
	p.renderLine(dst, cur[0])
	if (i/30)%2 == 0 {
		x := p.computer.Min.X + 60 + cur[1]*p.adv
		y := p.computer.Min.Y + (cur[0]-p.top)*p.lineH
		fill(dst, image.Rect(x, y+2, x+2, y+p.lineH), black)
	}
	p.prevCur = cur
	sb := image.Rect(p.computer.Min.X, p.computer.Max.Y-24, p.computer.Max.X, p.computer.Max.Y)
	fill(dst, sb, accent)
	text(dst, sans, sb.Min.X+10, sb.Min.Y+17, fmt.Sprintf("Ln %d, Col %d    UTF-8    Go", cur[0]+1, cur[1]+1), white)
}

func (p *typing) renderAll(dst *image.RGBA) {
	fill(dst, image.Rect(p.computer.Min.X, p.computer.Min.Y, p.computer.Max.X, p.computer.Max.Y-24), white)
	for l := p.top; l < len(p.lines) && l < p.top+p.visible; l++ {
		p.renderLine(dst, l)
	}
}

func (p *typing) renderLine(dst *image.RGBA, l int) {
	if l < p.top || l >= p.top+p.visible {
		return
	}
	y := p.computer.Min.Y + (l-p.top)*p.lineH
	fill(dst, image.Rect(p.computer.Min.X, y, p.computer.Min.X+50, y+p.lineH), color.RGBA{245, 245, 245, 255})
	fill(dst, image.Rect(p.computer.Min.X+50, y, p.computer.Max.X, y+p.lineH), white)
	text(dst, mono, p.computer.Min.X+8, y+14, fmt.Sprintf("%4d", l+1), color.RGBA{140, 140, 140, 255})
	s := p.lines[l]
	c := textGray
	switch t := strings.TrimSpace(s); {
	case strings.HasPrefix(t, "//"):
		c = color.RGBA{0, 128, 0, 255}
	case strings.HasPrefix(t, "func") || strings.HasPrefix(t, "for") || strings.HasPrefix(t, "if") ||
		strings.HasPrefix(t, "package") || strings.HasPrefix(t, "import") || strings.HasPrefix(t, "return"):
		c = color.RGBA{0, 0, 200, 255}
	case strings.Contains(t, `"`):
		c = color.RGBA{163, 21, 21, 255}
	}
	text(dst, mono, p.computer.Min.X+60, y+14, strings.ReplaceAll(s, "\t", "    "), c)
}

// --- menus ------------------------------------------------------------

type menus struct {
	base     *image.RGBA
	computer image.Rectangle
}

func newMenus() Player { return &menus{} }

var menuNames = []string{"File", "Edit", "View", "Insert", "Format", "Tools", "Help"}

func (p *menus) Frame(i int, dst *image.RGBA) {
	if i == 0 {
		p.base = image.NewRGBA(dst.Bounds())
		desktop(p.base)
		p.computer = window(p.base, image.Rect(160, 40, 1700, 1000), "Report.txt - Writer")
		fill(p.base, image.Rect(p.computer.Min.X, p.computer.Min.Y, p.computer.Max.X, p.computer.Min.Y+28), chrome)
		for k, n := range menuNames {
			text(p.base, sans, p.computer.Min.X+12+k*70, p.computer.Min.Y+19, n, textGray)
		}
		y := p.computer.Min.Y + 60
		for k := 0; y < p.computer.Max.Y-20; k++ {
			for _, l := range paragraph(k*13, 60) {
				text(p.base, sans, p.computer.Min.X+40, y, l, textGray)
				y += 20
			}
			y += 14
		}
	}
	copy(dst.Pix, p.base.Pix)
	// A 40 frame cycle: a menu opens, the pointer hovers down its items,
	// the menu closes and the screen rests.
	cycle, f := i/40, i%40
	if f >= 30 {
		return
	}
	k := cycle % len(menuNames)
	mx, my := p.computer.Min.X+4+k*70, p.computer.Min.Y+28
	fill(dst, image.Rect(mx+4, my-22, mx+66, my), color.RGBA{204, 228, 247, 255})
	text(dst, sans, mx+8, my-9, menuNames[k], textGray)
	items := 12
	m := image.Rect(mx, my, mx+280, my+items*26+8)
	fill(dst, m.Add(image.Pt(4, 4)), color.RGBA{0, 0, 0, 255})
	fill(dst, m, border)
	fill(dst, m.Inset(1), color.RGBA{249, 249, 249, 255})
	hover := f / 3
	for it := range items {
		r := image.Rect(m.Min.X+3, m.Min.Y+4+it*26, m.Max.X-3, m.Min.Y+4+(it+1)*26)
		c := textGray
		if it == hover {
			fill(dst, r, accent)
			c = white
		}
		text(dst, sans, r.Min.X+28, r.Min.Y+18, fmt.Sprintf("%s item %d", menuNames[k], it+1), c)
		text(dst, sans, r.Max.X-60, r.Min.Y+18, fmt.Sprintf("Ctrl+%c", 'A'+it), c)
	}
}

// --- scroll -----------------------------------------------------------

type scroll struct {
	base     *image.RGBA
	doc      *image.RGBA
	computer image.Rectangle
}

func newScroll() Player { return &scroll{} }

func (p *scroll) Frame(i int, dst *image.RGBA) {
	if i == 0 {
		p.base = image.NewRGBA(dst.Bounds())
		desktop(p.base)
		p.computer = window(p.base, image.Rect(200, 0, 1720, H-40), "Design notes - Viewer")
		w := p.computer.Dx() - 16
		p.doc = image.NewRGBA(image.Rect(0, 0, w, 14000))
		fill(p.doc, p.doc.Bounds(), white)
		y := 40
		for k := 0; y < p.doc.Rect.Dy()-400; k++ {
			text(p.doc, sansBig, 60, y, fmt.Sprintf("%d. Section about screen updates", k+1), color.RGBA{20, 40, 90, 255})
			y += 34
			for _, l := range paragraph(k*11, 120) {
				text(p.doc, sans, 60, y, l, textGray)
				y += 20
			}
			y += 16
			if k%3 == 1 {
				// A figure: a smooth many-colored picture.
				fig := image.Rect(60, y, 60+640, y+240)
				for fy := fig.Min.Y; fy < fig.Max.Y; fy++ {
					for fx := fig.Min.X; fx < fig.Max.X; fx++ {
						u, v := float64(fx-fig.Min.X)/640, float64(fy-fig.Min.Y)/240
						p.doc.SetRGBA(fx, fy, color.RGBA{uint8(255 * u), uint8(255 * v), uint8(128 + 127*math.Sin(6*u+3*v)), 255})
					}
				}
				y += 260
			}
		}
	}
	copy(dst.Pix, p.base.Pix)
	off := i * 24 // 24 px per frame: 1440 px/s at 60 fps
	view := image.Rect(p.computer.Min.X, p.computer.Min.Y, p.computer.Max.X-16, p.computer.Max.Y)
	draw.Draw(dst, view, p.doc, image.Pt(0, off), draw.Src)
	sb := image.Rect(p.computer.Max.X-16, p.computer.Min.Y, p.computer.Max.X, p.computer.Max.Y)
	fill(dst, sb, chrome)
	th := sb.Dy() * view.Dy() / p.doc.Rect.Dy()
	ty := sb.Min.Y + off*sb.Dy()/p.doc.Rect.Dy()
	fill(dst, image.Rect(sb.Min.X+3, ty, sb.Max.X-3, ty+th), border)
}

// --- drag -------------------------------------------------------------

type drag struct {
	base, win *image.RGBA
}

func newDrag() Player { return &drag{} }

func (p *drag) Frame(i int, dst *image.RGBA) {
	if i == 0 {
		p.base = image.NewRGBA(dst.Bounds())
		desktop(p.base)
		p.win = image.NewRGBA(image.Rect(0, 0, 900, 600))
		computer := window(p.win, p.win.Bounds(), "Notes")
		y := computer.Min.Y + 30
		for _, l := range paragraph(5, 300) {
			if y > computer.Max.Y-10 {
				break
			}
			text(p.win, sans, computer.Min.X+20, y, l, textGray)
			y += 20
		}
	}
	copy(dst.Pix, p.base.Pix)
	// Out and back along a diagonal, 7 px right and 3 px down per frame.
	k := i
	if k >= 120 {
		k = 239 - k
	}
	pos := image.Pt(200+7*k, 120+3*k)
	draw.Draw(dst, p.win.Bounds().Add(pos), p.win, image.Point{}, draw.Src)
}

// --- video ------------------------------------------------------------

type video struct {
	base *image.RGBA
	area image.Rectangle
	rng  *rand.Rand
	sin  [1024]float64
}

func newVideoWindow() Player { return &video{} }

func newFullVideo() Player { return &video{area: image.Rect(0, 0, W, H)} }

func (p *video) Frame(i int, dst *image.RGBA) {
	if i == 0 {
		for k := range p.sin {
			p.sin[k] = math.Sin(2 * math.Pi * float64(k) / 1024)
		}
		p.rng = rand.New(rand.NewPCG(1, 2))
		p.base = image.NewRGBA(dst.Bounds())
		desktop(p.base)
		if p.area.Empty() {
			c := window(p.base, image.Rect(300, 100, 300+1282, 100+31+720+40), "Video player")
			p.area = image.Rect(c.Min.X, c.Min.Y, c.Max.X, c.Min.Y+720)
			fill(p.base, image.Rect(c.Min.X, p.area.Max.Y, c.Max.X, c.Max.Y), color.RGBA{30, 30, 30, 255})
		}
		copy(dst.Pix, p.base.Pix)
	}
	a := p.area
	// Moving smooth color fields, two moving discs, and mild noise.
	type disc struct{ x, y, r float64 }
	discs := []disc{
		{float64(a.Min.X) + float64(a.Dx())*(0.5+0.4*math.Sin(float64(i)/23)), float64(a.Min.Y) + float64(a.Dy())*(0.5+0.3*math.Cos(float64(i)/17)), float64(a.Dy()) / 6},
		{float64(a.Min.X) + float64(a.Dx())*(0.5+0.3*math.Cos(float64(i)/29)), float64(a.Min.Y) + float64(a.Dy())*(0.5+0.4*math.Sin(float64(i)/31)), float64(a.Dy()) / 9},
	}
	for y := a.Min.Y; y < a.Max.Y; y++ {
		sy := p.sin[(y*3-i*11)&1023]
		cy := p.sin[(y*2+i*5+256)&1023]
		row := dst.Pix[dst.PixOffset(a.Min.X, y):]
		for x := a.Min.X; x < a.Max.X; x++ {
			sx := p.sin[(x*2+i*7)&1023]
			cx := p.sin[(x+i*3+512)&1023]
			r := 120 + 70*sx + 40*sy
			g := 110 + 60*cy + 50*sx*cy
			b := 130 + 80*cx*sy
			for _, d := range discs {
				dx, dy := float64(x)-d.x, float64(y)-d.y
				if dx*dx+dy*dy < d.r*d.r {
					r, g, b = 230-0.2*dy, 200+0.2*dx, 60
				}
			}
			n := float64(p.rng.IntN(13)) - 6
			o := (x - a.Min.X) * 4
			row[o] = clamp8(r + n)
			row[o+1] = clamp8(g + n)
			row[o+2] = clamp8(b + n)
			row[o+3] = 255
		}
	}
}

func clamp8(v float64) uint8 {
	return uint8(min(max(v, 0), 255))
}

// --- idle -------------------------------------------------------------

type idle struct{}

func newIdle() Player { return idle{} }

func (idle) Frame(i int, dst *image.RGBA) {
	if i == 0 {
		desktop(dst)
		c := window(dst, image.Rect(300, 100, 1500, 900), "Inbox - Mail")
		y := c.Min.Y + 30
		for _, l := range paragraph(3, 400) {
			if y > c.Max.Y-10 {
				break
			}
			text(dst, sans, c.Min.X+20, y, l, textGray)
			y += 20
		}
	}
}
