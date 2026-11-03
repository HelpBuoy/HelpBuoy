package ui

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"strconv"
	"strings"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xproto"

	"github.com/mevdschee/helpbuoy/client-linux/xkeys"
)

// xwin is the X side of the window: the connection, the window, atoms,
// the keyboard mapping, pixel upload and the clipboard. It is used from the
// UI goroutine only, except for the event reader.
type xwin struct {
	c     *xgb.Conn
	setup *xproto.SetupInfo
	scr   *xproto.ScreenInfo
	win   xproto.Window
	gc    xproto.Gcontext
	atoms map[string]xproto.Atom

	km      xkeys.Map
	cursors map[cursorKind]xproto.Cursor
	cursor  cursorKind

	clip string // text we own on CLIPBOARD, "" when we don't
	time xproto.Timestamp
}

// cursorKind is a pointer shape from the core cursor font.
type cursorKind int

const (
	cursorArrow cursorKind = iota
	cursorText
	cursorHand
)

var atomNames = []string{
	"WM_PROTOCOLS", "WM_DELETE_WINDOW", "WM_CHANGE_STATE", "WM_STATE",
	"_NET_WM_NAME", "_NET_WM_ICON", "_NET_ACTIVE_WINDOW", "_NET_WM_PID",
	"_NET_WM_WINDOW_TYPE", "_NET_WM_WINDOW_TYPE_NORMAL",
	"_NET_WM_STATE", "_NET_WM_STATE_DEMANDS_ATTENTION",
	"UTF8_STRING", "CLIPBOARD", "TARGETS", "TEXT", "HELPBUOY_PASTE", "RESOURCE_MANAGER",
}

// openX connects to $DISPLAY and creates the (unmapped) window, centered
// on the primary monitor. size gives the window size for the display's DPI.
func openX(title string, size func(dpi float64) (w, h int), icons [][]byte) (*xwin, error) {
	c, err := xgb.NewConn()
	if err != nil {
		if os.Getenv("DISPLAY") == "" {
			return nil, errors.New("no X display: DISPLAY is not set")
		}
		return nil, err
	}
	x := &xwin{c: c, setup: xproto.Setup(c), atoms: map[string]xproto.Atom{}, cursors: map[cursorKind]xproto.Cursor{}}
	x.scr = x.setup.DefaultScreen(c)
	if err := checkFormat(x.setup, x.scr); err != nil {
		c.Close()
		return nil, err
	}
	cookies := make([]xproto.InternAtomCookie, len(atomNames))
	for i, n := range atomNames {
		cookies[i] = xproto.InternAtom(c, false, uint16(len(n)), n)
	}
	for i, ck := range cookies {
		r, err := ck.Reply()
		if err != nil {
			c.Close()
			return nil, err
		}
		x.atoms[atomNames[i]] = r.Atom
	}
	if x.km, err = xkeys.Load(c); err != nil {
		c.Close()
		return nil, err
	}

	x.win, err = xproto.NewWindowId(c)
	if err != nil {
		c.Close()
		return nil, err
	}
	w, h := size(x.dpi())
	area := x.primary()
	px, py := area.Min.X+(area.Dx()-w)/2, area.Min.Y+(area.Dy()-h)/2
	mask := uint32(xproto.EventMaskExposure | xproto.EventMaskKeyPress | xproto.EventMaskKeyRelease |
		xproto.EventMaskButtonPress | xproto.EventMaskButtonRelease | xproto.EventMaskPointerMotion |
		xproto.EventMaskLeaveWindow | xproto.EventMaskStructureNotify | xproto.EventMaskFocusChange |
		xproto.EventMaskPropertyChange)
	err = xproto.CreateWindowChecked(c, x.scr.RootDepth, x.win, x.scr.Root, int16(px), int16(py),
		uint16(w), uint16(h), 0, xproto.WindowClassInputOutput, x.scr.RootVisual,
		xproto.CwBackPixel|xproto.CwEventMask, []uint32{0xf0f0f0, mask}).Check()
	if err != nil {
		c.Close()
		return nil, err
	}
	x.gc, _ = xproto.NewGcontextId(c)
	xproto.CreateGC(c, x.gc, xproto.Drawable(x.win), 0, nil)

	x.setTitle(title)
	class := "helpbuoy\x00HelpBuoy\x00"
	x.prop(xproto.AtomWmClass, xproto.AtomString, 8, []byte(class))
	x.prop(x.atoms["WM_PROTOCOLS"], xproto.AtomAtom, 32, u32(uint32(x.atoms["WM_DELETE_WINDOW"])))
	x.prop(x.atoms["_NET_WM_PID"], xproto.AtomCardinal, 32, u32(uint32(os.Getpid())))
	x.prop(x.atoms["_NET_WM_WINDOW_TYPE"], xproto.AtomAtom, 32, u32(uint32(x.atoms["_NET_WM_WINDOW_TYPE_NORMAL"])))
	if host, err := os.Hostname(); err == nil {
		x.prop(xproto.AtomWmClientMachine, xproto.AtomString, 8, []byte(host))
	}
	x.setFixedSize(px, py, w, h)
	// WM_HINTS: the window takes the keyboard focus (InputHint) and starts
	// in the normal state (StateHint).
	x.prop(xproto.AtomWmHints, xproto.AtomWmHints, 32, u32(1|2, 1, 1, 0, 0, 0, 0, 0, 0))
	if icon := netWMIcon(icons); len(icon) > 0 {
		x.prop(x.atoms["_NET_WM_ICON"], xproto.AtomCardinal, 32, icon)
	}
	x.loadCursors()
	return x, nil
}

