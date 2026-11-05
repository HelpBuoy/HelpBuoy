package main

import (
	"context"
	"image"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"

	"github.com/helpbuoy/helpbuoy/client-linux/injector"
	"github.com/helpbuoy/helpbuoy/client-linux/xkeys"
	"github.com/helpbuoy/helpbuoy/client-windows/identity"
	"github.com/helpbuoy/helpbuoy/proto/inner"
	"github.com/helpbuoy/helpbuoy/proto/input"
	"github.com/helpbuoy/helpbuoy/proto/screen"
	"github.com/helpbuoy/helpbuoy/proto/wire"
)

// These tests need an X server they may take over, such as
//
//	Xvfb :77 -screen 0 1280x800x24 &
//	HELPBUOY_X11_TEST=:77 go test ./client-linux
//
// They are skipped otherwise.
func xDisplay(t *testing.T) {
	d := os.Getenv("HELPBUOY_X11_TEST")
	if d == "" {
		t.Skip("set HELPBUOY_X11_TEST to an X display the test may take over")
	}
	t.Setenv("DISPLAY", d)
}

// testWindow is a red window that records the keysyms of its key presses.
type testWindow struct {
	c   *xgb.Conn
	win xproto.Window

	mu   sync.Mutex
	keys []xproto.Keysym

	wantShift atomic.Bool // record Shift presses
}

func newTestWindow(t *testing.T, x, y, w, h int) *testWindow {
	c, err := xgb.NewConn()
	if err != nil {
		t.Fatal(err)
	}
	scr := xproto.Setup(c).DefaultScreen(c)
	win, _ := xproto.NewWindowId(c)
	xproto.CreateWindow(c, scr.RootDepth, win, scr.Root, int16(x), int16(y), uint16(w), uint16(h), 0,
		xproto.WindowClassInputOutput, scr.RootVisual, xproto.CwBackPixel|xproto.CwOverrideRedirect|xproto.CwEventMask,
		[]uint32{0xff0000, 1, xproto.EventMaskKeyPress | xproto.EventMaskExposure})
	xproto.MapWindow(c, win)
	xproto.SetInputFocus(c, xproto.InputFocusPointerRoot, win, xproto.TimeCurrentTime)
	if _, err := xproto.GetInputFocus(c).Reply(); err != nil {
		t.Fatal(err)
	}
	tw := &testWindow{c: c, win: win}
	go func() {
		for {
			ev, err := c.WaitForEvent()
			if ev == nil && err == nil {
				return
			}
			if k, ok := ev.(xproto.KeyPressEvent); ok {
				km, _ := xkeys.Load(c) // the injector may have remapped a key
				ks := km.Lookup(uint8(k.Detail), k.State)
				if ks == xkeys.ShiftL && !tw.wantShift.Load() {
					continue // typing "H" presses Shift too
				}
				tw.mu.Lock()
				tw.keys = append(tw.keys, ks)
				tw.mu.Unlock()
			}
		}
	}()
	t.Cleanup(c.Close)
	return tw
}

