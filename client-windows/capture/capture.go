// Package capture reads the local screen and cursor (PLAN.md 5.2). The
// Windows implementation uses GDI only: BitBlt from the screen DC into
// top-down 32-bit DIB sections whose memory is read directly, on one locked
// OS thread. Everything else in this file is platform neutral so the session
// code and the cursor conversions can be tested anywhere.
package capture

import (
	"errors"
	"hash/fnv"
	"image"
)

// ErrUnsupported is returned by New on systems without a capture backend.
var ErrUnsupported = errors.New("capture: not supported on this system")

// Monitor is one display in virtual-desktop pixels.
type Monitor struct {
	Rect    image.Rectangle
	Primary bool
	Name    string // device name such as DISPLAY1, stable while the display stays attached
}

// CursorInfo is the state of the mouse cursor.
type CursorInfo struct {
	Showing bool        // false while an application hides the cursor
	Pos     image.Point // virtual-desktop pixels (the hotspot)
	Handle  uintptr     // HCURSOR; 0 when there is no cursor
}

// Shape is a cursor image with straight (non-premultiplied) alpha.
type Shape struct {
	HotX, HotY int
	W, H       int
	RGBA       []byte // W*H*4
}

// MaxShape is the largest cursor size sent to the helper (screen.MaxCursor);
// bigger cursors are cut to this size.
const MaxShape = 256

// Screen is the local display: monitor layout, captures and the cursor.
// Implementations are safe for use from one goroutine at a time.
type Screen interface {
	// Monitors lists the attached monitors.
	Monitors() ([]Monitor, error)
	// Open starts capturing the area of m.
	Open(m Monitor) (Capturer, error)
	// Cursor returns the current cursor state.
	Cursor() (CursorInfo, error)
	// CursorShape extracts the image of a cursor handle from Cursor.
	CursorShape(handle uintptr) (Shape, error)
	// Close releases the screen and every Capturer still open.
	Close() error
}

// Capturer captures one monitor.
type Capturer interface {
	// Capture grabs the monitor into an image with bounds (0,0)-(W,H). The
	// pixels are BGRA (alpha undefined). Captures are double buffered: the
	// returned image stays valid until the second following call or Close.
	Capture() (*image.RGBA, error)
	// Close frees the buffers.
	Close() error
}

// DefaultMonitor picks the monitor to share at session start: the one under
// the cursor, else the primary one, else the first. It returns -1 for an
// empty list.
func DefaultMonitor(ms []Monitor, cursor *image.Point) int {
	if cursor != nil {
		for i, m := range ms {
			if cursor.In(m.Rect) {
				return i
			}
		}
	}
	for i, m := range ms {
		if m.Primary {
			return i
		}
	}
	if len(ms) > 0 {
		return 0
	}
	return -1
}

// SameMonitors reports whether two monitor lists are equal.
func SameMonitors(a, b []Monitor) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// FindMonitor looks up the monitor called name in ms; failing that it
// returns the index of the monitor with the same rectangle, then
// DefaultMonitor without cursor.
func FindMonitor(ms []Monitor, name string, r image.Rectangle) int {
	for i, m := range ms {
		if name != "" && m.Name == name {
			return i
		}
	}
	for i, m := range ms {
		if m.Rect == r {
			return i
		}
	}
	return DefaultMonitor(ms, nil)
}

// ShapeID derives a cursor ID from the shape's content, so identical shapes
// share an ID and the helper caches each image once.
func ShapeID(s Shape) uint32 {
	h := fnv.New32a()
	var hdr [16]byte
	for i, v := range []int{s.HotX, s.HotY, s.W, s.H} {
		hdr[i*4] = byte(v >> 24)
		hdr[i*4+1] = byte(v >> 16)
		hdr[i*4+2] = byte(v >> 8)
		hdr[i*4+3] = byte(v)
	}
	h.Write(hdr[:])
	h.Write(s.RGBA)
	id := h.Sum32()
	if id == 0 {
		id = 1 // 0 is never a valid shape
	}
	return id
}

