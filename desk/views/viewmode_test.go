package views

import (
	"math"
	"reflect"
	"testing"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/mevdschee/helpbuoy/proto/input"
)

func near(a, b float32) bool { return math.Abs(float64(a-b)) < 0.01 }

func rectNear(a, b rl.Rectangle) bool {
	return near(a.X, b.X) && near(a.Y, b.Y) && near(a.Width, b.Width) && near(a.Height, b.Height)
}

func TestLayoutFit(t *testing.T) {
	view := rl.NewRectangle(100, 50, 800, 600)
	l := layoutView(view, 1600, 900, modeFit, 2, rl.NewVector2(300, 300), true)
	if want := rl.NewRectangle(100, 125, 800, 450); !rectNear(l.dest, want) {
		t.Errorf("fit dest %+v, want %+v", l.dest, want)
	}
	if l.hbar || l.vbar || l.scroll != (rl.Vector2{}) || l.port != view {
		t.Errorf("fit has scrolling: %+v", l)
	}
	x, y := l.toRemote(rl.NewVector2(100, 125))
	if x != 0 || y != 0 {
		t.Errorf("top left maps to %d,%d", x, y)
	}
	x, y = l.toRemote(rl.NewVector2(900, 575))
	if x != 65535 || y != 65535 {
		t.Errorf("bottom right maps to %d,%d", x, y)
	}
}

func TestLayoutOneSmallCentered(t *testing.T) {
	view := rl.NewRectangle(0, 0, 1000, 800)
	l := layoutView(view, 800, 600, modeOne, 1, rl.NewVector2(50, 50), true)
	if want := rl.NewRectangle(100, 100, 800, 600); !rectNear(l.dest, want) {
		t.Errorf("dest %+v, want %+v", l.dest, want)
	}
	if l.hbar || l.vbar || l.scroll != (rl.Vector2{}) {
		t.Errorf("unexpected scrolling: %+v", l)
	}
	// On a 2x screen 800x600 remote pixels are 400x300 logical pixels.
	l = layoutView(view, 800, 600, modeOne, 2, rl.Vector2{}, true)
	if want := rl.NewRectangle(300, 250, 400, 300); !rectNear(l.dest, want) {
		t.Errorf("hidpi dest %+v, want %+v", l.dest, want)
	}
}

func TestLayoutOneScrolled(t *testing.T) {
	view := rl.NewRectangle(10, 20, 800, 600)
	l := layoutView(view, 2560, 1440, modeOne, 1, rl.NewVector2(500, 99999), true)
	if !l.hbar || !l.vbar {
		t.Fatalf("bars %v %v", l.hbar, l.vbar)
	}
	if want := rl.NewRectangle(10, 20, 790, 590); l.port != want {
		t.Errorf("port %+v, want %+v", l.port, want)
	}
	if l.maxScroll != rl.NewVector2(2560-790, 1440-590) {
		t.Errorf("max scroll %+v", l.maxScroll)
	}
	if l.scroll != rl.NewVector2(500, 850) {
		t.Errorf("scroll %+v", l.scroll)
	}
	if want := rl.NewRectangle(10-500, 20-850, 2560, 1440); !rectNear(l.dest, want) {
		t.Errorf("dest %+v, want %+v", l.dest, want)
	}
	// The pointer at the port's top left is remote pixel (500, 850).
	x, y := l.toRemote(rl.NewVector2(10, 20))
	if want := normalize(500, 2560); x != want {
		t.Errorf("x %d, want %d", x, want)
	}
	if want := normalize(850, 1440); y != want {
		t.Errorf("y %d, want %d", y, want)
	}
	// The cursor at a remote pixel comes back to the same window position.
	if p := l.fromRemote(500, 850, 2560, 1440); !near(p.X, 10) || !near(p.Y, 20) {
		t.Errorf("fromRemote %+v", p)
	}
	if v := l.visible(); v != l.port {
		t.Errorf("visible %+v", v)
	}
}

