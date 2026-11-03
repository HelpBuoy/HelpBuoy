// Package capture reads the local X11 screen and cursor (PLAN.md 5.2) with
// jezek/xgb, so it needs no C libraries. Monitors come from RandR 1.5 (the
// whole root window without it), pixels from MIT-SHM when the X server is
// local (plain GetImage otherwise, which also works over a remote display)
// and the cursor from XFixes. It implements the capture.Screen of the
// Windows client, so the session code is shared.
package capture

import (
	"errors"
	"fmt"
	"image"
	"log/slog"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/shm"
	"github.com/jezek/xgb/xfixes"
	"github.com/jezek/xgb/xproto"

	wcapture "github.com/mevdschee/helpbuoy/client-windows/capture"
)

// Screen is the X display in $DISPLAY.
type Screen struct {
	c      *xgb.Conn
	root   xproto.Window
	log    *slog.Logger
	randr  bool
	xfixes bool
	shm    bool

	mu       sync.Mutex
	open     map[*Capturer]struct{}
	shape    wcapture.Shape // cursor image of serial
	serial   uint32
	hasShape bool
}

// New connects to the display and checks that its pixels can be read as
// 32-bit BGRX, which is what every 24 and 32 bit TrueColor X server sends.
func New(log *slog.Logger) (*Screen, error) {
	if log == nil {
		log = slog.Default()
	}
	c, err := xgb.NewConn()
	if err != nil {
		return nil, err
	}
	setup := xproto.Setup(c)
	scr := setup.DefaultScreen(c)
	if err := checkFormat(setup, scr); err != nil {
		c.Close()
		return nil, err
	}
	s := &Screen{c: c, root: scr.Root, log: log, open: make(map[*Capturer]struct{})}
	if randr.Init(c) == nil {
		if v, err := randr.QueryVersion(c, 1, 5).Reply(); err == nil && (v.MajorVersion > 1 || v.MinorVersion >= 5) {
			s.randr = true
		}
	}
	if xfixes.Init(c) == nil {
		if _, err := xfixes.QueryVersion(c, 4, 0).Reply(); err == nil {
			s.xfixes = true
		}
	}
	if shmSupported && shm.Init(c) == nil {
		if _, err := shm.QueryVersion(c).Reply(); err == nil {
			s.shm = true
		}
	}
	go s.drain()
	log.Info("X capture", "randr", s.randr, "xfixes", s.xfixes, "shm", s.shm)
	return s, nil
}

// drain reads events and errors, which xgb queues until someone does.
func (s *Screen) drain() {
	for {
		ev, err := s.c.WaitForEvent()
		if ev == nil && err == nil {
			return
		}
		if err != nil {
			s.log.Debug("X error on the capture connection", "err", err)
		}
	}
}

// checkFormat accepts the default screen when its root depth is stored as
// 32 bits per pixel, least significant byte first, with blue in the lowest
// byte.
func checkFormat(setup *xproto.SetupInfo, scr *xproto.ScreenInfo) error {
	if setup.ImageByteOrder != xproto.ImageOrderLSBFirst {
		return errors.New("capture: the X server sends images most significant byte first, which is not supported")
	}
	bpp := 0
	for _, f := range setup.PixmapFormats {
		if f.Depth == scr.RootDepth {
			bpp = int(f.BitsPerPixel)
		}
	}
	if bpp != 32 {
		return fmt.Errorf("capture: the screen uses %d bits per pixel at depth %d; only 32 is supported", bpp, scr.RootDepth)
	}
	for _, d := range scr.AllowedDepths {
		for _, v := range d.Visuals {
			if v.VisualId == scr.RootVisual && (v.RedMask != 0xff0000 || v.GreenMask != 0xff00 || v.BlueMask != 0xff) {
				return errors.New("capture: the screen's color layout is not RGB with 8 bits per color")
			}
		}
	}
	return nil
}

// Monitors lists the active monitors in root window pixels.
func (s *Screen) Monitors() ([]wcapture.Monitor, error) {
	if s.randr {
		r, err := randr.GetMonitors(s.c, s.root, true).Reply()
		if err == nil && len(r.Monitors) > 0 {
			ms := make([]wcapture.Monitor, 0, len(r.Monitors))
			for _, m := range r.Monitors {
				name := ""
				if a, err := xproto.GetAtomName(s.c, m.Name).Reply(); err == nil {
					name = a.Name
				}
				ms = append(ms, wcapture.Monitor{
					Rect:    image.Rect(int(m.X), int(m.Y), int(m.X)+int(m.Width), int(m.Y)+int(m.Height)),
					Primary: m.Primary,
					Name:    name,
				})
			}
			// Without a primary output RandR marks none; the first one is.
			return ms, nil
		}
	}
	g, err := xproto.GetGeometry(s.c, xproto.Drawable(s.root)).Reply()
	if err != nil {
		return nil, err
	}
	return []wcapture.Monitor{{Rect: image.Rect(0, 0, int(g.Width), int(g.Height)), Primary: true, Name: "screen"}}, nil
}

// Open starts capturing the area of m.
func (s *Screen) Open(m wcapture.Monitor) (wcapture.Capturer, error) {
	r := m.Rect
	if r.Empty() || r.Dx() > 0x7fff || r.Dy() > 0x7fff {
		return nil, fmt.Errorf("capture: bad monitor rectangle %v", r)
	}
	cp := &Capturer{s: s, rect: r}
	if s.shm {
		if err := cp.openShm(); err != nil {
			s.log.Info("MIT-SHM not usable, capturing with GetImage", "err", err)
			cp.closeShm()
		}
	}
	s.mu.Lock()
	s.open[cp] = struct{}{}
	s.mu.Unlock()
	return cp, nil
}

