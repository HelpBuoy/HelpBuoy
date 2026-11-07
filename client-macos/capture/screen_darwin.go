package capture

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework AppKit -framework CoreGraphics -framework CoreMedia -framework CoreVideo -framework Metal -framework ScreenCaptureKit
#include <stdlib.h>
#include "sck.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"image"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	wcapture "github.com/helpbuoy/helpbuoy/client-windows/capture"
)

// Screen is the local display set.
type Screen struct {
	log *slog.Logger

	mu     sync.Mutex
	open   map[*Capturer]struct{}
	shape  wcapture.Shape // the cursor image of handle
	handle uintptr
	buf    []byte
}

// New returns the screen. It does not check the Screen Recording
// permission; without it Open fails.
func New(log *slog.Logger) (*Screen, error) {
	if log == nil {
		log = slog.Default()
	}
	return &Screen{log: log, open: make(map[*Capturer]struct{}), buf: make([]byte, wcapture.MaxShape*wcapture.MaxShape*4)}, nil
}

// namePrefix starts every monitor name; the display ID follows.
const namePrefix = "display-"

// Monitors implements capture.Screen.
func (s *Screen) Monitors() ([]wcapture.Monitor, error) {
	var ds [32]C.hb_display
	n := int(C.hb_displays(&ds[0], C.int(len(ds))))
	if n < 0 {
		return nil, errors.New("capture: the displays could not be listed")
	}
	ms := make([]wcapture.Monitor, 0, n)
	for _, d := range ds[:n] {
		x, y := int(d.x), int(d.y)
		ms = append(ms, wcapture.Monitor{
			Rect:    image.Rect(x, y, x+int(d.w), y+int(d.h)),
			Primary: d.primary != 0,
			Name:    namePrefix + strconv.FormatUint(uint64(d.id), 10),
		})
	}
	return ms, nil
}

// Open implements capture.Screen: it starts a stream of m.
func (s *Screen) Open(m wcapture.Monitor) (wcapture.Capturer, error) {
	id, err := strconv.ParseUint(strings.TrimPrefix(m.Name, namePrefix), 10, 32)
	if err != nil {
		return nil, fmt.Errorf("capture: unknown monitor %q", m.Name)
	}
	w, h := m.Rect.Dx(), m.Rect.Dy()
	if w <= 0 || h <= 0 {
		return nil, errors.New("capture: empty monitor")
	}
	cp := &Capturer{s: s, id: uint32(id), w: w, h: h}
	if backend() == "cg" {
		cp.cg = true
	} else {
		var errBuf [256]C.char
		cp.st = C.hb_stream_start(C.uint32_t(id), C.int(w), C.int(h), &errBuf[0], C.int(len(errBuf)))
		if cp.st == nil {
			return nil, fmt.Errorf("capture: %s", C.GoString(&errBuf[0]))
		}
	}
	for i := range cp.bufs {
		cp.bufs[i] = image.NewRGBA(image.Rect(0, 0, w, h))
	}
	s.mu.Lock()
	s.open[cp] = struct{}{}
	s.mu.Unlock()
	if cp.cg {
		s.log.Info("CoreGraphics capture", "display", id, "size", image.Pt(w, h), "metal", hasMetal())
	} else {
		s.log.Info("ScreenCaptureKit stream", "display", id, "size", image.Pt(w, h))
	}
	return cp, nil
}

// Cursor implements capture.Screen. The handle is derived from the cursor
// image, since macOS has no cursor handles to compare; CursorShape returns
// the image Cursor read last.
func (s *Screen) Cursor() (wcapture.CursorInfo, error) {
	var x, y C.double
	if C.hb_cursor_pos(&x, &y) != 0 {
		return wcapture.CursorInfo{}, errors.New("capture: no cursor position")
	}
	ci := wcapture.CursorInfo{Showing: true, Pos: image.Pt(int(x), int(y))}
	s.mu.Lock()
	defer s.mu.Unlock()
	var w, h, hx, hy C.int
	if C.hb_cursor_shape(1, (*C.uint8_t)(unsafe.Pointer(&s.buf[0])), C.int(len(s.buf)), &w, &h, &hx, &hy) != 0 {
		ci.Handle = s.handle // keep the last shape
		return ci, nil
	}
	pix := make([]byte, int(w)*int(h)*4)
	copy(pix, s.buf)
	unpremultiply(pix)
	sh := wcapture.ClipShape(wcapture.Shape{HotX: int(hx), HotY: int(hy), W: int(w), H: int(h), RGBA: pix})
	s.handle = uintptr(wcapture.ShapeID(sh))
	s.shape = sh
	ci.Handle = s.handle
	return ci, nil
}

