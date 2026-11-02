//go:build windows

package capture

import (
	"errors"
	"fmt"
	"image"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")

	procGetDC               = user32.NewProc("GetDC")
	procReleaseDC           = user32.NewProc("ReleaseDC")
	procEnumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW     = user32.NewProc("GetMonitorInfoW")
	procGetCursorInfo       = user32.NewProc("GetCursorInfo")
	procGetIconInfo         = user32.NewProc("GetIconInfo")

	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procDeleteDC           = gdi32.NewProc("DeleteDC")
	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	procSelectObject       = gdi32.NewProc("SelectObject")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procBitBlt             = gdi32.NewProc("BitBlt")
	procGdiFlush           = gdi32.NewProc("GdiFlush")
	procGetDIBits          = gdi32.NewProc("GetDIBits")
	procGetObjectW         = gdi32.NewProc("GetObjectW")
)

const (
	srccopy            = 0x00CC0020
	captureblt         = 0x40000000
	biRGB              = 0
	dibRGBColors       = 0
	monitorinfoPrimary = 1
	cursorShowing      = 1
)

type rect struct{ Left, Top, Right, Bottom int32 }

type point struct{ X, Y int32 }

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

// bitmapInfo is BITMAPINFO with room for a color table GDI might write.
type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [256]uint32
}

type monitorInfoEx struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
	Device  [32]uint16
}

type cursorInfo struct {
	Size   uint32
	Flags  uint32
	Cursor uintptr
	Pos    point
}

type iconInfo struct {
	Icon    int32
	HotX    uint32
	HotY    uint32
	BmMask  uintptr
	BmColor uintptr
}

type bitmap struct {
	Type       int32
	Width      int32
	Height     int32
	WidthBytes int32
	Planes     uint16
	BitsPixel  uint16
	Bits       uintptr
}

// Options tune the GDI capture.
type Options struct {
	// CaptureBlt adds CAPTUREBLT to BitBlt so layered windows are included,
	// at the cost of cursor flicker and speed. Off by default.
	CaptureBlt bool
}

// gdiScreen runs every GDI call on one goroutine locked to its OS thread.
type gdiScreen struct {
	opts  Options
	calls chan func()
	quit  chan struct{}
	once  sync.Once

	open map[*gdiCapturer]struct{} // only touched on the GDI thread
}

// New starts the GDI capture thread.
func New(opts Options) (Screen, error) {
	s := &gdiScreen{
		opts:  opts,
		calls: make(chan func()),
		quit:  make(chan struct{}),
		open:  make(map[*gdiCapturer]struct{}),
	}
	ready := make(chan struct{})
	go s.loop(ready)
	<-ready
	return s, nil
}

func (s *gdiScreen) loop(ready chan<- struct{}) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	close(ready)
	for {
		select {
		case f := <-s.calls:
			f()
		case <-s.quit:
			for c := range s.open {
				c.free()
			}
			return
		}
	}
}

var errClosed = errors.New("capture: screen closed")

// do runs f on the GDI thread and waits for it.
func (s *gdiScreen) do(f func()) error {
	done := make(chan struct{})
	select {
	case s.calls <- func() { f(); close(done) }:
	case <-s.quit:
		return errClosed
	}
	<-done
	return nil
}

func (s *gdiScreen) Close() error {
	s.once.Do(func() { close(s.quit) })
	return nil
}

// enumMonitors is shared by all screens: callbacks created with
// windows.NewCallback are never freed, so there is only one.
var (
	enumMu      sync.Mutex
	enumHandles []uintptr
	enumCB      = windows.NewCallback(func(hmon, hdc, rc, lparam uintptr) uintptr {
		enumHandles = append(enumHandles, hmon)
		return 1
	})
)

func (s *gdiScreen) Monitors() ([]Monitor, error) {
	var (
		ms  []Monitor
		err error
	)
	if e := s.do(func() { ms, err = monitors() }); e != nil {
		return nil, e
	}
	return ms, err
}