func (x *xwin) close() { x.c.Close() }

// checkFormat accepts screens whose root visual is stored as 32-bit BGRX,
// least significant byte first: every 24 and 32 bit TrueColor X server.
func checkFormat(setup *xproto.SetupInfo, scr *xproto.ScreenInfo) error {
	bpp := 0
	for _, f := range setup.PixmapFormats {
		if f.Depth == scr.RootDepth {
			bpp = int(f.BitsPerPixel)
		}
	}
	if setup.ImageByteOrder != xproto.ImageOrderLSBFirst || bpp != 32 {
		return fmt.Errorf("the X screen (depth %d, %d bits per pixel) is not supported; it needs 24 or 32 bit color", scr.RootDepth, bpp)
	}
	return nil
}

// primary returns the primary monitor's rectangle, or the whole screen.
func (x *xwin) primary() image.Rectangle {
	all := image.Rect(0, 0, int(x.scr.WidthInPixels), int(x.scr.HeightInPixels))
	if randr.Init(x.c) != nil {
		return all
	}
	if v, err := randr.QueryVersion(x.c, 1, 5).Reply(); err != nil || v.MajorVersion == 1 && v.MinorVersion < 5 {
		return all
	}
	r, err := randr.GetMonitors(x.c, x.scr.Root, true).Reply()
	if err != nil || len(r.Monitors) == 0 {
		return all
	}
	m := r.Monitors[0]
	for _, mm := range r.Monitors {
		if mm.Primary {
			m = mm
		}
	}
	return image.Rect(int(m.X), int(m.Y), int(m.X)+int(m.Width), int(m.Y)+int(m.Height))
}

// dpi reads Xft.dpi from the root window's resources, as desktops set it
// for scaled displays, or returns 96.
func (x *xwin) dpi() float64 {
	r, err := xproto.GetProperty(x.c, false, x.scr.Root, x.atoms["RESOURCE_MANAGER"],
		xproto.AtomString, 0, 1<<16).Reply()
	if err != nil {
		return 96
	}
	for _, line := range strings.Split(string(r.Value), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(k) == "Xft.dpi" {
			if d, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && d >= 48 && d <= 480 {
				return d
			}
		}
	}
	return 96
}

func u32(vs ...uint32) []byte {
	b := make([]byte, 4*len(vs))
	for i, v := range vs {
		binary.LittleEndian.PutUint32(b[i*4:], v)
	}
	return b
}

// prop replaces a property of the window. data is in client byte order,
// which xgb sends as is; X servers accept either order from little-endian
// clients, which is what xgb announces.
func (x *xwin) prop(name, typ xproto.Atom, format byte, data []byte) {
	n := len(data) / int(format/8)
	xproto.ChangeProperty(x.c, xproto.PropModeReplace, x.win, name, typ, format, uint32(n), data)
}