// ShapeFromColor converts a color cursor to straight-alpha RGBA. color is
// the cursor's color bitmap and mask its AND mask, both read as top-down
// 32-bit BGRA rows of w pixels (a monochrome mask reads as black 0 or white
// 0xFFFFFF). If any color pixel has a non-zero alpha the cursor is an alpha
// cursor and its alpha channel is used as is; otherwise the mask decides:
// white means transparent, except where the color is not black, which on
// screen inverts the background and is drawn as opaque color here.
func ShapeFromColor(color, mask []byte, w, h int) []byte {
	n := w * h
	out := make([]byte, n*4)
	hasAlpha := false
	for i := 0; i < n; i++ {
		if color[i*4+3] != 0 {
			hasAlpha = true
			break
		}
	}
	for i := 0; i < n; i++ {
		b, g, r, a := color[i*4], color[i*4+1], color[i*4+2], color[i*4+3]
		if !hasAlpha {
			a = 255
			if mask != nil && mask[i*4] != 0 && (r|g|b) == 0 {
				a = 0
			}
		}
		out[i*4], out[i*4+1], out[i*4+2], out[i*4+3] = r, g, b, a
	}
	return out
}

// ShapeFromMonochrome converts a monochrome cursor to RGBA. mask holds the
// AND mask (top h rows) followed by the XOR mask (next h rows), read as
// 32-bit rows of w pixels. AND 0 draws the XOR color (black or white); AND 1
// with XOR 0 is transparent; AND 1 with XOR 1 inverts the background, which
// is drawn as black with a white outline so it shows on any background
// (this is how the text I-beam usually looks).
func ShapeFromMonochrome(mask []byte, w, h int) []byte {
	n := w * h
	out := make([]byte, n*4)
	inverted := make([]bool, n)
	for i := 0; i < n; i++ {
		and := mask[i*4] != 0
		xor := mask[(n+i)*4] != 0
		switch {
		case !and && !xor:
			out[i*4+3] = 255
		case !and && xor:
			out[i*4], out[i*4+1], out[i*4+2], out[i*4+3] = 255, 255, 255, 255
		case and && xor:
			out[i*4+3] = 255
			inverted[i] = true
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if !inverted[i] {
				continue
			}
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					nx, ny := x+dx, y+dy
					if nx < 0 || ny < 0 || nx >= w || ny >= h {
						continue
					}
					j := ny*w + nx
					if out[j*4+3] == 0 {
						out[j*4], out[j*4+1], out[j*4+2], out[j*4+3] = 255, 255, 255, 255
					}
				}
			}
		}
	}
	return out
}

// ClipShape cuts s to at most MaxShape x MaxShape pixels and keeps the
// hotspot inside the shape.
func ClipShape(s Shape) Shape {
	if s.W <= MaxShape && s.H <= MaxShape {
		s.HotX, s.HotY = max(0, min(s.HotX, s.W-1)), max(0, min(s.HotY, s.H-1))
		return s
	}
	w, h := min(s.W, MaxShape), min(s.H, MaxShape)
	out := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		copy(out[y*w*4:(y+1)*w*4], s.RGBA[y*s.W*4:])
	}
	s.W, s.H, s.RGBA = w, h, out
	s.HotX, s.HotY = min(s.HotX, w-1), min(s.HotY, h-1)
	return s
}

// SwapRB converts BGRA to RGBA (or back) in place and sets alpha to 255. It
// is used for recordings and checks, not on the session path, where the
// encoder swaps only the pixels it sends.
func SwapRB(pix []byte) {
	for i := 0; i+3 < len(pix); i += 4 {
		pix[i], pix[i+2], pix[i+3] = pix[i+2], pix[i], 255
	}
}

// Arrow returns a plain arrow cursor, used when the real shape can't be
// read (for example a cursor of another process on some systems).
func Arrow() Shape {
	const w, h = 12, 19
	pix := make([]byte, w*h*4)
	set := func(x, y int, v byte) {
		i := (y*w + x) * 4
		pix[i], pix[i+1], pix[i+2], pix[i+3] = v, v, v, 255
	}
	for y := 0; y < 16; y++ {
		// The arrow's right edge runs at 45 degrees down to row 11, then
		// back in towards the stem.
		right := min(y, 11)
		if y > 11 {
			right = 11 - (y-11)*2
		}
		for x := 0; x <= right; x++ {
			v := byte(0)
			if x == 0 || x == right || y == 11 && x > 5 {
				v = 255
			}
			set(x, y, v)
		}
	}
	// The stem.
	for y := 12; y < h; y++ {
		x0 := 4 + (y-12)/2
		for x := x0; x < x0+3 && x < w; x++ {
			v := byte(0)
			if x == x0 || x == x0+2 || y == h-1 {
				v = 255
			}
			set(x, y, v)
		}
	}
	return Shape{W: w, H: h, RGBA: pix}
}
