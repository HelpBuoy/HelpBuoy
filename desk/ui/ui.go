// Package ui is the desk app's small immediate-mode widget set drawn with
// raylib (raygui needs cgo): labels, buttons, list rows, a single-line text
// field, a checkbox, tabs and a vertical scroll area.
//
// All coordinates are logical pixels; fonts are rasterized at the window's
// DPI scale so text stays sharp on high-DPI screens. Everything here must run
// on the main OS thread, between Begin and End of one frame.
package ui

import (
	"runtime"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/goregular"

	"github.com/mevdschee/helpbuoy/proto/i18n"
)

// FontKind selects one of the embedded Go fonts.
type FontKind int

const (
	Regular FontKind = iota
	Bold
	Mono
)

// Text sizes in logical pixels.
const (
	SizeSmall   = 14
	SizeBody    = 16
	SizeHeading = 20
	SizeLarge   = 26
)

// Colors of the app.
var (
	ColBackground = rl.NewColor(0xf4, 0xf5, 0xf7, 0xff)
	ColPanel      = rl.NewColor(0xff, 0xff, 0xff, 0xff)
	ColBorder     = rl.NewColor(0xd8, 0xdb, 0xe0, 0xff)
	ColText       = rl.NewColor(0x1d, 0x23, 0x2b, 0xff)
	ColMuted      = rl.NewColor(0x6b, 0x72, 0x80, 0xff)
	ColAccent     = rl.NewColor(0x25, 0x63, 0xeb, 0xff)
	ColAccentDark = rl.NewColor(0x1d, 0x4e, 0xd8, 0xff)
	ColAccentSoft = rl.NewColor(0xdb, 0xea, 0xfe, 0xff)
	ColHover      = rl.NewColor(0xee, 0xf0, 0xf3, 0xff)
	ColPressed    = rl.NewColor(0xe2, 0xe5, 0xe9, 0xff)
	ColOK         = rl.NewColor(0x16, 0xa3, 0x4a, 0xff)
	ColWarn       = rl.NewColor(0xd9, 0x77, 0x06, 0xff)
	ColError      = rl.NewColor(0xdc, 0x26, 0x26, 0xff)
	ColErrorSoft  = rl.NewColor(0xfe, 0xe2, 0xe2, 0xff)
	ColSelection  = rl.NewColor(0xbf, 0xdb, 0xfe, 0xff)
	ColViewer     = rl.NewColor(0x11, 0x13, 0x16, 0xff)
)

type fontKey struct {
	kind FontKind
	size int
}

// Context holds the per-frame input snapshot and the widget state.
type Context struct {
	scale float32
	fonts map[fontKey]rl.Font

	iconPNG []byte       // the app icon, set by SetIcon
	icon    rl.Texture2D // loaded on first use, with mipmaps

	// Input snapshot of this frame.
	Mouse         rl.Vector2
	MousePressed  bool // left button went down this frame
	MouseReleased bool
	MouseDown     bool
	Wheel         rl.Vector2
	Chars         []rune // typed characters, in order
	Shift         bool
	Ctrl          bool // Control, or Command on macOS: the shortcut modifier
	Now           time.Time

	// Interacted is set when there was any input this frame.
	Interacted bool

	// Focus is the id of the widget that has keyboard focus, or "".
	Focus        string
	focusClaimed bool

	active    string // widget the left button went down on
	clip      []rl.Rectangle
	cursor    rl.MouseCursor
	curCursor rl.MouseCursor
	hidden    bool // HideCursor requested this frame
	isHidden  bool
	scroll    map[string]float32
	lastClick time.Time
	tooltip   string
	tipPos    rl.Vector2
}

// New returns a Context. Call Load after InitWindow.
func New() *Context {
	return &Context{fonts: map[fontKey]rl.Font{}, scroll: map[string]float32{}}
}

// Scale returns the DPI scale the fonts are rasterized for.
func (c *Context) Scale() float32 { return c.scale }

