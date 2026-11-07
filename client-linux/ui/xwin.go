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
	"sync"
	"sync/atomic"
	"unicode"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xproto"

	"github.com/helpbuoy/helpbuoy/client-linux/xkeys"
)

// xwin is the X11 Host: the connection, the window, atoms, the keyboard
// mapping, pixel upload and the clipboard. Its methods are called from the
// Window's goroutine; the event reader goroutine translates X events into
// Events and answers clipboard requests, and owns km and lastRelease.
type xwin struct {
	c     *xgb.Conn
	setup *xproto.SetupInfo
	scr   *xproto.ScreenInfo
	win   xproto.Window
	gc    xproto.Gcontext
	atoms map[string]xproto.Atom

	km          xkeys.Map
	lastRelease xproto.KeyReleaseEvent // a press at the same time is auto-repeat
	cursors     map[Cursor]xproto.Cursor
	cursor      Cursor
	events      chan Event

	clipMu sync.Mutex
	clip   string        // text we own on CLIPBOARD, "" when we don't
	time   atomic.Uint32 // xproto.Timestamp of the last user input
}

var atomNames = []string{
	"WM_PROTOCOLS", "WM_DELETE_WINDOW", "WM_CHANGE_STATE", "WM_STATE",
	"_NET_WM_NAME", "_NET_WM_ICON", "_NET_ACTIVE_WINDOW", "_NET_WM_PID",
	"_NET_WM_WINDOW_TYPE", "_NET_WM_WINDOW_TYPE_NORMAL",
	"_NET_WM_STATE", "_NET_WM_STATE_DEMANDS_ATTENTION",
	"UTF8_STRING", "CLIPBOARD", "TARGETS", "TEXT", "HELPBUOY_PASTE", "RESOURCE_MANAGER",
}

// Open implements Host: it connects to $DISPLAY and creates the (unmapped)
// window, centered on the primary monitor and sized for the display's DPI.
func (x *xwin) Open(title string, cw, ch int, icons [][]byte) (float64, error) {
	if err := x.open(title, cw, ch, icons); err != nil {
		return 0, err
	}
	x.events = make(chan Event, 64)
	go x.readEvents()
	return x.scale(), nil
}

// scale is the number of pixels per 96-DPI unit.
func (x *xwin) scale() float64 { return max(1, x.dpi()/96) }

func (x *xwin) open(title string, cw, ch int, icons [][]byte) error {
	c, err := xgb.NewConn()
	if err != nil {
		if os.Getenv("DISPLAY") == "" {
			return errors.New("no X display: DISPLAY is not set")
		}
		return err
	}
	x.c, x.setup, x.atoms, x.cursors = c, xproto.Setup(c), map[string]xproto.Atom{}, map[Cursor]xproto.Cursor{}
	x.scr = x.setup.DefaultScreen(c)
	if err := checkFormat(x.setup, x.scr); err != nil {
		c.Close()
		return err
	}
	cookies := make([]xproto.InternAtomCookie, len(atomNames))
	for i, n := range atomNames {
		cookies[i] = xproto.InternAtom(c, false, uint16(len(n)), n)
	}
	for i, ck := range cookies {
		r, err := ck.Reply()
		if err != nil {
			c.Close()
			return err
		}
		x.atoms[atomNames[i]] = r.Atom
	}
	if x.km, err = xkeys.Load(c); err != nil {
		c.Close()
		return err
	}

	x.win, err = xproto.NewWindowId(c)
	if err != nil {
		c.Close()
		return err
	}
	s := x.scale()
	w, h := int(float64(cw)*s+0.5), int(float64(ch)*s+0.5)
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
		return err
	}
	x.gc, _ = xproto.NewGcontextId(c)
	xproto.CreateGC(c, x.gc, xproto.Drawable(x.win), 0, nil)

	x.SetTitle(title)
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
	return nil
}

// Close implements Host.
func (x *xwin) Close() { x.c.Close() }

// Events implements Host.
func (x *xwin) Events() <-chan Event { return x.events }

// Show implements Host.
func (x *xwin) Show() { xproto.MapWindow(x.c, x.win) }