func (x *xwin) setTitle(t string) {
	x.prop(x.atoms["_NET_WM_NAME"], x.atoms["UTF8_STRING"], 8, []byte(t))
	x.prop(xproto.AtomWmName, xproto.AtomString, 8, latin1(t))
}

func latin1(s string) []byte {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 0xff {
			r = '?'
		}
		b = append(b, byte(r))
	}
	return b
}

// setFixedSize sets WM_NORMAL_HINTS: the window can't be resized, and its
// position is the program's choice.
func (x *xwin) setFixedSize(px, py, w, h int) {
	const pPosition, pSize, pMinSize, pMaxSize = 4, 8, 16, 32
	hints := make([]uint32, 18)
	hints[0] = pPosition | pSize | pMinSize | pMaxSize
	hints[1], hints[2], hints[3], hints[4] = uint32(px), uint32(py), uint32(w), uint32(h)
	hints[5], hints[6], hints[7], hints[8] = uint32(w), uint32(h), uint32(w), uint32(h)
	x.prop(xproto.AtomWmNormalHints, xproto.AtomWmSizeHints, 32, u32(hints...))
}

// netWMIcon builds _NET_WM_ICON from PNG files: width, height, then ARGB
// pixels, for each size.
func netWMIcon(pngs [][]byte) []byte {
	var out []uint32
	for _, p := range pngs {
		img, err := png.Decode(bytes.NewReader(p))
		if err != nil {
			continue
		}
		b := img.Bounds()
		rgba := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)
		out = append(out, uint32(b.Dx()), uint32(b.Dy()))
		for i := 0; i < len(rgba.Pix); i += 4 {
			px := rgba.Pix[i : i+4]
			out = append(out, uint32(px[3])<<24|uint32(px[0])<<16|uint32(px[1])<<8|uint32(px[2]))
		}
	}
	return u32(out...)
}

// loadCursors creates the pointer shapes from the core cursor font.
func (x *xwin) loadCursors() {
	font, err := xproto.NewFontId(x.c)
	if err != nil {
		return
	}
	if xproto.OpenFontChecked(x.c, font, 6, "cursor").Check() != nil {
		return
	}
	for kind, glyph := range map[cursorKind]uint16{cursorArrow: 68, cursorText: 152, cursorHand: 60} {
		cur, err := xproto.NewCursorId(x.c)
		if err != nil {
			continue
		}
		xproto.CreateGlyphCursor(x.c, cur, font, font, glyph, glyph+1, 0, 0, 0, 0xffff, 0xffff, 0xffff)
		x.cursors[kind] = cur
	}
	xproto.CloseFont(x.c, font)
	x.setCursor(cursorArrow)
}

func (x *xwin) setCursor(k cursorKind) {
	cur, ok := x.cursors[k]
	if !ok || x.cursor == k && k != cursorArrow {
		return
	}
	x.cursor = k
	xproto.ChangeWindowAttributes(x.c, x.win, xproto.CwCursor, []uint32{uint32(cur)})
}

// put uploads img (RGBA) to the window at (0,0), as BGRX in bands that fit
// in one request.
func (x *xwin) put(img *image.RGBA) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	maxReq := int(x.setup.MaximumRequestLength)*4 - 32
	rows := max(1, maxReq/(w*4))
	buf := make([]byte, w*rows*4)
	for y := 0; y < h; y += rows {
		n := min(rows, h-y)
		b := buf[:w*n*4]
		for r := 0; r < n; r++ {
			src := img.Pix[(y+r)*img.Stride:]
			dst := b[r*w*4:]
			for i := 0; i < w*4; i += 4 {
				dst[i], dst[i+1], dst[i+2], dst[i+3] = src[i+2], src[i+1], src[i], 0
			}
		}
		xproto.PutImage(x.c, xproto.ImageFormatZPixmap, xproto.Drawable(x.win), x.gc,
			uint16(w), uint16(n), 0, int16(y), 0, x.scr.RootDepth, b)
	}
}

// clientMessage sends a 32-bit client message about the window to the
// root window, the way window managers expect requests.
func (x *xwin) clientMessage(typ xproto.Atom, data ...uint32) {
	var d [5]uint32
	copy(d[:], data)
	ev := xproto.ClientMessageEvent{Format: 32, Window: x.win, Type: typ,
		Data: xproto.ClientMessageDataUnionData32New(d[:])}
	xproto.SendEvent(x.c, false, x.scr.Root,
		xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(ev.Bytes()))
}

