// Package textedit is the editing logic of the single-line text field: text,
// cursor and selection, independent of raylib so it can be tested directly.
//
// Positions are rune indexes from 0 to Len(). The selection runs between the
// anchor and the cursor; it is empty when they are equal.
package textedit

import (
	"strings"
	"unicode"
)

// State is the content and caret of one text field.
type State struct {
	text   []rune
	cursor int
	anchor int
	// Max limits the number of runes; 0 means no limit.
	Max int
}

// New returns a State holding s with the cursor at the end.
func New(s string) *State {
	e := &State{}
	e.SetText(s)
	return e
}

// String returns the text.
func (e *State) String() string { return string(e.text) }

// Runes returns the text as runes. The slice must not be modified.
func (e *State) Runes() []rune { return e.text }

// Len returns the number of runes.
func (e *State) Len() int { return len(e.text) }

// SetText replaces the text and puts the cursor at the end.
func (e *State) SetText(s string) {
	e.text = []rune(s)
	if e.Max > 0 && len(e.text) > e.Max {
		e.text = e.text[:e.Max]
	}
	e.cursor = len(e.text)
	e.anchor = e.cursor
}

// Cursor returns the cursor position.
func (e *State) Cursor() int { return e.cursor }

// Selection returns the selected range [start, end).
func (e *State) Selection() (start, end int) {
	if e.anchor < e.cursor {
		return e.anchor, e.cursor
	}
	return e.cursor, e.anchor
}

// HasSelection reports whether some text is selected.
func (e *State) HasSelection() bool { return e.anchor != e.cursor }

// SelectedText returns the selected text.
func (e *State) SelectedText() string {
	s, t := e.Selection()
	return string(e.text[s:t])
}

// SelectAll selects the whole text.
func (e *State) SelectAll() {
	e.anchor = 0
	e.cursor = len(e.text)
}

// SetCursor moves the cursor to i (clamped). With extend the anchor stays,
// which extends or shrinks the selection.
func (e *State) SetCursor(i int, extend bool) {
	e.cursor = clamp(i, 0, len(e.text))
	if !extend {
		e.anchor = e.cursor
	}
}

// Move moves the cursor by delta runes. Without extend, a selection collapses
// to its start or end in the direction of the move instead.
func (e *State) Move(delta int, extend bool) {
	if !extend && e.HasSelection() {
		s, t := e.Selection()
		if delta < 0 {
			e.SetCursor(s, false)
		} else {
			e.SetCursor(t, false)
		}
		return
	}
	e.SetCursor(e.cursor+delta, extend)
}

// MoveWord moves the cursor to the previous (dir < 0) or next word boundary.
func (e *State) MoveWord(dir int, extend bool) {
	e.SetCursor(e.wordBoundary(e.cursor, dir), extend)
}

// Home moves the cursor to the start.
func (e *State) Home(extend bool) { e.SetCursor(0, extend) }

// End moves the cursor to the end.
func (e *State) End(extend bool) { e.SetCursor(len(e.text), extend) }

// Insert replaces the selection with s, dropping NUL characters and
// respecting Max. The text is kept as given, line breaks included, so pasted
// key lines reach the parser unchanged.
func (e *State) Insert(s string) {
	s = strings.ReplaceAll(s, "\x00", "")
	e.deleteSelection()
	ins := []rune(s)
	if e.Max > 0 {
		room := e.Max - len(e.text)
		if room < 0 {
			room = 0
		}
		if len(ins) > room {
			ins = ins[:room]
		}
	}
	if len(ins) == 0 {
		return
	}
	t := make([]rune, 0, len(e.text)+len(ins))
	t = append(t, e.text[:e.cursor]...)
	t = append(t, ins...)
	t = append(t, e.text[e.cursor:]...)
	e.text = t
	e.cursor += len(ins)
	e.anchor = e.cursor
}

// Backspace deletes the selection, or the rune (or word) before the cursor.
func (e *State) Backspace(word bool) {
	if e.deleteSelection() {
		return
	}
	from := e.cursor - 1
	if word {
		from = e.wordBoundary(e.cursor, -1)
	}
	e.deleteRange(from, e.cursor)
}

// Delete deletes the selection, or the rune (or word) after the cursor.
func (e *State) Delete(word bool) {
	if e.deleteSelection() {
		return
	}
	to := e.cursor + 1
	if word {
		to = e.wordBoundary(e.cursor, 1)
	}
	e.deleteRange(e.cursor, to)
}

// Cut removes the selected text and returns it.
func (e *State) Cut() string {
	s := e.SelectedText()
	e.deleteSelection()
	return s
}

func (e *State) deleteSelection() bool {
	if !e.HasSelection() {
		return false
	}
	s, t := e.Selection()
	e.deleteRange(s, t)
	return true
}

func (e *State) deleteRange(from, to int) {
	from = clamp(from, 0, len(e.text))
	to = clamp(to, 0, len(e.text))
	if from >= to {
		return
	}
	e.text = append(e.text[:from:from], e.text[to:]...)
	e.cursor = from
	e.anchor = from
}

// wordBoundary finds the next word boundary from i in direction dir: it skips
// spaces, then a run of word characters or a run of other symbols.
func (e *State) wordBoundary(i, dir int) int {
	t := e.text
	if dir < 0 {
		for i > 0 && unicode.IsSpace(t[i-1]) {
			i--
		}
		if i > 0 {
			w := isWord(t[i-1])
			for i > 0 && !unicode.IsSpace(t[i-1]) && isWord(t[i-1]) == w {
				i--
			}
		}
		return i
	}
	for i < len(t) && unicode.IsSpace(t[i]) {
		i++
	}
	if i < len(t) {
		w := isWord(t[i])
		for i < len(t) && !unicode.IsSpace(t[i]) && isWord(t[i]) == w {
			i++
		}
	}
	return i
}

func isWord(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Display returns the text as the field draws it: line breaks, tabs and other
// control characters become spaces, so the rune count stays the same.
func Display(r []rune) string {
	var b strings.Builder
	for _, c := range r {
		if unicode.IsControl(c) || c == ' ' || c == '​' {
			c = ' '
		}
		b.WriteRune(c)
	}
	return b.String()
}

// IndexAt returns the caret position nearest to x, given the caret offsets
// offsets[i] (x before rune i, len(offsets) = Len()+1, ascending).
func IndexAt(offsets []float32, x float32) int {
	if len(offsets) == 0 {
		return 0
	}
	for i := 1; i < len(offsets); i++ {
		mid := (offsets[i-1] + offsets[i]) / 2
		if x < mid {
			return i - 1
		}
	}
	return len(offsets) - 1
}

// ScrollFor returns the horizontal scroll that keeps the caret at offset
// caretX visible in a field of the given width, starting from scroll.
func ScrollFor(scroll, caretX, textWidth, width float32) float32 {
	const margin = 8
	if caretX-scroll > width-margin {
		scroll = caretX - width + margin
	}
	if caretX-scroll < margin {
		scroll = caretX - margin
	}
	if maxScroll := textWidth - width + margin; scroll > maxScroll {
		scroll = maxScroll
	}
	if scroll < 0 {
		scroll = 0
	}
	return scroll
}