func TestLayoutOneHiDPIScroll(t *testing.T) {
	// 2x: the 1000x500 logical view holds 2000x1000 remote pixels.
	view := rl.NewRectangle(0, 0, 1000, 500)
	l := layoutView(view, 2560, 1000, modeOne, 2, rl.NewVector2(100, 0), false)
	if l.hbar || l.vbar {
		t.Error("bars without bars allowed")
	}
	if l.maxScroll != rl.NewVector2(560, 0) {
		t.Errorf("max scroll %+v", l.maxScroll)
	}
	if want := rl.NewRectangle(-50, 0, 1280, 500); !rectNear(l.dest, want) {
		t.Errorf("dest %+v, want %+v", l.dest, want)
	}
	x, _ := l.toRemote(rl.NewVector2(0, 0))
	if want := normalize(100, 2560); math.Abs(float64(x)-float64(want)) > 1 {
		t.Errorf("x %d, want %d", x, want)
	}
}

func TestLayoutOneBarCascade(t *testing.T) {
	// Only slightly wider than the view: the horizontal bar takes height,
	// which makes the screen too tall as well.
	view := rl.NewRectangle(0, 0, 800, 600)
	l := layoutView(view, 900, 595, modeOne, 1, rl.Vector2{}, true)
	if !l.hbar || !l.vbar {
		t.Errorf("bars %v %v", l.hbar, l.vbar)
	}
	l = layoutView(view, 900, 500, modeOne, 1, rl.Vector2{}, true)
	if !l.hbar || l.vbar {
		t.Errorf("bars %v %v", l.hbar, l.vbar)
	}
	// Vertically centered in the port above the bar.
	if !near(l.dest.Y, (590-500)/2) {
		t.Errorf("dest y %v", l.dest.Y)
	}
}

func TestLayoutEmpty(t *testing.T) {
	l := layoutView(rl.NewRectangle(5, 6, 100, 100), 0, 0, modeOne, 1, rl.Vector2{}, true)
	if l.dest.Width != 0 || l.dest.Height != 0 {
		t.Errorf("dest %+v", l.dest)
	}
}

func TestEdgeSpeed(t *testing.T) {
	cases := []struct {
		pos, want float32
	}{
		{100, -1}, {108, -0.5}, {116, 0}, {500, 0}, {884, 0}, {892, 0.5}, {900, 1},
		{99, 0}, {901, 0},
	}
	for _, c := range cases {
		if got := edgeSpeed(c.pos, 100, 900); !near(got, c.want) {
			t.Errorf("edgeSpeed(%v) = %v, want %v", c.pos, got, c.want)
		}
	}
	if edgeSpeed(5, 0, 20) != 0 {
		t.Error("tiny view scrolls")
	}
}

func TestEdgeScroll(t *testing.T) {
	view := rl.NewRectangle(0, 0, 800, 600)
	l := layoutView(view, 2560, 600, modeOne, 1, rl.NewVector2(100, 0), false)
	s := edgeScroll(l, rl.NewVector2(799, 300), 0.1, 1)
	if s.X <= 100 || s.Y != 0 {
		t.Errorf("right edge scroll %+v", s)
	}
	s = edgeScroll(l, rl.NewVector2(0, 300), 1, 1)
	if s.X != 0 {
		t.Errorf("left edge clamps: %+v", s)
	}
	s = edgeScroll(l, rl.NewVector2(400, 0), 1, 1)
	if s != l.scroll {
		t.Errorf("no vertical room, yet %+v", s)
	}
	// Twice the speed in remote pixels on a 2x screen.
	a := edgeScroll(l, rl.NewVector2(799, 300), 0.01, 1)
	b := edgeScroll(l, rl.NewVector2(799, 300), 0.01, 2)
	if !near(b.X-100, 2*(a.X-100)) {
		t.Errorf("dpi speed %v vs %v", a.X, b.X)
	}
}

