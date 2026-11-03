package ui

import (
	"image"
	"image/color"
	"strings"
	"unicode"
)

// kind is the type of a widget.
type kind int

const (
	kindLabel  kind = iota
	kindButton      // push button
	kindField       // one line text entry
	kindKeyBox      // read-only wrapped text on white, for the key line
	kindLink        // underlined clickable text
)

// widget is one control. Positions are in 96-DPI units; the window scales
// them. There is no widget tree: the window lists them all and shows the
// ones of the current screen.
type widget struct {
	kind       kind
	x, y, w, h int
	font       fontKind
	align      align
	text       string
	color      color.Color // labels; nil is the normal text color
	visible    bool
	enabled    bool
	isDefault  bool // a default button has a blue border
	onClick    func()

	field *field // kindField
}

func newWidget(k kind, x, y, w, h int, f fontKind) *widget {
	wd := &widget{kind: k, x: x, y: y, w: w, h: h, font: f, enabled: true}
	if k == kindField {
		wd.field = &field{}
	}
	return wd
}

func (wd *widget) focusable() bool {
	return wd.visible && wd.enabled && (wd.kind == kindButton || wd.kind == kindField || wd.kind == kindLink)
}

// field is the editing state of a text entry. Positions are rune indexes.
type field struct {
	text   []rune
	caret  int
	anchor int // other end of the selection; == caret when none
	scroll int // pixels scrolled to keep the caret visible
	limit  int
}

func (f *field) set(s string) {
	f.text = []rune(s)
	f.caret, f.anchor, f.scroll = len(f.text), len(f.text), 0
}

func (f *field) selectAll() { f.anchor, f.caret = 0, len(f.text) }

func (f *field) selection() (int, int) {
	return min(f.caret, f.anchor), max(f.caret, f.anchor)
}

func (f *field) selected() string {
	a, b := f.selection()
	return string(f.text[a:b])
}

// insert replaces the selection with s; line breaks and other control
// characters are dropped, as in a one line entry.
func (f *field) insert(s string) {
	var rs []rune
	for _, r := range s {
		if !unicode.IsControl(r) {
			rs = append(rs, r)
		}
	}
	a, b := f.selection()
	if f.limit > 0 {
		rs = rs[:max(0, min(len(rs), f.limit-(len(f.text)-(b-a))))]
	}
	t := append([]rune{}, f.text[:a]...)
	t = append(t, rs...)
	t = append(t, f.text[b:]...)
	f.text = t
	f.caret = a + len(rs)
	f.anchor = f.caret
}

// erase deletes the selection, or the character before (back) or after
// the caret.
func (f *field) erase(back bool) {
	a, b := f.selection()
	if a == b {
		if back && a > 0 {
			a--
		} else if !back && b < len(f.text) {
			b++
		}
	}
	f.text = append(f.text[:a:a], f.text[b:]...)
	f.caret, f.anchor = a, a
}

// move puts the caret at pos, extending the selection when extend is set.
func (f *field) move(pos int, extend bool) {
	f.caret = max(0, min(pos, len(f.text)))
	if !extend {
		f.anchor = f.caret
	}
}

// wordLeft and wordRight are the caret positions for Ctrl+Left/Right.
func (f *field) wordLeft() int {
	i := f.caret
	for i > 0 && f.text[i-1] == ' ' {
		i--
	}
	for i > 0 && !strings.ContainsRune(" .:/", f.text[i-1]) {
		i--
	}
	return i
}

func (f *field) wordRight() int {
	i := f.caret
	for i < len(f.text) && !strings.ContainsRune(" .:/", f.text[i]) {
		i++
	}
	for i < len(f.text) && strings.ContainsRune(" .:/", f.text[i]) {
		i++
	}
	return i
}