// minimize asks the window manager to iconify the window.
func (x *xwin) minimize() {
	const iconicState = 3
	x.clientMessage(x.atoms["WM_CHANGE_STATE"], iconicState)
}

// activate maps, raises and focuses the window. Window managers may refuse
// the focus; the demands-attention state makes the taskbar entry flash
// instead.
func (x *xwin) activate() {
	xproto.MapWindow(x.c, x.win)
	xproto.ConfigureWindow(x.c, x.win, xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove})
	// Source 2 (a pager): the user asked for it, so focus stealing
	// prevention should let it through.
	x.clientMessage(x.atoms["_NET_ACTIVE_WINDOW"], 2, uint32(x.time), 0)
}

// demandAttention sets or clears _NET_WM_STATE_DEMANDS_ATTENTION.
func (x *xwin) demandAttention(on bool) {
	action := uint32(0)
	if on {
		action = 1
	}
	x.clientMessage(x.atoms["_NET_WM_STATE"], action, uint32(x.atoms["_NET_WM_STATE_DEMANDS_ATTENTION"]), 0, 1)
}

// copy takes CLIPBOARD ownership for text.
func (x *xwin) copy(text string) bool {
	xproto.SetSelectionOwner(x.c, x.win, x.atoms["CLIPBOARD"], x.time)
	r, err := xproto.GetSelectionOwner(x.c, x.atoms["CLIPBOARD"]).Reply()
	if err != nil || r.Owner != x.win {
		return false
	}
	x.clip = text
	return true
}

// requestPaste asks the CLIPBOARD owner for its text; it arrives as a
// SelectionNotify event, see pasted.
func (x *xwin) requestPaste() {
	xproto.ConvertSelection(x.c, x.win, x.atoms["CLIPBOARD"], x.atoms["UTF8_STRING"], x.atoms["HELPBUOY_PASTE"], x.time)
}

// pasted reads the text of a SelectionNotify for requestPaste.
func (x *xwin) pasted(ev xproto.SelectionNotifyEvent) (string, bool) {
	if ev.Property == xproto.AtomNone {
		return "", false
	}
	r, err := xproto.GetProperty(x.c, true, x.win, ev.Property, xproto.GetPropertyTypeAny, 0, 1<<16).Reply()
	if err != nil || r.Format != 8 {
		return "", false
	}
	return string(r.Value), true
}

// selectionRequest answers another application that pastes our text.
func (x *xwin) selectionRequest(ev xproto.SelectionRequestEvent) {
	prop := ev.Property
	if prop == xproto.AtomNone {
		prop = ev.Target // obsolete clients
	}
	ok := false
	if x.clip != "" && ev.Selection == x.atoms["CLIPBOARD"] {
		switch ev.Target {
		case x.atoms["TARGETS"]:
			ts := u32(uint32(x.atoms["TARGETS"]), uint32(x.atoms["UTF8_STRING"]), uint32(xproto.AtomString), uint32(x.atoms["TEXT"]))
			xproto.ChangeProperty(x.c, xproto.PropModeReplace, ev.Requestor, prop, xproto.AtomAtom, 32, 4, ts)
			ok = true
		case x.atoms["UTF8_STRING"], x.atoms["TEXT"]:
			xproto.ChangeProperty(x.c, xproto.PropModeReplace, ev.Requestor, prop, x.atoms["UTF8_STRING"], 8, uint32(len(x.clip)), []byte(x.clip))
			ok = true
		case xproto.AtomString:
			b := latin1(x.clip)
			xproto.ChangeProperty(x.c, xproto.PropModeReplace, ev.Requestor, prop, xproto.AtomString, 8, uint32(len(b)), b)
			ok = true
		}
	}
	reply := xproto.SelectionNotifyEvent{Time: ev.Time, Requestor: ev.Requestor, Selection: ev.Selection, Target: ev.Target, Property: prop}
	if !ok {
		reply.Property = xproto.AtomNone
	}
	xproto.SendEvent(x.c, false, ev.Requestor, 0, string(reply.Bytes()))
}
