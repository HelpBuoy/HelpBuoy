package ui

import "image"

// Host is the platform side of the window: the X11 window on Linux (xwin)
// and the AppKit window of the macOS client. The Window draws everything
// itself into an RGBA buffer and only needs a host to show it and to deliver
// input. All methods are called from the Window's goroutine; a host that
// needs its own thread (AppKit's main thread) hands the work over itself and
// must not block on it, except in Open.
type Host interface {
	// Open creates the window, not shown yet, centered on the main display,
	// with a client area of w by h 96-DPI units. It returns the number of
	// pixels per unit, at least 1.
	Open(title string, w, h int, icons [][]byte) (scale float64, err error)
	// Events delivers the window's input. It is closed when the window is
	// gone for good (the display connection was lost).
	Events() <-chan Event
	// Show shows the window.
	Show()
	// Put shows img, the whole client area in pixels.
	Put(img *image.RGBA)
	SetTitle(title string)
	Minimize()
	// Activate shows, raises and focuses the window.
	Activate()
	// DemandAttention flashes the taskbar entry or bounces the Dock icon.
	DemandAttention(on bool)
	SetCursor(c Cursor)
	// Copy puts text on the clipboard and reports whether that worked.
	Copy(text string) bool
	// RequestPaste asks for the clipboard's text, which arrives as a
	// PasteEvent.
	RequestPaste()
	// Close destroys the window.
	Close()
}

// Cursor is a pointer shape.
type Cursor int

const (
	CursorArrow Cursor = iota
	CursorText
	CursorHand
)

// Event is one of the *Event types below.
type Event any

// ExposeEvent asks for the client area to be shown again.
type ExposeEvent struct{}

// FocusEvent tells that the window got (In) or lost the keyboard focus.
type FocusEvent struct{ In bool }

// CloseEvent is the user closing the window (or quitting the app).
type CloseEvent struct{}

// PasteEvent carries the clipboard text that RequestPaste asked for.
type PasteEvent struct{ Text string }

// MouseKind says what a MouseEvent is.
type MouseKind int

const (
	MouseDown MouseKind = iota // left button pressed
	MouseUp                    // left button released
	MouseMove
	MouseLeave // the pointer left the window
)

// MouseEvent is the pointer in client area pixels. Only the left button is
// reported.
type MouseEvent struct {
	Kind MouseKind
	X, Y int
	Drag bool // the left button is held (MouseMove)
}

// Key is a key that edits or moves rather than types.
type Key int

const (
	KeyNone Key = iota // a key that types Text, or nothing the window uses
	KeyEnter
	KeyEscape
	KeyTab
	KeyBackTab // Shift+Tab
	KeyBackspace
	KeyDelete
	KeyLeft
	KeyRight
	KeyHome
	KeyEnd
	KeyInsert
)

// KeyEvent is a key press. The modifiers are named for what they do, since
// that differs per platform.
type KeyEvent struct {
	Key Key
	// Char is the key's character without modifiers, lowercase, for
	// shortcuts and the space bar; 0 for none.
	Char rune
	// Text is what the key types, "" when it types nothing or the
	// modifiers make it a shortcut.
	Text string
	// Cmd is the shortcut modifier: Ctrl on Linux, Command on macOS.
	Cmd bool
	// Word moves the caret by words: Ctrl on Linux, Option on macOS.
	Word  bool
	Shift bool
	// Repeat is auto-repeat of a held key.
	Repeat bool
}