// codepoints covers ASCII, Latin-1, Latin Extended-A and the few symbols the
// app draws, so user@machine names in most European languages render, plus
// every character the embedded translations use.
var codepoints = func() []rune {
	var cp []rune
	for r := rune(32); r < 127; r++ {
		cp = append(cp, r)
	}
	for r := rune(0xa0); r <= 0x17f; r++ {
		cp = append(cp, r)
	}
	cp = append(cp, '…', '•', '–', '—', '←', '→', '▲', '►', '▼', '◄', '✓')
	seen := map[rune]bool{}
	for _, r := range cp {
		seen[r] = true
	}
	for _, r := range i18n.Chars() {
		if !seen[r] {
			seen[r] = true
			cp = append(cp, r)
		}
	}
	return cp
}()

// font returns the font for kind and size, loading it on first use.
func (c *Context) font(kind FontKind, size float32) rl.Font {
	k := fontKey{kind, int(size + 0.5)}
	if f, ok := c.fonts[k]; ok {
		return f
	}
	data := goregular.TTF
	switch kind {
	case Bold:
		data = gobold.TTF
	case Mono:
		data = gomono.TTF
	}
	f := rl.LoadFontFromMemory(".ttf", data, int32(float32(k.size)*c.scale+0.5), codepoints)
	rl.SetTextureFilter(f.Texture, rl.FilterBilinear)
	c.fonts[k] = f
	return f
}

// Unload frees all fonts and the icon texture.
func (c *Context) Unload() {
	for k, f := range c.fonts {
		rl.UnloadFont(f)
		delete(c.fonts, k)
	}
	if c.icon.ID != 0 {
		rl.UnloadTexture(c.icon)
		c.icon = rl.Texture2D{}
	}
}

// SetIcon sets the app icon (PNG) that DrawIcon draws.
func (c *Context) SetIcon(png []byte) { c.iconPNG = png }

// DrawIcon draws the app icon size logical pixels square, centered on
// center. The texture has mipmaps and trilinear filtering, so it stays
// smooth when drawn much smaller than the PNG.
func (c *Context) DrawIcon(center rl.Vector2, size float32) {
	if c.icon.ID == 0 {
		if len(c.iconPNG) == 0 {
			return
		}
		img := rl.LoadImageFromMemory(".png", c.iconPNG, int32(len(c.iconPNG)))
		if img == nil || img.Width == 0 {
			c.iconPNG = nil
			return
		}
		c.icon = rl.LoadTextureFromImage(img)
		rl.UnloadImage(img)
		rl.GenTextureMipmaps(&c.icon)
		rl.SetTextureFilter(c.icon, rl.FilterTrilinear)
	}
	src := rl.NewRectangle(0, 0, float32(c.icon.Width), float32(c.icon.Height))
	dst := rl.NewRectangle(center.X-size/2, center.Y-size/2, size, size)
	rl.DrawTexturePro(c.icon, src, dst, rl.NewVector2(0, 0), 0, rl.White)
}

func dpiScale() float32 {
	s := rl.GetWindowScaleDPI().Y
	if s < 1 {
		return 1
	}
	return s
}

// WindowSize returns the window size in logical pixels.
func WindowSize() (w, h float32) {
	sc := rl.GetWindowScaleDPI()
	if sc.X <= 0 || sc.Y <= 0 {
		sc = rl.NewVector2(1, 1)
	}
	return float32(rl.GetRenderWidth()) / sc.X, float32(rl.GetRenderHeight()) / sc.Y
}