// CursorShape implements capture.Screen.
func (s *Screen) CursorShape(handle uintptr) (wcapture.Shape, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if handle == 0 || handle != s.handle {
		return wcapture.Shape{}, errors.New("capture: cursor shape changed")
	}
	return s.shape, nil
}

var (
	metalOnce sync.Once
	metal     bool
)

// hasMetal reports whether the Mac has a Metal device, asked once.
func hasMetal() bool {
	metalOnce.Do(func() { metal = C.hb_has_metal() != 0 })
	return metal
}

// Close implements capture.Screen.
func (s *Screen) Close() error {
	s.mu.Lock()
	open := s.open
	s.open = map[*Capturer]struct{}{}
	s.mu.Unlock()
	for cp := range open {
		cp.Close()
	}
	return nil
}

// Capturer captures one display, from a stream or with CoreGraphics.
type Capturer struct {
	s      *Screen
	st     *C.hb_stream // nil with cg
	cg     bool         // CGDisplayCreateImage on every Capture
	id     uint32
	closed bool
	w, h   int
	seq    C.uint64_t
	bufs   [2]*image.RGBA
	last   int // index of the buffer returned last, -1 before the first frame
	got    bool
}

// firstFrame is how long Capture waits for the stream's first frame.
const firstFrame = 2 * time.Second

// Capture implements capture.Capturer. ScreenCaptureKit only delivers
// changed frames, so when nothing changed it returns the previous image
// again; the other buffer is filled with the next new frame, which keeps an
// image valid until the second following call.
func (cp *Capturer) Capture() (*image.RGBA, error) {
	if cp.closed {
		return nil, errors.New("capture: closed")
	}
	if cp.cg {
		next := 0
		if cp.got {
			next = 1 - cp.last
		}
		img := cp.bufs[next]
		var errBuf [256]C.char
		if C.hb_cg_copy(C.uint32_t(cp.id), (*C.uint8_t)(unsafe.Pointer(&img.Pix[0])), C.int(img.Stride),
			C.int(cp.w), C.int(cp.h), &errBuf[0], C.int(len(errBuf))) < 0 {
			return nil, fmt.Errorf("capture: %s", C.GoString(&errBuf[0]))
		}
		cp.last, cp.got = next, true
		return img, nil
	}
	deadline := time.Now().Add(firstFrame)
	for {
		next := 0
		if cp.got {
			next = 1 - cp.last
		}
		img := cp.bufs[next]
		var errBuf [256]C.char
		r := C.hb_stream_copy(cp.st, (*C.uint8_t)(unsafe.Pointer(&img.Pix[0])), C.int(img.Stride),
			C.int(cp.w), C.int(cp.h), &cp.seq, &errBuf[0], C.int(len(errBuf)))
		switch {
		case r < 0:
			return nil, fmt.Errorf("capture: %s", C.GoString(&errBuf[0]))
		case r > 0:
			cp.last, cp.got = next, true
			return img, nil
		case cp.got:
			return cp.bufs[cp.last], nil
		case time.Now().After(deadline):
			return nil, errors.New("capture: no frame from the screen")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Close implements capture.Capturer.
func (cp *Capturer) Close() error {
	if cp.closed {
		return nil
	}
	cp.closed = true
	if cp.st != nil {
		C.hb_stream_stop(cp.st)
		cp.st = nil
	}
	cp.s.mu.Lock()
	delete(cp.s.open, cp)
	cp.s.mu.Unlock()
	return nil
}
