package ui

import (
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/mevdschee/help-me-win/helper/ui/textedit"
)

// ButtonKind selects a button's look.
type ButtonKind int

const (
	Normal  ButtonKind = iota // bordered, white
	Primary                   // filled accent
	Danger                    // filled red
	Flat                      // text only, for small inline actions
)

// Button draws a button and reports a click. A disabled button never clicks.
func (c *Context) Button(id string, r rl.Rectangle, label string, kind ButtonKind, disabled bool) bool {
	hover := !disabled && c.Hover(r)
	pressed := !disabled && c.Pressed(id, r)
	bg, fg, border := ColPanel, ColText, ColBorder
	switch kind {
	case Primary:
		bg, fg, border = ColAccent, ColPanel, ColAccent
		if hover {
			bg = ColAccentDark
		}
	case Danger:
		bg, fg, border = ColError, ColPanel, ColError
		if hover {
			bg = rl.NewColor(0xb9, 0x1c, 0x1c, 0xff)
		}
	case Flat:
		bg, fg, border = rl.Blank, ColAccent, rl.Blank
		if hover {
			bg = ColHover
		}
	default:
		if hover {
			bg = ColHover
		}
	}
	if pressed && kind != Primary && kind != Danger {
		bg = ColPressed
	}
	if disabled {
		fg = ColMuted
		if kind == Primary || kind == Danger {
			bg, fg, border = ColPressed, ColMuted, ColPressed
		}
	}
	rl.DrawRectangleRounded(r, 0.25, 6, bg)
	if border != rl.Blank {
		rl.DrawRectangleRoundedLinesEx(r, 0.25, 6, 1, border)
	}
	font := Regular
	if kind == Primary || kind == Danger {
		font = Bold
	}
	c.TextCentered(font, SizeBody, label, r, fg)
	if hover {
		c.SetCursor(rl.MouseCursorPointingHand)
	}
	if disabled {
		return false
	}
	return c.Clicked(id, r)
}

// ButtonWidth returns a comfortable width for a button with label.
func (c *Context) ButtonWidth(label string) float32 {
	return c.Measure(Regular, SizeBody, label).X + 28
}

// Row draws a list row background and reports hover and click.
func (c *Context) Row(id string, r rl.Rectangle, bg rl.Color, selected bool) (hover, clicked bool) {
	hover = c.Hover(r)
	if selected {
		bg = ColAccentSoft
	} else if hover && bg == ColPanel {
		bg = ColHover
	}
	rl.DrawRectangleRec(r, bg)
	return hover, c.Clicked(id, r)
}

// Checkbox draws a checkbox with a label and toggles *v on click. It
// reports whether the value changed.
func (c *Context) Checkbox(id string, r rl.Rectangle, label string, v *bool) bool {
	box := rl.NewRectangle(r.X, r.Y+(r.Height-18)/2, 18, 18)
	hover := c.Hover(r)
	if *v {
		rl.DrawRectangleRounded(box, 0.25, 4, ColAccent)
		rl.DrawLineEx(rl.NewVector2(box.X+4, box.Y+9), rl.NewVector2(box.X+8, box.Y+13), 2, ColPanel)
		rl.DrawLineEx(rl.NewVector2(box.X+8, box.Y+13), rl.NewVector2(box.X+14, box.Y+5), 2, ColPanel)
	} else {
		rl.DrawRectangleRounded(box, 0.25, 4, ColPanel)
		col := ColBorder
		if hover {
			col = ColMuted
		}
		rl.DrawRectangleRoundedLinesEx(box, 0.25, 4, 1.5, col)
	}
	c.TextIn(Regular, SizeBody, label, rl.NewRectangle(r.X+28, r.Y, r.Width-28, r.Height), ColText)
	if hover {
		c.SetCursor(rl.MouseCursorPointingHand)
	}
	if c.Clicked(id, r) {
		*v = !*v
		return true
	}
	return false
}

// Tab draws one tab of a tab bar and reports a click on it. With icon set
// the app icon (a lifebuoy) stands before the title, for session tabs.
func (c *Context) Tab(id string, r rl.Rectangle, title string, active, icon bool) bool {
	hover := c.Hover(r)
	bg := ColBackground
	if active {
		bg = ColPanel
	} else if hover {
		bg = ColHover
	}
	rl.DrawRectangleRec(r, bg)
	if active {
		rl.DrawRectangleRec(rl.NewRectangle(r.X, r.Y+r.Height-3, r.Width, 3), ColAccent)
	}
	rl.DrawLineV(rl.NewVector2(r.X+r.Width, r.Y+6), rl.NewVector2(r.X+r.Width, r.Y+r.Height-6), ColBorder)
	textX := r.X + 12
	if icon {
		c.DrawIcon(rl.NewVector2(r.X+21, r.Y+r.Height/2), 18)
		textX += 22
	}
	textW := r.X + r.Width - 12 - textX
	font := Regular
	col := ColMuted
	if active {
		font, col = Bold, ColText
	}
	c.TextIn(font, SizeBody, c.Ellipsize(font, SizeBody, title, textW), rl.NewRectangle(textX, r.Y, textW, r.Height), col)
	if hover {
		c.SetCursor(rl.MouseCursorPointingHand)
	}
	return c.Clicked(id, r)
}