// Begin takes the input snapshot for a frame. Call it before BeginDrawing.
func (c *Context) Begin() {
	if s := dpiScale(); s != c.scale {
		c.Unload()
		c.scale = s
	}
	c.Now = time.Now()
	prev := c.Mouse
	c.Mouse = rl.GetMousePosition()
	c.MousePressed = rl.IsMouseButtonPressed(rl.MouseButtonLeft)
	c.MouseReleased = rl.IsMouseButtonReleased(rl.MouseButtonLeft)
	c.MouseDown = rl.IsMouseButtonDown(rl.MouseButtonLeft)
	c.Wheel = rl.GetMouseWheelMoveV()
	c.Chars = c.Chars[:0]
	for r := rl.GetCharPressed(); r != 0; r = rl.GetCharPressed() {
		c.Chars = append(c.Chars, r)
	}
	c.Shift = rl.IsKeyDown(rl.KeyLeftShift) || rl.IsKeyDown(rl.KeyRightShift)
	c.Ctrl = rl.IsKeyDown(rl.KeyLeftControl) || rl.IsKeyDown(rl.KeyRightControl)
	if runtime.GOOS == "darwin" {
		c.Ctrl = rl.IsKeyDown(rl.KeyLeftSuper) || rl.IsKeyDown(rl.KeyRightSuper)
	}
	c.Interacted = prev != c.Mouse || c.MousePressed || c.MouseReleased || c.MouseDown ||
		c.Wheel.X != 0 || c.Wheel.Y != 0 || len(c.Chars) > 0 || rl.GetKeyPressed() != 0
	c.focusClaimed = false
	c.cursor = rl.MouseCursorDefault
	c.hidden = false
	c.tooltip = ""
	c.clip = c.clip[:0]
}

// End finishes the frame's widget work: drops focus after a click outside
// any focusable widget, draws the tooltip and applies the mouse cursor. Call
// it before EndDrawing.
func (c *Context) End() {
	if c.MousePressed && !c.focusClaimed {
		c.Focus = ""
	}
	if c.MouseReleased {
		c.active = ""
	}
	if c.tooltip != "" {
		c.drawTooltip()
	}
	if c.hidden != c.isHidden {
		if c.hidden {
			rl.HideCursor()
		} else {
			rl.ShowCursor()
		}
		c.isHidden = c.hidden
	}
	if !c.hidden && c.cursor != c.curCursor {
		rl.SetMouseCursor(int32(c.cursor))
		c.curCursor = c.cursor
	}
}

// ClaimFocus gives keyboard focus to id. Call it on the click that focuses.
func (c *Context) ClaimFocus(id string) {
	c.Focus = id
	c.focusClaimed = true
}

// HideCursor hides the local mouse cursor for this frame.
func (c *Context) HideCursor() { c.hidden = true }

// SetCursor sets the mouse cursor shape for this frame.
func (c *Context) SetCursor(m rl.MouseCursor) { c.cursor = m }

// Hover reports whether the mouse is over r and inside the current clip.
func (c *Context) Hover(r rl.Rectangle) bool {
	if !rl.CheckCollisionPointRec(c.Mouse, r) {
		return false
	}
	if n := len(c.clip); n > 0 && !rl.CheckCollisionPointRec(c.Mouse, c.clip[n-1]) {
		return false
	}
	return true
}

// Clicked reports a completed click (press and release) on widget id at r.
func (c *Context) Clicked(id string, r rl.Rectangle) bool {
	h := c.Hover(r)
	if h && c.MousePressed {
		c.active = id
	}
	return h && c.MouseReleased && c.active == id
}

// Pressed reports whether widget id is being held down.
func (c *Context) Pressed(id string, r rl.Rectangle) bool {
	return c.active == id && c.MouseDown && c.Hover(r)
}

// Tooltip shows text near the mouse at the end of the frame.
func (c *Context) Tooltip(text string) {
	c.tooltip = text
	c.tipPos = c.Mouse
}

func (c *Context) drawTooltip() {
	m := c.Measure(Regular, SizeSmall, c.tooltip)
	w, h := WindowSize()
	r := rl.NewRectangle(c.tipPos.X+12, c.tipPos.Y+18, m.X+12, m.Y+8)
	if r.X+r.Width > w {
		r.X = w - r.Width - 4
	}
	if r.Y+r.Height > h {
		r.Y = c.tipPos.Y - r.Height - 6
	}
	rl.DrawRectangleRec(r, ColText)
	c.Text(Regular, SizeSmall, c.tooltip, r.X+6, r.Y+4, ColPanel)
}