// readEvents translates X events into Events until the connection closes.
func (x *xwin) readEvents() {
	defer close(x.events)
	for {
		ev, err := x.c.WaitForEvent()
		if ev == nil && err == nil {
			return
		}
		if err != nil {
			continue // errors of requests nobody checks
		}
		if e := x.translate(ev); e != nil {
			x.events <- e
		}
	}
}

// translate handles one X event and returns what the Window needs to know
// about it, or nil.
func (x *xwin) translate(ev xgb.Event) Event {
	switch e := ev.(type) {
	case xproto.ExposeEvent:
		if e.Count == 0 {
			return ExposeEvent{}
		}
	case xproto.FocusInEvent:
		return FocusEvent{In: true}
	case xproto.FocusOutEvent:
		return FocusEvent{In: false}
	case xproto.ClientMessageEvent:
		if e.Type == x.atoms["WM_PROTOCOLS"] && xproto.Atom(e.Data.Data32[0]) == x.atoms["WM_DELETE_WINDOW"] {
			return CloseEvent{}
		}
	case xproto.MappingNotifyEvent:
		if km, err := xkeys.Load(x.c); err == nil {
			x.km = km
		}
	case xproto.SelectionRequestEvent:
		x.selectionRequest(e)
	case xproto.SelectionClearEvent:
		x.clipMu.Lock()
		x.clip = ""
		x.clipMu.Unlock()
	case xproto.SelectionNotifyEvent:
		if s, ok := x.pasted(e); ok {
			return PasteEvent{Text: s}
		}
	case xproto.KeyPressEvent:
		x.time.Store(uint32(e.Time))
		repeat := e.Detail == x.lastRelease.Detail && e.Time == x.lastRelease.Time
		return x.keyEvent(e, repeat)
	case xproto.KeyReleaseEvent:
		x.lastRelease = e
	case xproto.ButtonPressEvent:
		x.time.Store(uint32(e.Time))
		if e.Detail == 1 {
			return MouseEvent{Kind: MouseDown, X: int(e.EventX), Y: int(e.EventY)}
		}
	case xproto.ButtonReleaseEvent:
		x.time.Store(uint32(e.Time))
		if e.Detail == 1 {
			return MouseEvent{Kind: MouseUp, X: int(e.EventX), Y: int(e.EventY)}
		}
	case xproto.MotionNotifyEvent:
		return MouseEvent{Kind: MouseMove, X: int(e.EventX), Y: int(e.EventY), Drag: e.State&xproto.ButtonMask1 != 0}
	case xproto.LeaveNotifyEvent:
		return MouseEvent{Kind: MouseLeave}
	}
	return nil
}

// keyEvent translates a key press with the keyboard mapping.
func (x *xwin) keyEvent(e xproto.KeyPressEvent, repeat bool) KeyEvent {
	ks := x.km.Lookup(uint8(e.Detail), e.State)
	ctrl := e.State&xkeys.ModControl != 0
	k := KeyEvent{Cmd: ctrl, Word: ctrl, Shift: e.State&xkeys.ModShift != 0, Repeat: repeat}
	switch ks {
	case xkeys.Return, xkeys.KPEnter:
		k.Key = KeyEnter
	case xkeys.Escape:
		k.Key = KeyEscape
	case xkeys.Tab:
		k.Key = KeyTab
	case xkeys.ISOLeftTab:
		k.Key = KeyBackTab
	case xkeys.BackSpace:
		k.Key = KeyBackspace
	case xkeys.Delete, xkeys.KPDelete:
		k.Key = KeyDelete
	case xkeys.Left, xkeys.KPLeft:
		k.Key = KeyLeft
	case xkeys.Right, xkeys.KPRight:
		k.Key = KeyRight
	case xkeys.Home, xkeys.KPHome:
		k.Key = KeyHome
	case xkeys.End, xkeys.KPEnd:
		k.Key = KeyEnd
	case xkeys.Insert:
		k.Key = KeyInsert
	}
	if k.Key != KeyNone {
		return k
	}
	if r := xkeys.Rune(ks); r != 0 {
		k.Char = unicode.ToLower(r)
		if !ctrl && e.State&xkeys.ModMod1 == 0 {
			k.Text = string(r)
		}
	}
	return k
}

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