// BeginScroll starts a vertical scroll area at r for content of the given
// height (usually measured in the previous frame) and returns the y offset to
// add to the content's coordinates. Pair it with EndScroll.
func (c *Context) BeginScroll(id string, r rl.Rectangle, contentHeight float32) float32 {
	off := c.scroll[id]
	maxOff := contentHeight - r.Height
	if maxOff < 0 {
		maxOff = 0
	}
	if c.Hover(r) && c.Wheel.Y != 0 {
		off -= c.Wheel.Y * 48
	}
	off = min(max(off, 0), maxOff)
	c.scroll[id] = off
	c.PushClip(r)
	if maxOff > 0 {
		// A thin indicator on the right edge.
		h := r.Height * r.Height / contentHeight
		y := r.Y + (r.Height-h)*off/maxOff
		defer rl.DrawRectangleRounded(rl.NewRectangle(r.X+r.Width-5, y+2, 3, h-4), 1, 4, ColBorder)
	}
	return -off
}

// EndScroll ends a scroll area.
func (c *Context) EndScroll() { c.PopClip() }

// Field is the state of a single-line text field.
type Field struct {
	Edit        textedit.State
	Placeholder string
	Mono        bool
	Disabled    bool
	scroll      float32
	dragging    bool
}

// FieldResult reports what happened to a text field this frame.
type FieldResult struct {
	Changed   bool
	Submitted bool // Enter was pressed
}

// TextField draws and edits a single-line text field. Typing, Backspace,
// Delete, arrows (with Shift to select, Ctrl for words), Home, End, Ctrl+A,
// Ctrl+C, Ctrl+X and Ctrl+V work while it has focus; Ctrl is Command on
// macOS. Line breaks in pasted text are kept and shown as spaces.
func (c *Context) TextField(id string, r rl.Rectangle, f *Field) FieldResult {
	var res FieldResult
	focused := c.Focus == id && !f.Disabled
	hover := c.Hover(r) && !f.Disabled
	kind := Regular
	if f.Mono {
		kind = Mono
	}
	const size = SizeBody
	pad := float32(10)
	inner := rl.NewRectangle(r.X+pad, r.Y, r.Width-2*pad, r.Height)

	// Caret offsets for hit testing and drawing.
	runes := f.Edit.Runes()
	disp := textedit.Display(runes)
	dr := []rune(disp)
	offsets := make([]float32, len(dr)+1)
	for i := 1; i <= len(dr); i++ {
		offsets[i] = c.Measure(kind, size, string(dr[:i])).X
	}

	if hover {
		c.SetCursor(rl.MouseCursorIBeam)
	}
	if hover && c.MousePressed {
		c.ClaimFocus(id)
		focused = true
		i := textedit.IndexAt(offsets, c.Mouse.X-inner.X+f.scroll)
		double := c.Now.Sub(c.lastClick) < 400*time.Millisecond
		c.lastClick = c.Now
		if double {
			f.Edit.SelectAll()
		} else {
			f.Edit.SetCursor(i, c.Shift)
			f.dragging = true
		}
	}
	if f.dragging {
		if c.MouseDown {
			f.Edit.SetCursor(textedit.IndexAt(offsets, c.Mouse.X-inner.X+f.scroll), true)
		} else {
			f.dragging = false
		}
	}

	if focused {
		before := f.Edit.String()
		res.Submitted = c.editKeys(f)
		res.Changed = f.Edit.String() != before
		if res.Changed {
			runes = f.Edit.Runes()
			disp = textedit.Display(runes)
			dr = []rune(disp)
			offsets = make([]float32, len(dr)+1)
			for i := 1; i <= len(dr); i++ {
				offsets[i] = c.Measure(kind, size, string(dr[:i])).X
			}
		}
	}

	// Draw.
	bg := ColPanel
	if f.Disabled {
		bg = ColBackground
	}
	rl.DrawRectangleRounded(r, 0.2, 6, bg)
	border := ColBorder
	if focused {
		border = ColAccent
	} else if hover {
		border = ColMuted
	}
	thick := float32(1)
	if focused {
		thick = 2
	}
	rl.DrawRectangleRoundedLinesEx(r, 0.2, 6, thick, border)

	textW := offsets[len(offsets)-1]
	if focused {
		f.scroll = textedit.ScrollFor(f.scroll, offsets[f.Edit.Cursor()], textW, inner.Width)
	} else if !f.dragging {
		f.scroll = 0
	}
	c.PushClip(inner)
	m := c.Measure(kind, size, "Ag")
	ty := r.Y + (r.Height-m.Y)/2
	if focused && f.Edit.HasSelection() {
		s, e := f.Edit.Selection()
		x0, x1 := offsets[s]-f.scroll, offsets[e]-f.scroll
		rl.DrawRectangleRec(rl.NewRectangle(inner.X+x0, ty, x1-x0, m.Y), ColSelection)
	}
	if len(dr) == 0 && f.Placeholder != "" {
		c.Text(Regular, size, f.Placeholder, inner.X, ty, ColMuted)
	} else {
		col := ColText
		if f.Disabled {
			col = ColMuted
		}
		c.Text(kind, size, disp, inner.X-f.scroll, ty, col)
	}
	if focused {
		x := inner.X + offsets[f.Edit.Cursor()] - f.scroll
		rl.DrawRectangleRec(rl.NewRectangle(snap(x, c.scale), ty, 1.5, m.Y), ColText)
	}
	c.PopClip()
	return res
}