// Text draws text with its top-left at x, y.
func (c *Context) Text(kind FontKind, size float32, s string, x, y float32, col rl.Color) {
	rl.DrawTextEx(c.font(kind, size), s, rl.NewVector2(snap(x, c.scale), snap(y, c.scale)), size, 0, col)
}

// Measure returns the size of text.
func (c *Context) Measure(kind FontKind, size float32, s string) rl.Vector2 {
	return rl.MeasureTextEx(c.font(kind, size), s, size, 0)
}

// TextIn draws text vertically centered in r, starting at r.X.
func (c *Context) TextIn(kind FontKind, size float32, s string, r rl.Rectangle, col rl.Color) {
	m := c.Measure(kind, size, s)
	c.Text(kind, size, s, r.X, r.Y+(r.Height-m.Y)/2, col)
}

// TextCentered draws text centered in r.
func (c *Context) TextCentered(kind FontKind, size float32, s string, r rl.Rectangle, col rl.Color) {
	m := c.Measure(kind, size, s)
	c.Text(kind, size, s, r.X+(r.Width-m.X)/2, r.Y+(r.Height-m.Y)/2, col)
}

// Ellipsize shortens s with "…" so it fits in width.
func (c *Context) Ellipsize(kind FontKind, size float32, s string, width float32) string {
	if c.Measure(kind, size, s).X <= width {
		return s
	}
	r := []rune(s)
	lo, hi := 0, len(r)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if c.Measure(kind, size, string(r[:mid])+"…").X <= width {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return string(r[:lo]) + "…"
}

// Wrap breaks s into lines that fit width, at spaces where possible.
func (c *Context) Wrap(kind FontKind, size float32, s string, width float32) []string {
	var lines []string
	for _, para := range splitLines(s) {
		line := ""
		for _, word := range splitWords(para) {
			try := word
			if line != "" {
				try = line + " " + word
			}
			if line != "" && c.Measure(kind, size, try).X > width {
				lines = append(lines, line)
				line = word
			} else {
				line = try
			}
		}
		lines = append(lines, line)
	}
	return lines
}

// Paragraph draws wrapped text and returns its height.
func (c *Context) Paragraph(kind FontKind, size float32, s string, x, y, width float32, col rl.Color) float32 {
	lh := size * 1.35
	lines := c.Wrap(kind, size, s, width)
	for i, l := range lines {
		c.Text(kind, size, l, x, y+float32(i)*lh, col)
	}
	return float32(len(lines)) * lh
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func splitWords(s string) []string {
	var out []string
	start := -1
	for i, r := range s {
		if r == ' ' {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}

// snap rounds a logical coordinate to the physical pixel grid.
func snap(v, scale float32) float32 {
	if scale <= 0 {
		return v
	}
	return float32(int(v*scale+0.5)) / scale
}

// PushClip restricts drawing and hit testing to r (intersected with the
// current clip).
func (c *Context) PushClip(r rl.Rectangle) {
	if n := len(c.clip); n > 0 {
		r = intersect(r, c.clip[n-1])
	}
	c.clip = append(c.clip, r)
	c.applyClip()
}

// PopClip undoes the last PushClip.
func (c *Context) PopClip() {
	c.clip = c.clip[:len(c.clip)-1]
	rl.EndScissorMode()
	if len(c.clip) > 0 {
		c.applyClip()
	}
}

func (c *Context) applyClip() {
	r := c.clip[len(c.clip)-1]
	rl.BeginScissorMode(int32(r.X), int32(r.Y), int32(r.Width+0.5), int32(r.Height+0.5))
}

func intersect(a, b rl.Rectangle) rl.Rectangle {
	x0, y0 := max(a.X, b.X), max(a.Y, b.Y)
	x1, y1 := min(a.X+a.Width, b.X+b.Width), min(a.Y+a.Height, b.Y+b.Height)
	if x1 < x0 {
		x1 = x0
	}
	if y1 < y0 {
		y1 = y0
	}
	return rl.NewRectangle(x0, y0, x1-x0, y1-y0)
}