// Cursor returns the cursor position, and as Handle the XFixes cursor
// serial, which changes with the shape. X has no portable way to tell that
// an application hid the cursor, so it is always showing.
func (s *Screen) Cursor() (wcapture.CursorInfo, error) {
	if !s.xfixes {
		p, err := xproto.QueryPointer(s.c, s.root).Reply()
		if err != nil {
			return wcapture.CursorInfo{}, err
		}
		return wcapture.CursorInfo{Showing: true, Pos: image.Pt(int(p.RootX), int(p.RootY)), Handle: 1}, nil
	}
	r, err := xfixes.GetCursorImage(s.c).Reply()
	if err != nil {
		return wcapture.CursorInfo{}, err
	}
	s.mu.Lock()
	if !s.hasShape || r.CursorSerial != s.serial {
		s.serial, s.hasShape = r.CursorSerial, true
		s.shape = shapeFromARGB(r)
	}
	s.mu.Unlock()
	// The serial may be 0; handles must not be.
	h := uintptr(r.CursorSerial) | 1<<32
	return wcapture.CursorInfo{Showing: true, Pos: image.Pt(int(r.X), int(r.Y)), Handle: h}, nil
}

// CursorShape returns the image read with the last Cursor call.
func (s *Screen) CursorShape(handle uintptr) (wcapture.Shape, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasShape || uint32(handle) != s.serial {
		return wcapture.Shape{}, errors.New("capture: no cursor image")
	}
	return s.shape, nil
}

// shapeFromARGB converts the XFixes cursor image, premultiplied ARGB words,
// to straight RGBA.
func shapeFromARGB(r *xfixes.GetCursorImageReply) wcapture.Shape {
	w, h := int(r.Width), int(r.Height)
	pix := make([]byte, w*h*4)
	for i := 0; i < w*h && i < len(r.CursorImage); i++ {
		p := r.CursorImage[i]
		a := byte(p >> 24)
		rr, g, b := byte(p>>16), byte(p>>8), byte(p)
		if a != 0 && a != 255 {
			rr = byte(min(255, int(rr)*255/int(a)))
			g = byte(min(255, int(g)*255/int(a)))
			b = byte(min(255, int(b)*255/int(a)))
		}
		pix[i*4], pix[i*4+1], pix[i*4+2], pix[i*4+3] = rr, g, b, a
	}
	return wcapture.ClipShape(wcapture.Shape{HotX: int(r.Xhot), HotY: int(r.Yhot), W: w, H: h, RGBA: pix})
}

// Close closes every open Capturer and the connection.
func (s *Screen) Close() error {
	s.mu.Lock()
	open := s.open
	s.open = map[*Capturer]struct{}{}
	s.mu.Unlock()
	for cp := range open {
		cp.Close()
	}
	s.c.Close()
	return nil
}

// Capturer captures one monitor.
type Capturer struct {
	s    *Screen
	rect image.Rectangle
	segs [2]*shmSeg // double buffer when MIT-SHM is used
	next int
}

func (cp *Capturer) openShm() error {
	size := cp.rect.Dx() * cp.rect.Dy() * 4
	for i := range cp.segs {
		seg, err := newShmSeg(cp.s.c, size)
		if err != nil {
			return err
		}
		cp.segs[i] = seg
	}
	return nil
}

func (cp *Capturer) closeShm() {
	for i, seg := range cp.segs {
		if seg != nil {
			seg.close(cp.s.c)
			cp.segs[i] = nil
		}
	}
}

// Capture grabs the monitor as BGRX. With MIT-SHM the image is one of two
// shared buffers, valid until the second following call.
func (cp *Capturer) Capture() (*image.RGBA, error) {
	r := cp.rect
	w, h := r.Dx(), r.Dy()
	if seg := cp.segs[cp.next]; seg != nil {
		cp.next = 1 - cp.next
		_, err := shm.GetImage(cp.s.c, xproto.Drawable(cp.s.root), int16(r.Min.X), int16(r.Min.Y),
			uint16(w), uint16(h), 0xffffffff, xproto.ImageFormatZPixmap, seg.id, 0).Reply()
		if err != nil {
			return nil, err
		}
		return &image.RGBA{Pix: seg.data[:w*h*4], Stride: w * 4, Rect: image.Rect(0, 0, w, h)}, nil
	}
	return cp.getImage()
}

// getImage reads the monitor with core GetImage, in bands that fit in one
// reply even without the BIG-REQUESTS extension.
func (cp *Capturer) getImage() (*image.RGBA, error) {
	r := cp.rect
	w, h := r.Dx(), r.Dy()
	img := &image.RGBA{Pix: make([]byte, w*h*4), Stride: w * 4, Rect: image.Rect(0, 0, w, h)}
	// Replies have no request size limit, but very large ones keep the
	// server busy; 16 MiB bands are a good compromise.
	band := max(1, (16<<20)/(w*4))
	for y := 0; y < h; y += band {
		bh := min(band, h-y)
		rep, err := xproto.GetImage(cp.s.c, xproto.ImageFormatZPixmap, xproto.Drawable(cp.s.root),
			int16(r.Min.X), int16(r.Min.Y+y), uint16(w), uint16(bh), 0xffffffff).Reply()
		if err != nil {
			return nil, err
		}
		copy(img.Pix[y*w*4:], rep.Data)
	}
	return img, nil
}

// Close frees the buffers.
func (cp *Capturer) Close() error {
	cp.s.mu.Lock()
	delete(cp.s.open, cp)
	cp.s.mu.Unlock()
	cp.closeShm()
	return nil
}
