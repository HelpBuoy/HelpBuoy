package macwin

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework AppKit -framework QuartzCore
#include <stdlib.h>
#include "host.h"
*/
import "C"

import (
	"errors"
	"image"
	"runtime"
	"sync"
	"unsafe"

	"github.com/helpbuoy/helpbuoy/client-linux/ui"
)

func init() {
	// AppKit must run on the main thread, which the main goroutine starts
	// on; keep it there for Main.
	runtime.LockOSThread()
}

// events is where hbEvent delivers; one window per program.
var (
	eventsMu sync.Mutex
	events   chan ui.Event
)

//export hbEvent
func hbEvent(kind, a, b, c, d C.int, text *C.char) {
	var e ui.Event
	switch kind {
	case C.hbEvExpose:
		e = ui.ExposeEvent{}
	case C.hbEvFocus:
		e = ui.FocusEvent{In: a != 0}
	case C.hbEvClose:
		e = ui.CloseEvent{}
	case C.hbEvPaste:
		e = ui.PasteEvent{Text: C.GoString(text)}
	case C.hbEvKey:
		e = ui.KeyEvent{
			Key:    ui.Key(a),
			Char:   rune(c),
			Text:   C.GoString(text),
			Cmd:    b&C.hbKeyCmd != 0,
			Word:   b&C.hbKeyWord != 0,
			Shift:  b&C.hbKeyShift != 0,
			Repeat: b&C.hbKeyRepeat != 0,
		}
	case C.hbEvMouse:
		e = ui.MouseEvent{Kind: ui.MouseKind(a), X: int(b), Y: int(c), Drag: d != 0}
	default:
		return
	}
	eventsMu.Lock()
	ch := events
	eventsMu.Unlock()
	if ch == nil {
		return
	}
	// Never hold up the main thread: with the window's goroutine stuck or
	// gone, events are dropped.
	select {
	case ch <- e:
	default:
	}
}

// Init sets up the application: the menu with the given titles and, for a
// bare binary, the Dock icon (PNG). Call it from the main goroutine before
// Main.
func Init(hide, quit, minimize string, icon []byte) {
	ch, cq, cm := C.CString(hide), C.CString(quit), C.CString(minimize)
	defer C.free(unsafe.Pointer(ch))
	defer C.free(unsafe.Pointer(cq))
	defer C.free(unsafe.Pointer(cm))
	var p *C.uint8_t
	if len(icon) > 0 {
		p = (*C.uint8_t)(unsafe.Pointer(&icon[0]))
	}
	C.hb_app_init(ch, cq, cm, p, C.int(len(icon)))
}

// Main runs the AppKit loop on the main thread until Quit.
func Main() { C.hb_app_run() }

// Quit ends the program: AppKit exits the process. Call it once
// everything is cleaned up.
func Quit() { C.hb_app_quit() }

// ShowRunning brings the running copy with process ID pid to the front.
func ShowRunning(pid int) error {
	if pid <= 0 || C.hb_show_running(C.int(pid)) != 0 {
		return errors.New("the running copy could not be shown")
	}
	return nil
}

// Host is the ui.Host of the macOS window.
type Host struct {
	events chan ui.Event
}

// Open implements ui.Host. The window is w by h points; the scale is the
// main display's pixels per point.
func (h *Host) Open(title string, w, ht int, icons [][]byte) (float64, error) {
	h.events = make(chan ui.Event, 256)
	eventsMu.Lock()
	events = h.events
	eventsMu.Unlock()
	t := C.CString(title)
	defer C.free(unsafe.Pointer(t))
	var scale C.double
	if C.hb_win_open(t, C.int(w), C.int(ht), &scale) != 0 {
		return 0, errors.New("the window could not be created")
	}
	return float64(scale), nil
}

// Events implements ui.Host.
func (h *Host) Events() <-chan ui.Event { return h.events }

// Show implements ui.Host.
func (h *Host) Show() { C.hb_win_show() }

// Put implements ui.Host.
func (h *Host) Put(img *image.RGBA) {
	b := img.Rect
	if b.Empty() {
		return
	}
	C.hb_win_put((*C.uint8_t)(unsafe.Pointer(&img.Pix[0])), C.int(b.Dx()), C.int(b.Dy()), C.int(img.Stride))
}

// SetTitle implements ui.Host.
func (h *Host) SetTitle(title string) {
	t := C.CString(title)
	defer C.free(unsafe.Pointer(t))
	C.hb_win_title(t)
}

// Minimize implements ui.Host.
func (h *Host) Minimize() { C.hb_win_minimize() }

// Activate implements ui.Host.
func (h *Host) Activate() { C.hb_win_activate() }

// DemandAttention implements ui.Host: it bounces the Dock icon until the
// app is active.
func (h *Host) DemandAttention(on bool) {
	v := C.int(0)
	if on {
		v = 1
	}
	C.hb_win_attention(v)
}

// SetCursor implements ui.Host.
func (h *Host) SetCursor(c ui.Cursor) { C.hb_win_cursor(C.int(c)) }

// Copy implements ui.Host.
func (h *Host) Copy(text string) bool {
	t := C.CString(text)
	defer C.free(unsafe.Pointer(t))
	C.hb_clip_set(t)
	return true
}

// RequestPaste implements ui.Host.
func (h *Host) RequestPaste() { C.hb_clip_request() }

// Close implements ui.Host. Events are dropped from now on.
func (h *Host) Close() {
	eventsMu.Lock()
	events = nil
	eventsMu.Unlock()
	C.hb_win_close()
}