// SetTitle implements Host.
func (x *xwin) SetTitle(t string) {
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
	for kind, glyph := range map[Cursor]uint16{CursorArrow: 68, CursorText: 152, CursorHand: 60} {
		cur, err := xproto.NewCursorId(x.c)
		if err != nil {
			continue
		}
		xproto.CreateGlyphCursor(x.c, cur, font, font, glyph, glyph+1, 0, 0, 0, 0xffff, 0xffff, 0xffff)
		x.cursors[kind] = cur
	}
	xproto.CloseFont(x.c, font)
	x.SetCursor(CursorArrow)
}

// SetCursor implements Host.
func (x *xwin) SetCursor(k Cursor) {
	cur, ok := x.cursors[k]
	if !ok || x.cursor == k && k != CursorArrow {
		return
	}
	x.cursor = k
	xproto.ChangeWindowAttributes(x.c, x.win, xproto.CwCursor, []uint32{uint32(cur)})
}

// Put implements Host: it uploads img (RGBA) to the window at (0,0), as
// BGRX in bands that fit in one request.
func (x *xwin) Put(img *image.RGBA) {
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

// Minimize implements Host: it asks the window manager to iconify the
// window.
func (x *xwin) Minimize() {
	const iconicState = 3
	x.clientMessage(x.atoms["WM_CHANGE_STATE"], iconicState)
}

// Activate implements Host: it maps, raises and focuses the window. Window
// managers may refuse the focus; the demands-attention state makes the
// taskbar entry flash instead.
func (x *xwin) Activate() {
	xproto.MapWindow(x.c, x.win)
	xproto.ConfigureWindow(x.c, x.win, xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove})
	// Source 2 (a pager): the user asked for it, so focus stealing
	// prevention should let it through.
	x.clientMessage(x.atoms["_NET_ACTIVE_WINDOW"], 2, x.time.Load(), 0)
}

// DemandAttention implements Host: it sets or clears
// _NET_WM_STATE_DEMANDS_ATTENTION.
func (x *xwin) DemandAttention(on bool) {
	action := uint32(0)
	if on {
		action = 1
	}
	x.clientMessage(x.atoms["_NET_WM_STATE"], action, uint32(x.atoms["_NET_WM_STATE_DEMANDS_ATTENTION"]), 0, 1)
}

// Copy implements Host: it takes CLIPBOARD ownership for text.
func (x *xwin) Copy(text string) bool {
	// Set the text first: the owner check is a round trip, and the event
	// reader may answer a paste request before it returns.
	x.clipMu.Lock()
	old := x.clip
	x.clip = text
	x.clipMu.Unlock()
	xproto.SetSelectionOwner(x.c, x.win, x.atoms["CLIPBOARD"], xproto.Timestamp(x.time.Load()))
	r, err := xproto.GetSelectionOwner(x.c, x.atoms["CLIPBOARD"]).Reply()
	if err != nil || r.Owner != x.win {
		x.clipMu.Lock()
		x.clip = old
		x.clipMu.Unlock()
		return false
	}
	return true
}

// RequestPaste implements Host: it asks the CLIPBOARD owner for its text,
// which arrives as a SelectionNotify event, see pasted.
func (x *xwin) RequestPaste() {
	xproto.ConvertSelection(x.c, x.win, x.atoms["CLIPBOARD"], x.atoms["UTF8_STRING"], x.atoms["HELPBUOY_PASTE"],
		xproto.Timestamp(x.time.Load()))
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
	x.clipMu.Lock()
	clip := x.clip
	x.clipMu.Unlock()
	if clip != "" && ev.Selection == x.atoms["CLIPBOARD"] {
		switch ev.Target {
		case x.atoms["TARGETS"]:
			ts := u32(uint32(x.atoms["TARGETS"]), uint32(x.atoms["UTF8_STRING"]), uint32(xproto.AtomString), uint32(x.atoms["TEXT"]))
			xproto.ChangeProperty(x.c, xproto.PropModeReplace, ev.Requestor, prop, xproto.AtomAtom, 32, 4, ts)
			ok = true
		case x.atoms["UTF8_STRING"], x.atoms["TEXT"]:
			xproto.ChangeProperty(x.c, xproto.PropModeReplace, ev.Requestor, prop, x.atoms["UTF8_STRING"], 8, uint32(len(clip)), []byte(clip))
			ok = true
		case xproto.AtomString:
			b := latin1(clip)
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