func keyHit(k int32) bool { return rl.IsKeyPressed(k) || rl.IsKeyPressedRepeat(k) }

// editKeys applies this frame's keys and characters to a focused field and
// reports whether Enter was pressed.
func (c *Context) editKeys(f *Field) bool {
	e := &f.Edit
	word := rl.IsKeyDown(rl.KeyLeftControl) || rl.IsKeyDown(rl.KeyRightControl) ||
		rl.IsKeyDown(rl.KeyLeftAlt) || rl.IsKeyDown(rl.KeyRightAlt)
	if c.Ctrl {
		switch {
		case rl.IsKeyPressed(rl.KeyA):
			e.SelectAll()
		case rl.IsKeyPressed(rl.KeyC):
			if e.HasSelection() {
				rl.SetClipboardText(e.SelectedText())
			}
		case rl.IsKeyPressed(rl.KeyX):
			if e.HasSelection() {
				rl.SetClipboardText(e.Cut())
			}
		case keyHit(rl.KeyV):
			e.Insert(rl.GetClipboardText())
		}
	} else {
		for _, r := range c.Chars {
			if r >= 32 && r != 127 {
				e.Insert(string(r))
			}
		}
	}
	switch {
	case keyHit(rl.KeyBackspace):
		e.Backspace(word)
	case keyHit(rl.KeyDelete):
		e.Delete(word)
	case keyHit(rl.KeyLeft):
		if word {
			e.MoveWord(-1, c.Shift)
		} else {
			e.Move(-1, c.Shift)
		}
	case keyHit(rl.KeyRight):
		if word {
			e.MoveWord(1, c.Shift)
		} else {
			e.Move(1, c.Shift)
		}
	case keyHit(rl.KeyHome):
		e.Home(c.Shift)
	case keyHit(rl.KeyEnd):
		e.End(c.Shift)
	case rl.IsKeyPressed(rl.KeyInsert) && c.Shift:
		e.Insert(rl.GetClipboardText())
	}
	return rl.IsKeyPressed(rl.KeyEnter) || rl.IsKeyPressed(rl.KeyKpEnter)
}

// SegmentedWidth returns the width Segmented needs for labels.
func (c *Context) SegmentedWidth(labels []string) float32 {
	w := float32(0)
	for _, l := range labels {
		w += c.Measure(Regular, SizeSmall, l).X + 20
	}
	return w
}

// Segmented draws a row of joined toggle buttons at x, y with height h, the
// selected one highlighted (-1 for none). It returns the index of the
// segment clicked this frame, or -1, and the index under the mouse, or -1.
func (c *Context) Segmented(id string, x, y, h float32, labels []string, selected int) (clicked, hovered int) {
	clicked, hovered = -1, -1
	total := rl.NewRectangle(x, y, c.SegmentedWidth(labels), h)
	rl.DrawRectangleRounded(total, 0.25, 6, ColPanel)
	for i, l := range labels {
		w := c.Measure(Regular, SizeSmall, l).X + 20
		r := rl.NewRectangle(x, y, w, h)
		sid := id + "/" + l
		fg := ColText
		switch {
		case i == selected:
			rl.DrawRectangleRounded(shrink(r, 2), 0.3, 6, ColAccentSoft)
			fg = ColAccentDark
		case c.Pressed(sid, r):
			rl.DrawRectangleRounded(shrink(r, 2), 0.3, 6, ColPressed)
		case c.Hover(r):
			rl.DrawRectangleRounded(shrink(r, 2), 0.3, 6, ColHover)
		}
		if i > 0 && i != selected && i-1 != selected {
			rl.DrawLineV(rl.NewVector2(x, y+6), rl.NewVector2(x, y+h-6), ColBorder)
		}
		c.TextCentered(Regular, SizeSmall, l, r, fg)
		if c.Hover(r) {
			hovered = i
			c.SetCursor(rl.MouseCursorPointingHand)
		}
		if c.Clicked(sid, r) {
			clicked = i
		}
		x += w
	}
	rl.DrawRectangleRoundedLinesEx(total, 0.25, 6, 1, ColBorder)
	return clicked, hovered
}

func shrink(r rl.Rectangle, d float32) rl.Rectangle {
	return rl.NewRectangle(r.X+d, r.Y+d, r.Width-2*d, r.Height-2*d)
}