func TestThumb(t *testing.T) {
	pos, length := thumb(400, 800, 3200, 0)
	if pos != 0 || length != 100 {
		t.Errorf("thumb %v %v", pos, length)
	}
	pos, _ = thumb(400, 800, 3200, 2400)
	if pos != 300 {
		t.Errorf("end pos %v", pos)
	}
	_, length = thumb(400, 10, 100000, 0)
	if length != minThumb {
		t.Errorf("min thumb %v", length)
	}
	if pos, length := thumb(400, 800, 600, 0); pos != 0 || length != 400 {
		t.Errorf("no scroll %v %v", pos, length)
	}
	if got := thumbDrag(400, 800, 3200, 0, 150); got != 1200 {
		t.Errorf("drag %v", got)
	}
	if got := thumbDrag(400, 800, 3200, 2000, 1000); got != 2400 {
		t.Errorf("drag clamp %v", got)
	}
	if got := thumbJump(400, 800, 3200, 200); got != 1200 {
		t.Errorf("jump %v", got)
	}
	if got := thumbJump(400, 800, 3200, 0); got != 0 {
		t.Errorf("jump clamp %v", got)
	}
}

func TestResizeToComputer(t *testing.T) {
	// Fits: remote size plus chrome.
	r := resizeToComputer(1280, 720, 1, 1, 301, 85, 1920, 1080, 760, 480)
	if r != (resizeTarget{w: 1581, h: 805, fits: true}) {
		t.Errorf("fit %+v", r)
	}
	// 2x screen: remote pixels are halved in logical units.
	r = resizeToComputer(1280, 720, 2, 1, 0, 85, 3840, 2160, 760, 480)
	if r != (resizeTarget{w: 760, h: 480, fits: true}) {
		t.Errorf("hidpi %+v", r)
	}
	// Window units in physical pixels.
	r = resizeToComputer(1600, 1000, 2, 2, 100, 85, 3840, 2160, 760, 480)
	if r != (resizeTarget{w: 1800, h: 1170, fits: true}) {
		t.Errorf("physical units %+v", r)
	}
	// Too big: clamped to the monitor minus the decorations.
	r = resizeToComputer(2560, 1440, 1, 1, 301, 85, 1920, 1080, 760, 480)
	if r != (resizeTarget{w: 1920 - decorW, h: 1080 - decorH, fits: false}) {
		t.Errorf("clamped %+v", r)
	}
	// Small screens keep the minimum window size.
	r = resizeToComputer(320, 200, 1, 1, 0, 85, 1920, 1080, 760, 480)
	if r != (resizeTarget{w: 760, h: 480, fits: true}) {
		t.Errorf("min %+v", r)
	}
}

func TestHostFilter(t *testing.T) {
	type ev struct {
		code         string
		down, repeat bool
	}
	k := func(code string, down bool) input.Key { return input.Key{Code: code, Down: down} }
	cases := []struct {
		name string
		evs  []ev
		keys []input.Key
		acts []hostAction
	}{
		{"plain key", []ev{{"KeyA", true, false}, {"KeyA", false, false}},
			[]input.Key{k("KeyA", true), k("KeyA", false)}, nil},
		{"left ctrl is never the host key", []ev{{"ControlLeft", true, false}, {"KeyF", true, false}, {"KeyF", false, false}, {"ControlLeft", false, false}},
			[]input.Key{k("ControlLeft", true), k("KeyF", true), k("KeyF", false), k("ControlLeft", false)}, nil},
		{"shortcut", []ev{{hostKey, true, false}, {"KeyF", true, false}, {"KeyF", false, false}, {hostKey, false, false}},
			nil, []hostAction{actFullscreen}},
		{"two shortcuts in one hold, repeats ignored", []ev{{hostKey, true, false}, {hostKey, true, true}, {"KeyS", true, false}, {"KeyS", true, true}, {"KeyS", false, false}, {"KeyR", true, false}, {"KeyR", false, false}, {hostKey, false, false}},
			nil, []hostAction{actToggleMode, actResize}},
		{"lone tap", []ev{{hostKey, true, false}, {hostKey, false, false}},
			[]input.Key{k(hostKey, true), k(hostKey, false)}, nil},
		{"remote ctrl shortcut", []ev{{hostKey, true, false}, {"KeyC", true, false}, {"KeyC", false, false}, {hostKey, false, false}},
			[]input.Key{k(hostKey, true), k("KeyC", true), k("KeyC", false), k(hostKey, false)}, nil},
		{"shortcut after forwarding still runs", []ev{{hostKey, true, false}, {"KeyC", true, false}, {"KeyC", false, false}, {"KeyS", true, false}, {"KeyS", false, false}, {hostKey, false, false}},
			[]input.Key{k(hostKey, true), k("KeyC", true), k("KeyC", false), k(hostKey, false)}, []hostAction{actToggleMode}},
		{"shortcut key released after host key", []ev{{hostKey, true, false}, {"KeyF", true, false}, {hostKey, false, false}, {"KeyF", true, true}, {"KeyF", false, false}, {"KeyF", true, false}},
			[]input.Key{k("KeyF", true)}, []hostAction{actFullscreen}},
		{"host key repeat while forwarded", []ev{{hostKey, true, false}, {"KeyV", true, false}, {hostKey, true, true}, {"KeyV", false, false}, {hostKey, false, false}},
			[]input.Key{k(hostKey, true), k("KeyV", true), k(hostKey, true), k("KeyV", false), k(hostKey, false)}, nil},
	}
	for _, c := range cases {
		var f hostFilter
		var keys []input.Key
		var acts []hostAction
		for _, e := range c.evs {
			out, act := f.key(e.code, e.down, e.repeat)
			keys = append(keys, out...)
			if act != actNone {
				acts = append(acts, act)
			}
		}
		if !reflect.DeepEqual(keys, c.keys) {
			t.Errorf("%s: keys %v, want %v", c.name, keys, c.keys)
		}
		if !reflect.DeepEqual(acts, c.acts) {
			t.Errorf("%s: actions %v, want %v", c.name, acts, c.acts)
		}
		if f.held() {
			t.Errorf("%s: host key still held", c.name)
		}
	}
}