// draw paints the widget. focused is whether it has the keyboard focus
// (and the window has it too); hover and pressed are for buttons.
func (wd *widget) draw(c *canvas, r image.Rectangle, s float64, focused, hover, pressed bool) {
	px := func(v int) int { return max(1, int(float64(v)*s+0.5)) }
	switch wd.kind {
	case kindLabel:
		col := wd.color
		if col == nil {
			col = colText
		}
		c.textBox(wd.font, wd.text, r, wd.align, col)

	case kindLink:
		col := colLink
		if !wd.enabled {
			col = colDisabled
		}
		c.text(wd.font, wd.text, r.Min.X, r.Min.Y, col)
		tw := c.fonts.width(wd.font, wd.text)
		uy := r.Min.Y + c.fonts.face(wd.font).Metrics().Ascent.Ceil() + px(2)
		if hover || focused {
			c.fill(image.Rect(r.Min.X, uy, r.Min.X+tw, uy+px(1)), col)
		}
		if focused {
			c.frame(image.Rect(r.Min.X-px(2), r.Min.Y-px(1), r.Min.X+tw+px(2), uy+px(3)), colGray, 1)
		}

	case kindButton:
		bg, border := colBtn, colBorder
		switch {
		case !wd.enabled:
			bg = color.RGBA{0xcc, 0xcc, 0xcc, 0xff}
			border = color.RGBA{0xbf, 0xbf, 0xbf, 0xff}
		case pressed && hover:
			bg, border = colBtnPressed, color.RGBA{0x00, 0x54, 0x99, 0xff}
		case hover:
			bg, border = colBtnHover, colAccent
		case wd.isDefault || focused:
			border = colAccent
		}
		c.fill(r, bg)
		bw := 1
		if (wd.isDefault || focused) && wd.enabled {
			bw = px(2)
		}
		c.frame(r, border, bw)
		if focused {
			in := r.Inset(px(4))
			dotted(c, in, colText)
		}
		col := colText
		if !wd.enabled {
			col = colDisabled
		}
		lh := c.fonts.lineHeight(wd.font)
		tw := c.fonts.width(wd.font, wd.text)
		c.text(wd.font, wd.text, r.Min.X+(r.Dx()-tw)/2, r.Min.Y+(r.Dy()-lh)/2, col)

	case kindField:
		f := wd.field
		bg, border := colWhite, colEditBorder
		if !wd.enabled {
			bg, border = colBg, colBorder
		} else if focused || hover {
			border = colAccent
		}
		c.fill(r, bg)
		c.frame(r, border, 1)
		if focused {
			c.fill(image.Rect(r.Min.X, r.Max.Y-px(2), r.Max.X, r.Max.Y), colAccent)
		}
		pad := px(5)
		inner := image.Rect(r.Min.X+pad, r.Min.Y, r.Max.X-pad, r.Max.Y)
		lh := c.fonts.lineHeight(wd.font)
		ty := r.Min.Y + (r.Dy()-lh)/2
		widthTo := func(i int) int { return c.fonts.width(wd.font, string(f.text[:i])) }
		cx := widthTo(f.caret)
		if cx-f.scroll > inner.Dx()-px(2) {
			f.scroll = cx - inner.Dx() + px(2)
		}
		if cx-f.scroll < 0 {
			f.scroll = cx
		}
		if total := widthTo(len(f.text)); total-f.scroll < inner.Dx()-px(2) {
			f.scroll = max(0, total-inner.Dx()+px(2))
		}
		sub := &canvas{img: c.img.SubImage(inner).(*image.RGBA), fonts: c.fonts}
		x0 := inner.Min.X - f.scroll
		if a, b := f.selection(); a != b && focused {
			sub.fill(image.Rect(x0+widthTo(a), ty, x0+widthTo(b), ty+lh), colSelection)
		}
		col := colText
		if !wd.enabled {
			col = colDisabled
		}
		sub.text(wd.font, string(f.text), x0, ty, col)
		if focused {
			sub.fill(image.Rect(x0+cx, ty, x0+cx+px(1), ty+lh), colText)
		}

	case kindKeyBox:
		c.fill(r, colWhite)
		c.frame(r, colBorder, 1)
		c.textBox(wd.font, wd.text, r.Inset(px(5)), alignLeft, colText)
	}
}

// dotted draws a dotted focus rectangle.
func dotted(c *canvas, r image.Rectangle, col color.RGBA) {
	for x := r.Min.X; x < r.Max.X; x += 2 {
		c.img.SetRGBA(x, r.Min.Y, col)
		c.img.SetRGBA(x, r.Max.Y-1, col)
	}
	for y := r.Min.Y; y < r.Max.Y; y += 2 {
		c.img.SetRGBA(r.Min.X, y, col)
		c.img.SetRGBA(r.Max.X-1, y, col)
	}
}

// caretAt returns the rune index in a field closest to pixel x relative to
// the text start.
func (wd *widget) caretAt(c *fonts, x int) int {
	f := wd.field
	best, bestD := 0, 1<<30
	for i := 0; i <= len(f.text); i++ {
		d := c.width(wd.font, string(f.text[:i])) - x
		if d < 0 {
			d = -d
		}
		if d < bestD {
			best, bestD = i, d
		}
	}
	return best
}