func (tw *testWindow) waitKeys(t *testing.T, want ...xproto.Keysym) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		tw.mu.Lock()
		got := append([]xproto.Keysym{}, tw.keys...)
		tw.mu.Unlock()
		if len(got) >= len(want) {
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("key presses %#x, want %#x", got, want)
				}
			}
			tw.mu.Lock()
			tw.keys = tw.keys[len(want):]
			tw.mu.Unlock()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("key presses %#x, want %#x", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestScreenSession runs a real session against the X server: capture,
// cursor, mouse, keys and text.
func TestScreenSession(t *testing.T) {
	xDisplay(t)
	tw := newTestWindow(t, 100, 100, 200, 150)

	computer, _ := identity.Ephemeral("anna@pc")
	techie, _ := identity.Ephemeral("alice")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()
	techieside, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer techieside.Close()
	computerside := <-accepted

	r := newScreenRunner(computer, screen.Hello{UserMachine: "anna@pc", OS: "linux"}, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- r.Run(ctx, computerside, techie.Public) }()

	cs, err := inner.Dial(techieside, techie.Signer, computer.Public)
	if err != nil {
		t.Fatal("inner dial:", err)
	}
	defer cs.Close()
	fr, fw := wire.NewReader(cs.Screen()), wire.NewWriter(cs.Screen())
	iw := wire.NewWriter(cs.Input())
	send := func(m any) {
		typ, b, err := input.Encode(m)
		if err != nil {
			t.Fatal(err)
		}
		iw.WriteFrame(typ, b)
	}

	// Frames until the red window shows; the cursor comes along.
	var img *image.RGBA
	dec := screen.NewDecoder()
	var h screen.Hello
	cursor := false
	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("the red window never showed")
		}
		typ, b, err := fr.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		switch typ {
		case screen.MsgHello:
			if h, err = screen.DecodeHello(b); err != nil {
				t.Fatal(err)
			}
			img = image.NewRGBA(image.Rect(0, 0, h.Width, h.Height))
		case screen.MsgCursorPos:
			cursor = true
		case screen.MsgFrame:
			f, err := screen.DecodeFrame(b)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := dec.Apply(img, f); err != nil {
				t.Fatal(err)
			}
			fw.WriteFrame(screen.MsgAck, screen.EncodeAck(screen.Ack{Seq: f.Seq}))
		}
		if img != nil {
			p := img.RGBAAt(200, 170)
			if p.R > 200 && p.G < 50 && p.B < 50 && cursor {
				break
			}
		}
	}
	if h.OS != "linux" || len(h.Monitors) == 0 {
		t.Fatalf("hello %+v", h)
	}

	// The mouse lands on the pixel the techie points at.
	mon := h.Monitors[h.Current]
	norm := func(v, size int) uint16 { return uint16((v*65535 + (size-1)/2) / (size - 1)) }
	send(input.MouseMove{X: norm(321, mon.W), Y: norm(234, mon.H)})
	deadline = time.Now().Add(2 * time.Second)
	for {
		p, err := xproto.QueryPointer(tw.c, xproto.Setup(tw.c).DefaultScreen(tw.c).Root).Reply()
		if err != nil {
			t.Fatal(err)
		}
		if int(p.RootX) == mon.X+321 && int(p.RootY) == mon.Y+234 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pointer at %d,%d", p.RootX, p.RootY)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Keys by position, then text, including characters the layout lacks.
	send(input.Key{Code: "KeyA", Down: true})
	send(input.Key{Code: "KeyA", Down: false})
	tw.waitKeys(t, 'a')
	send(input.Text{Text: "Hi€✓\n"})
	tw.waitKeys(t, 'H', 'i', xkeys.Keysym('€'), xkeys.Keysym('✓'), xkeys.Return)

	// A held key is released when the session ends.
	tw.wantShift.Store(true)
	send(input.Key{Code: "ShiftLeft", Down: true})
	tw.waitKeys(t, xkeys.ShiftL)
	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}
	km, err := xproto.QueryKeymap(tw.c).Reply()
	if err != nil {
		t.Fatal(err)
	}
	if shift := 42 + 8; km.Keys[shift/8]&(1<<(shift%8)) != 0 {
		t.Fatal("Shift is still down after the session")
	}
}

// TestEscHook checks that a local Esc ends the session and an injected one
// does not.
func TestEscHook(t *testing.T) {
	xDisplay(t)
	var fired atomic.Int32
	stop, err := injector.InstallEscHook(func() { fired.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	time.Sleep(100 * time.Millisecond)

	j := injector.New(slog.Default(), nil)
	defer j.Close()
	for _, down := range []bool{true, false} {
		typ, b, _ := input.Encode(input.Key{Code: "Escape", Down: down})
		j.Input(typ, b)
		time.Sleep(80 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	if n := fired.Load(); n != 0 {
		t.Fatalf("the injected Esc fired the hook %d times", n)
	}

	// The "local" keyboard: XTEST from another client, which the injector
	// does not know about.
	c, err := xgb.NewConn()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	xtest.Init(c)
	root := xproto.Setup(c).DefaultScreen(c).Root
	xtest.FakeInput(c, 2, 9, 0, root, 0, 0, 0)
	xproto.GetInputFocus(c).Reply()
	time.Sleep(100 * time.Millisecond)
	xtest.FakeInput(c, 3, 9, 0, root, 0, 0, 0)
	xproto.GetInputFocus(c).Reply()
	deadline := time.Now().Add(2 * time.Second)
	for fired.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("a local Esc did not fire the hook")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
