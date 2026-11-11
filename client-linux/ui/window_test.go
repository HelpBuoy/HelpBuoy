package ui

import (
	"errors"
	"image"
	"image/png"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/helpbuoy/helpbuoy/client-windows/app"
	"github.com/helpbuoy/helpbuoy/client-windows/config"
	"github.com/helpbuoy/helpbuoy/proto/appkey"
)

// fakeHost is a Host without a display: it keeps the last frame and takes
// events from the test.
type fakeHost struct {
	events chan Event
	mu     sync.Mutex
	frame  *image.RGBA
	clip   string
}

func newFakeHost() *fakeHost { return &fakeHost{events: make(chan Event, 16)} }

func (h *fakeHost) Open(string, int, int, [][]byte) (float64, error) { return 1, nil }
func (h *fakeHost) Events() <-chan Event                             { return h.events }
func (h *fakeHost) Show()                                            {}
func (h *fakeHost) SetTitle(string)                                  {}
func (h *fakeHost) Minimize()                                        {}
func (h *fakeHost) Activate()                                        {}
func (h *fakeHost) DemandAttention(bool)                             {}
func (h *fakeHost) SetCursor(Cursor)                                 {}
func (h *fakeHost) RequestPaste()                                    { h.events <- PasteEvent{Text: h.clip} }
func (h *fakeHost) Close()                                           {}

func (h *fakeHost) Copy(s string) bool {
	h.clip = s
	return true
}

func (h *fakeHost) Put(img *image.RGBA) {
	h.mu.Lock()
	h.frame = image.NewRGBA(img.Rect)
	copy(h.frame.Pix, img.Pix)
	h.mu.Unlock()
}

// waitFor polls cond until it holds or a second passed.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// newController returns a controller whose key never loads, which is
// enough to tell whether it was started: Start loads the key.
func newController(t *testing.T, loads *atomic.Int32, w *Window) *app.Controller {
	return app.New(app.Deps{
		UI:    w,
		Store: config.Store{Dir: t.TempDir()},
		LoadKey: func() (*appkey.Key, error) {
			loads.Add(1)
			return nil, errors.New("no key in this test")
		},
		RemoveKey: func() error { return nil },
	})
}

func TestPermissionsScreen(t *testing.T) {
	host := newFakeHost()
	w := NewWithHost(host, nil)
	var screen, input atomic.Bool
	opened := 0
	w.Permissions = []Permission{
		{Name: "Screen Recording", Text: "So the techie can see your screen.", Granted: screen.Load, Open: func() { opened++ }},
		{Name: "Accessibility", Text: "So the techie can use your mouse and keyboard.", Granted: input.Load},
	}
	w.PermissionsNote = "Turn on HelpBuoy in each list."
	var loads atomic.Int32
	ctrl := newController(t, &loads, w)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctrl) }()

	waitFor(t, "a frame", func() bool {
		host.mu.Lock()
		defer host.mu.Unlock()
		return host.frame != nil
	})
	// The first button opens the first permission's settings.
	host.events <- KeyEvent{Key: KeyEnter}
	w.do(func() {})
	if opened != 1 {
		t.Errorf("Enter on the first button opened %d times, want 1", opened)
	}
	input.Store(true)
	time.Sleep(400 * time.Millisecond) // a few polls
	if n := loads.Load(); n != 0 {
		t.Fatalf("controller started with a permission missing (%d key loads)", n)
	}
	if out := os.Getenv("HELPBUOY_UI_PNG"); out != "" {
		host.mu.Lock()
		f, err := os.Create(out)
		if err == nil {
			png.Encode(f, host.frame)
			f.Close()
		}
		host.mu.Unlock()
	}
	screen.Store(true)
	waitFor(t, "the controller to start", func() bool { return loads.Load() > 0 })
	w.do(func() {
		if w.perms != nil || w.lblPermHeading.visible {
			t.Error("the permissions screen is still shown")
		}
	})

	host.events <- CloseEvent{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAddressEditing(t *testing.T) {
	host := newFakeHost()
	w := NewWithHost(host, nil)
	var loads atomic.Int32
	ctrl := newController(t, &loads, w)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctrl) }()
	waitFor(t, "the controller to start", func() bool { return loads.Load() > 0 })

	// Without a key the window shows the key error; put the address screen
	// up by hand to edit its field.
	w.do(func() {
		w.apply(app.View{Screen: app.ScreenAddress})
		w.focus = w.edtAddr
	})
	for _, e := range []KeyEvent{
		{Char: 'h', Text: "h"}, {Char: 'i', Text: "i"},
		{Key: KeyLeft}, {Char: 'x', Text: "x"}, // hxi
		{Key: KeyHome}, {Char: 'a', Cmd: true}, {Char: 'c', Cmd: true}, // copy all
		{Key: KeyEnd}, {Char: 'v', Cmd: true}, // hxihxi
		{Key: KeyLeft, Word: true}, {Key: KeyBackspace}, // the word is the whole text
		{Char: 'z', Cmd: true, Text: ""}, // a shortcut types nothing
	} {
		host.events <- e
	}
	waitFor(t, "the edits", func() bool {
		var s string
		w.do(func() { s = string(w.edtAddr.field.text) })
		return s == "hxihxi"
	})
	if host.clip != "hxi" {
		t.Errorf("copied %q, want hxi", host.clip)
	}
	host.events <- CloseEvent{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// TestProxyCheckbox: the box under the address field shows the setting,
// and Space toggles it.
func TestProxyCheckbox(t *testing.T) {
	host := newFakeHost()
	w := NewWithHost(host, nil)
	var loads atomic.Int32
	ctrl := newController(t, &loads, w)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctrl) }()
	waitFor(t, "the controller to start", func() bool { return loads.Load() > 0 })

	w.do(func() {
		w.apply(app.View{Screen: app.ScreenAddress, Address: "helpbuoy.example.com", UseSystemProxy: true,
			Error: "Nothing is answering at this address."})
		w.focus = w.chkProxy
	})
	var checked, visible bool
	w.do(func() { checked, visible = w.chkProxy.checked, w.chkProxy.visible })
	if !checked || !visible {
		t.Fatalf("checkbox checked %v visible %v", checked, visible)
	}
	if out := os.Getenv("HELPBUOY_UI_PNG"); out != "" {
		w.do(func() { w.redraw() })
		host.mu.Lock()
		if f, err := os.Create(out); err == nil {
			png.Encode(f, host.frame)
			f.Close()
		}
		host.mu.Unlock()
	}
	w.do(func() {
		w.key(KeyEvent{Char: ' ', Text: " "})
		checked = w.chkProxy.checked
	})
	if checked {
		t.Error("Space did not untick the box")
	}
	host.events <- CloseEvent{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