func TestHostFilterFlush(t *testing.T) {
	var f hostFilter
	f.key(hostKey, true, false)
	if got := f.flush(); !reflect.DeepEqual(got, []input.Key{{Code: hostKey, Down: true}}) {
		t.Errorf("flush %v", got)
	}
	if got := f.flush(); got != nil {
		t.Errorf("second flush %v", got)
	}
	if out, _ := f.key(hostKey, false, false); !reflect.DeepEqual(out, []input.Key{{Code: hostKey}}) {
		t.Errorf("release after flush %v", out)
	}
	// Reset drops a pending host key without sending anything.
	f.key(hostKey, true, false)
	f.key("KeyF", true, false)
	f.reset()
	if f.held() {
		t.Error("held after reset")
	}
	if out, _ := f.key("KeyF", false, false); !reflect.DeepEqual(out, []input.Key{{Code: "KeyF"}}) {
		t.Errorf("release after reset %v", out)
	}
}

func TestCursorChoice(t *testing.T) {
	cases := []struct {
		over, known, visible, shape bool
		want                        cursorDraw
	}{
		{true, true, true, true, cursorShapeAtMouse},
		{true, true, false, true, cursorShapeAtMouse}, // hidden remotely: still drawn locally
		{true, false, false, false, cursorArrowAtMouse},
		{true, true, true, false, cursorArrowAtMouse},
		{true, true, false, false, cursorArrowAtMouse},
		{false, true, true, true, cursorShapeAtRemote},
		{false, true, false, true, cursorNone},
		{false, true, true, false, cursorNone},
		{false, false, true, true, cursorNone},
	}
	for _, c := range cases {
		if got := cursorChoice(c.over, c.known, c.visible, c.shape); got != c.want {
			t.Errorf("cursorChoice(%v, %v, %v, %v) = %v, want %v", c.over, c.known, c.visible, c.shape, got, c.want)
		}
	}
}

func TestValidComputerName(t *testing.T) {
	long := ""
	for range maxComputerName {
		long += "é"
	}
	for _, s := range []string{"a", "Anna laptop", "anna@ANNA-PC", "  padded  ", long} {
		if !validComputerName(s) {
			t.Errorf("%q rejected", s)
		}
	}
	for _, s := range []string{"", "   ", long + "x", "tab\there", "nl\nx", "\x00"} {
		if validComputerName(s) {
			t.Errorf("%q accepted", s)
		}
	}
}