func monitors() ([]Monitor, error) {
	enumMu.Lock()
	enumHandles = enumHandles[:0]
	r, _, e := procEnumDisplayMonitors.Call(0, 0, enumCB, 0)
	handles := append([]uintptr(nil), enumHandles...)
	enumMu.Unlock()
	if r == 0 {
		return nil, fmt.Errorf("capture: EnumDisplayMonitors: %w", e)
	}
	var ms []Monitor
	for _, h := range handles {
		mi := monitorInfoEx{Size: uint32(unsafe.Sizeof(monitorInfoEx{}))}
		if r, _, _ := procGetMonitorInfoW.Call(h, uintptr(unsafe.Pointer(&mi))); r == 0 {
			continue
		}
		name := windows.UTF16ToString(mi.Device[:])
		name = strings.TrimPrefix(name, `\\.\`)
		ms = append(ms, Monitor{
			Rect:    image.Rect(int(mi.Monitor.Left), int(mi.Monitor.Top), int(mi.Monitor.Right), int(mi.Monitor.Bottom)),
			Primary: mi.Flags&monitorinfoPrimary != 0,
			Name:    name,
		})
	}
	if len(ms) == 0 {
		return nil, errors.New("capture: no monitors")
	}
	return ms, nil
}

func (s *gdiScreen) Cursor() (CursorInfo, error) {
	var (
		ci  CursorInfo
		err error
	)
	if e := s.do(func() {
		c := cursorInfo{Size: uint32(unsafe.Sizeof(cursorInfo{}))}
		r, _, e := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&c)))
		if r == 0 {
			err = fmt.Errorf("capture: GetCursorInfo: %w", e)
			return
		}
		ci = CursorInfo{
			Showing: c.Flags&cursorShowing != 0,
			Pos:     image.Pt(int(c.Pos.X), int(c.Pos.Y)),
			Handle:  c.Cursor,
		}
	}); e != nil {
		return CursorInfo{}, e
	}
	return ci, err
}

func (s *gdiScreen) CursorShape(handle uintptr) (Shape, error) {
	var (
		sh  Shape
		err error
	)
	if e := s.do(func() { sh, err = cursorShape(handle) }); e != nil {
		return Shape{}, e
	}
	return sh, err
}

func cursorShape(handle uintptr) (Shape, error) {
	var ii iconInfo
	if r, _, e := procGetIconInfo.Call(handle, uintptr(unsafe.Pointer(&ii))); r == 0 {
		return Shape{}, fmt.Errorf("capture: GetIconInfo: %w", e)
	}
	if ii.BmMask != 0 {
		defer procDeleteObject.Call(ii.BmMask)
	}
	if ii.BmColor != 0 {
		defer procDeleteObject.Call(ii.BmColor)
	}
	if ii.BmMask == 0 {
		return Shape{}, errors.New("capture: cursor without mask")
	}
	mw, mh, err := bitmapSize(ii.BmMask)
	if err != nil {
		return Shape{}, err
	}
	hdc, _, _ := procGetDC.Call(0)
	if hdc == 0 {
		return Shape{}, errors.New("capture: GetDC failed")
	}
	defer procReleaseDC.Call(0, hdc)

	sh := Shape{HotX: int(ii.HotX), HotY: int(ii.HotY)}
	if ii.BmColor != 0 {
		w, h, err := bitmapSize(ii.BmColor)
		if err != nil {
			return Shape{}, err
		}
		color, err := readBitmap(hdc, ii.BmColor, w, h)
		if err != nil {
			return Shape{}, err
		}
		var mask []byte
		if mw == w && mh >= h {
			mask, _ = readBitmap(hdc, ii.BmMask, w, h)
		}
		sh.W, sh.H = w, h
		sh.RGBA = ShapeFromColor(color, mask, w, h)
	} else {
		w, h := mw, mh/2
		if h <= 0 {
			return Shape{}, errors.New("capture: empty monochrome cursor")
		}
		mask, err := readBitmap(hdc, ii.BmMask, w, 2*h)
		if err != nil {
			return Shape{}, err
		}
		sh.W, sh.H = w, h
		sh.RGBA = ShapeFromMonochrome(mask, w, h)
	}
	return ClipShape(sh), nil
}

func bitmapSize(hbm uintptr) (w, h int, err error) {
	var bm bitmap
	if r, _, _ := procGetObjectW.Call(hbm, unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm))); r == 0 {
		return 0, 0, errors.New("capture: GetObject failed")
	}
	if bm.Width <= 0 || bm.Height <= 0 || bm.Width > 1024 || bm.Height > 2048 {
		return 0, 0, fmt.Errorf("capture: cursor bitmap %dx%d", bm.Width, bm.Height)
	}
	return int(bm.Width), int(bm.Height), nil
}

// readBitmap reads the top h rows of hbm as top-down 32-bit BGRA.
func readBitmap(hdc, hbm uintptr, w, h int) ([]byte, error) {
	bi := bitmapInfo{Header: bitmapInfoHeader{
		Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32, Compression: biRGB,
	}}
	bi.Header.Size = uint32(unsafe.Sizeof(bi.Header))
	buf := make([]byte, w*h*4)
	r, _, _ := procGetDIBits.Call(hdc, hbm, 0, uintptr(h), uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&bi)), dibRGBColors)
	if r == 0 {
		return nil, errors.New("capture: GetDIBits failed")
	}
	return buf, nil
}

// dib is one DIB section selected into its own memory DC.
type dib struct {
	dc, hbm, old uintptr
	img          *image.RGBA
}

type gdiCapturer struct {
	s      *gdiScreen
	rect   image.Rectangle
	rop    uintptr
	screen uintptr // screen DC
	bufs   [2]dib
	next   int
	freed  bool
}

func (s *gdiScreen) Open(m Monitor) (Capturer, error) {
	if m.Rect.Empty() {
		return nil, errors.New("capture: empty monitor")
	}
	c := &gdiCapturer{s: s, rect: m.Rect, rop: srccopy}
	if s.opts.CaptureBlt {
		c.rop |= captureblt
	}
	var err error
	if e := s.do(func() {
		if err = c.alloc(); err != nil {
			c.free()
			return
		}
		s.open[c] = struct{}{}
	}); e != nil {
		return nil, e
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (c *gdiCapturer) alloc() error {
	w, h := c.rect.Dx(), c.rect.Dy()
	var err error
	c.screen, _, err = procGetDC.Call(0)
	if c.screen == 0 {
		return fmt.Errorf("capture: GetDC: %w", err)
	}
	for i := range c.bufs {
		b := &c.bufs[i]
		b.dc, _, err = procCreateCompatibleDC.Call(c.screen)
		if b.dc == 0 {
			return fmt.Errorf("capture: CreateCompatibleDC: %w", err)
		}
		bi := bitmapInfo{Header: bitmapInfoHeader{
			Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32, Compression: biRGB,
		}}
		bi.Header.Size = uint32(unsafe.Sizeof(bi.Header))
		var bits unsafe.Pointer
		b.hbm, _, err = procCreateDIBSection.Call(b.dc, uintptr(unsafe.Pointer(&bi)), dibRGBColors,
			uintptr(unsafe.Pointer(&bits)), 0, 0)
		if b.hbm == 0 || bits == nil {
			b.hbm = 0
			return fmt.Errorf("capture: CreateDIBSection %dx%d: %w", w, h, err)
		}
		b.old, _, _ = procSelectObject.Call(b.dc, b.hbm)
		b.img = &image.RGBA{
			Pix:    unsafe.Slice((*byte)(bits), w*h*4),
			Stride: w * 4,
			Rect:   image.Rect(0, 0, w, h),
		}
	}
	return nil
}

// free releases the GDI objects; it runs on the GDI thread.
func (c *gdiCapturer) free() {
	if c.freed {
		return
	}
	c.freed = true
	delete(c.s.open, c)
	for i := range c.bufs {
		b := &c.bufs[i]
		if b.dc != 0 && b.old != 0 {
			procSelectObject.Call(b.dc, b.old)
		}
		if b.hbm != 0 {
			procDeleteObject.Call(b.hbm)
		}
		if b.dc != 0 {
			procDeleteDC.Call(b.dc)
		}
		b.img = nil
	}
	if c.screen != 0 {
		procReleaseDC.Call(0, c.screen)
	}
}

func (c *gdiCapturer) Capture() (*image.RGBA, error) {
	var (
		img *image.RGBA
		err error
	)
	if e := c.s.do(func() {
		if c.freed {
			err = errClosed
			return
		}
		b := &c.bufs[c.next]
		r, _, e := procBitBlt.Call(b.dc, 0, 0, uintptr(c.rect.Dx()), uintptr(c.rect.Dy()),
			c.screen, uintptr(c.rect.Min.X), uintptr(c.rect.Min.Y), c.rop)
		procGdiFlush.Call()
		if r == 0 {
			err = fmt.Errorf("capture: BitBlt: %w", e)
			return
		}
		img = b.img
		c.next = 1 - c.next
	}); e != nil {
		return nil, e
	}
	return img, err
}

func (c *gdiCapturer) Close() error {
	c.s.do(c.free)
	return nil
}
