package injector

import (
	"errors"
	"image"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mevdschee/helpbuoy/proto/input"
)

// recorder collects the batches an Injector sends.
type recorder struct {
	mu      sync.Mutex
	batches [][]event
	err     error
}

func (r *recorder) send(evs []event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.batches = append(r.batches, append([]event(nil), evs...))
	return nil
}

func (r *recorder) all() []event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var all []event
	for _, b := range r.batches {
		all = append(all, b...)
	}
	return all
}

func (r *recorder) reset() {
	r.mu.Lock()
	r.batches = nil
	r.mu.Unlock()
}

var (
	testVirtual = image.Rect(-1920, -200, 2560, 1440)
	testPrimary = image.Rect(0, 0, 2560, 1440)
)

func newTest(t *testing.T) (*Injector, *recorder, *atomic.Int32) {
	t.Helper()
	rec := &recorder{}
	var n atomic.Int32
	j := New(nil, func() { n.Add(1) })
	j.send = rec.send
	j.screens = func() (image.Rectangle, image.Rectangle) { return testVirtual, testPrimary }
	return j, rec, &n
}

func feed(t *testing.T, j *Injector, m any) {
	t.Helper()
	typ, b, err := input.Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	j.Input(typ, b)
}

func TestExtendedFlags(t *testing.T) {
	ext := []string{
		"ControlRight", "AltRight", "MetaLeft", "MetaRight", "ContextMenu",
		"Insert", "Delete", "Home", "End", "PageUp", "PageDown",
		"ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight",
		"NumpadEnter", "NumpadDivide", "NumLock", "PrintScreen",
	}
	plain := []string{
		"ControlLeft", "AltLeft", "ShiftLeft", "ShiftRight", "Enter", "KeyA",
		"Numpad0", "Numpad7", "NumpadDecimal", "NumpadMultiply", "NumpadAdd",
		"NumpadSubtract", "ScrollLock", "CapsLock", "Escape", "IntlBackslash",
	}
	for _, c := range ext {
		k, ok := lookupKey(c)
		if !ok || !k.Ext {
			t.Errorf("%s: want extended, got %+v", c, k)
		}
		if e := keyEvent(k, true); k.VK == 0 && e.flags&keyeventfExtendedKey == 0 {
			t.Errorf("%s: event lacks KEYEVENTF_EXTENDEDKEY", c)
		}
	}
	for _, c := range plain {
		if k, ok := lookupKey(c); !ok || k.Ext {
			t.Errorf("%s: want plain, got %+v", c, k)
		}
	}
}

func TestTableScanCodes(t *testing.T) {
	// A sample of well-known set 1 make codes.
	want := map[string]uint16{
		"Escape": 0x01, "Digit1": 0x02, "Digit0": 0x0b, "KeyQ": 0x10, "KeyA": 0x1e,
		"KeyZ": 0x2c, "Space": 0x39, "F1": 0x3b, "F10": 0x44, "F11": 0x57, "F12": 0x58,
		"F13": 0x64, "F24": 0x76, "ArrowUp": 0x48, "Delete": 0x53, "NumLock": 0x45,
		"IntlBackslash": 0x56, "MetaLeft": 0x5b, "Backquote": 0x29, "Backslash": 0x2b,
	}
	for c, s := range want {
		if k, _ := lookupKey(c); k.Scan != s {
			t.Errorf("%s: scan %#x, want %#x", c, k.Scan, s)
		}
	}
	// Scan code plus extended flag must be unique among scan-code keys,
	// apart from the documented aliases.
	alias := map[string]bool{"IntlHash": true, "Lang5": true}
	seen := map[[2]uint16]string{}
	for c, k := range keyTable {
		if k.VK != 0 || alias[c] {
			continue
		}
		var e uint16
		if k.Ext {
			e = 1
		}
		if prev, ok := seen[[2]uint16{k.Scan, e}]; ok {
			t.Errorf("%s and %s share scan code %#x ext=%v", c, prev, k.Scan, k.Ext)
		}
		seen[[2]uint16{k.Scan, e}] = c
		if k.Scan == 0 || k.Scan > 0x7f {
			t.Errorf("%s: scan %#x out of range", c, k.Scan)
		}
	}
	for _, c := range []string{"Power", "Sleep", "WakeUp"} {
		if _, ok := lookupKey(c); ok {
			t.Errorf("%s must not be injectable", c)
		}
	}
}

func TestKeyEvents(t *testing.T) {
	j, rec, n := newTest(t)
	feed(t, j, input.Key{Code: "KeyA", Down: true})
	feed(t, j, input.Key{Code: "KeyA"})
	feed(t, j, input.Key{Code: "ArrowLeft", Down: true})
	feed(t, j, input.Key{Code: "Pause", Down: true})
	got := rec.all()
	want := []event{
		{scan: 0x1e, flags: keyeventfScancode},
		{scan: 0x1e, flags: keyeventfScancode | keyeventfKeyUp},
		{scan: 0x4b, flags: keyeventfScancode | keyeventfExtendedKey},
		{scan: 0x45, vk: 0x13},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	if n.Load() != 4 {
		t.Errorf("OnInjected called %d times, want 4", n.Load())
	}
}

func TestUnknownAndStrayKeys(t *testing.T) {
	j, rec, n := newTest(t)
	feed(t, j, input.Key{Code: "NoSuchKey", Down: true})
	feed(t, j, input.Key{Code: "KeyB"}) // never pressed
	feed(t, j, input.MouseButton{Button: input.ButtonLeft})
	j.Input(input.MsgKey, []byte{7})
	if len(rec.all()) != 0 || n.Load() != 0 {
		t.Fatalf("sent %+v", rec.all())
	}
}

func TestReleaseAllOrder(t *testing.T) {
	j, rec, _ := newTest(t)
	feed(t, j, input.Key{Code: "ShiftLeft", Down: true})
	feed(t, j, input.MouseButton{Button: input.ButtonLeft, Down: true})
	feed(t, j, input.Key{Code: "ControlRight", Down: true})
	feed(t, j, input.Key{Code: "KeyX", Down: true})
	feed(t, j, input.Key{Code: "KeyX", Down: true}) // auto-repeat
	feed(t, j, input.MouseButton{Button: input.ButtonForward, Down: true})
	feed(t, j, input.MouseButton{Button: input.ButtonForward, Down: true}) // ignored
	feed(t, j, input.Key{Code: "KeyX"})
	rec.reset()

	feed(t, j, input.ReleaseAll{})
	want := []event{
		{mouse: true, flags: mouseeventfXUp, data: xbutton2},
		{scan: 0x1d, flags: keyeventfScancode | keyeventfExtendedKey | keyeventfKeyUp},
		{mouse: true, flags: mouseeventfLeftUp},
		{scan: 0x2a, flags: keyeventfScancode | keyeventfKeyUp},
	}
	if len(rec.batches) != 1 {
		t.Fatalf("want one release batch, got %d", len(rec.batches))
	}
	got := rec.batches[0]
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("release %d: %+v, want %+v", i, got[i], want[i])
		}
	}

	rec.reset()
	j.ReleaseAll()
	if len(rec.all()) != 0 {
		t.Errorf("second release sent %+v", rec.all())
	}
}

func TestClose(t *testing.T) {
	j, rec, _ := newTest(t)
	feed(t, j, input.Key{Code: "AltLeft", Down: true})
	feed(t, j, input.MouseButton{Button: input.ButtonRight, Down: true})
	rec.reset()
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	got := rec.all()
	if len(got) != 2 || got[0].flags != mouseeventfRightUp || got[1].scan != 0x38 || got[1].flags&keyeventfKeyUp == 0 {
		t.Fatalf("close released %+v", got)
	}
	rec.reset()
	feed(t, j, input.Key{Code: "KeyA", Down: true})
	feed(t, j, input.MouseMove{X: 1, Y: 1})
	j.ReleaseAll()
	j.Close()
	if len(rec.all()) != 0 {
		t.Fatalf("input after Close sent %+v", rec.all())
	}
}

func TestSendErrorLoggedAndNotHeld(t *testing.T) {
	j, rec, n := newTest(t)
	rec.err = errors.New("Access is denied.")
	for range 3 {
		feed(t, j, input.Key{Code: "KeyA", Down: true})
	}
	if len(j.logged) != 1 {
		t.Errorf("logged %v, want one entry", j.logged)
	}
	if len(j.held) != 0 || n.Load() != 0 {
		t.Errorf("held %v after failed sends, OnInjected %d", j.held, n.Load())
	}
}

func TestLogOnlyWithoutSender(t *testing.T) {
	j := New(nil, func() { t.Error("OnInjected without injection") })
	j.send = nil
	feed(t, j, input.Key{Code: "KeyA", Down: true})
	feed(t, j, input.MouseMove{})
	j.ReleaseAll()
	j.Close()
}

func TestButtons(t *testing.T) {
	cases := []struct {
		b        uint8
		dn, up   uint32
		data     uint32
		knownBtn bool
	}{
		{input.ButtonLeft, mouseeventfLeftDown, mouseeventfLeftUp, 0, true},
		{input.ButtonRight, mouseeventfRightDown, mouseeventfRightUp, 0, true},
		{input.ButtonMiddle, mouseeventfMiddleDown, mouseeventfMiddleUp, 0, true},
		{input.ButtonBack, mouseeventfXDown, mouseeventfXUp, xbutton1, true},
		{input.ButtonForward, mouseeventfXDown, mouseeventfXUp, xbutton2, true},
		{9, 0, 0, 0, false},
	}
	for _, c := range cases {
		d, ok := buttonEvent(c.b, true)
		u, _ := buttonEvent(c.b, false)
		if ok != c.knownBtn {
			t.Errorf("button %d: ok %v", c.b, ok)
			continue
		}
		if ok && (d.flags != c.dn || u.flags != c.up || d.data != c.data || u.data != c.data || !d.mouse) {
			t.Errorf("button %d: down %+v up %+v", c.b, d, u)
		}
	}
}

func TestWheelSigns(t *testing.T) {
	evs := wheelEvents(input.Wheel{DX: 120, DY: -240})
	if len(evs) != 2 {
		t.Fatalf("got %+v", evs)
	}
	if evs[0].flags != mouseeventfWheel || int32(evs[0].data) != -240 {
		t.Errorf("vertical %+v", evs[0])
	}
	if evs[1].flags != mouseeventfHWheel || int32(evs[1].data) != 120 {
		t.Errorf("horizontal %+v", evs[1])
	}
	evs = wheelEvents(input.Wheel{DY: 120})
	if len(evs) != 1 || int32(evs[0].data) != 120 {
		t.Errorf("up one notch %+v", evs)
	}
	if len(wheelEvents(input.Wheel{})) != 0 {
		t.Error("zero wheel sends events")
	}
	j, rec, n := newTest(t)
	feed(t, j, input.Wheel{})
	if len(rec.all()) != 0 || n.Load() != 0 {
		t.Error("zero wheel injected")
	}
	feed(t, j, input.Wheel{DX: -32768, DY: 32767})
	got := rec.all()
	if len(got) != 2 || int32(got[0].data) != 32767 || int32(got[1].data) != -32768 {
		t.Errorf("extremes %+v", got)
	}
}

func TestTextUnicode(t *testing.T) {
	evs := textEvents("aé😀")
	want := []event{
		{scan: 'a', flags: keyeventfUnicode},
		{scan: 'a', flags: keyeventfUnicode | keyeventfKeyUp},
		{scan: 0xe9, flags: keyeventfUnicode},
		{scan: 0xe9, flags: keyeventfUnicode | keyeventfKeyUp},
		{scan: 0xd83d, flags: keyeventfUnicode},
		{scan: 0xd83d, flags: keyeventfUnicode | keyeventfKeyUp},
		{scan: 0xde00, flags: keyeventfUnicode},
		{scan: 0xde00, flags: keyeventfUnicode | keyeventfKeyUp},
	}
	if len(evs) != len(want) {
		t.Fatalf("got %+v", evs)
	}
	for i := range want {
		if evs[i] != want[i] {
			t.Errorf("event %d: %+v, want %+v", i, evs[i], want[i])
		}
	}
}

func TestTextControls(t *testing.T) {
	enter := keyTable["Enter"]
	tab := keyTable["Tab"]
	evs := textEvents("a\r\nb\nc\r\td")
	var keys []uint16
	for _, e := range evs {
		if e.flags&keyeventfKeyUp != 0 {
			continue
		}
		if e.flags&keyeventfUnicode != 0 {
			keys = append(keys, e.scan)
		} else {
			keys = append(keys, 0x100|e.scan)
		}
	}
	E, T := 0x100|enter.Scan, 0x100|tab.Scan
	want := []uint16{'a', E, 'b', E, 'c', E, T, 'd'}
	if len(keys) != len(want) {
		t.Fatalf("got %x, want %x", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("got %x, want %x", keys, want)
		}
	}
}

// windowsPixel models how Windows maps absolute coordinates to a pixel. Two
// formulas are in circulation; the injector must land on the same pixel
// with either.
func windowsPixel(abs int32, origin, size int, rounded bool) int {
	if rounded {
		return origin + int((int64(abs)*int64(size-1)+32767)/65535)
	}
	return origin + int(int64(abs)*int64(size)/65536)
}

func TestAbsoluteMapping(t *testing.T) {
	type tc struct {
		name      string
		mon, virt image.Rectangle
	}
	cases := []tc{
		{"single", image.Rect(0, 0, 1920, 1080), image.Rect(0, 0, 1920, 1080)},
		{"left of primary", image.Rect(-1920, 0, 0, 1080), image.Rect(-1920, 0, 2560, 1440)},
		{"above primary", image.Rect(-300, -1200, 1620, 0), image.Rect(-300, -1200, 2560, 1440)},
		{"right secondary", image.Rect(2560, 100, 3840, 1124), image.Rect(0, 0, 3840, 1440)},
		{"primary in negative desk", image.Rect(0, 0, 2560, 1440), image.Rect(-1920, -200, 2560, 1440)},
		{"odd sizes", image.Rect(-1023, -7, 0, 761), image.Rect(-1023, -7, 3001, 2000)},
	}
	for _, c := range cases {
		w, h := c.mon.Dx(), c.mon.Dy()
		// The helper maps its pixel v of a w-pixel view to v/(w-1)*65535;
		// with matching sizes every monitor pixel must be hit exactly.
		for px := 0; px < w; px++ {
			n := uint16((int64(px)*65535 + int64(w-1)/2) / int64(w-1))
			ax, _ := absolute(n, 0, c.mon, c.virt)
			for _, rounded := range []bool{false, true} {
				if got := windowsPixel(ax, c.virt.Min.X, c.virt.Dx(), rounded); got != c.mon.Min.X+px {
					t.Fatalf("%s: x pixel %d -> abs %d -> %d (rounded=%v), want %d", c.name, px, ax, got, rounded, c.mon.Min.X+px)
				}
			}
		}
		for py := 0; py < h; py++ {
			n := uint16((int64(py)*65535 + int64(h-1)/2) / int64(h-1))
			_, ay := absolute(0, n, c.mon, c.virt)
			for _, rounded := range []bool{false, true} {
				if got := windowsPixel(ay, c.virt.Min.Y, c.virt.Dy(), rounded); got != c.mon.Min.Y+py {
					t.Fatalf("%s: y pixel %d -> %d (rounded=%v), want %d", c.name, py, got, rounded, c.mon.Min.Y+py)
				}
			}
		}
		// Corners stay inside 0..65535.
		for _, n := range []uint16{0, 65535} {
			x, y := absolute(n, n, c.mon, c.virt)
			if x < 0 || x > 65535 || y < 0 || y > 65535 {
				t.Errorf("%s: corner %d -> %d,%d", c.name, n, x, y)
			}
		}
	}
}

func TestAbsoluteClampsAndFallbacks(t *testing.T) {
	virt := image.Rect(0, 0, 1000, 1000)
	// A monitor that no longer fits the desktop is clamped to its edge.
	x, y := absolute(65535, 65535, image.Rect(500, 500, 2000, 2000), virt)
	if x != (999*65536+32768)/1000 || y != x {
		t.Errorf("clamp: %d,%d", x, y)
	}
	// Without a virtual desktop the monitor itself is used.
	x, _ = absolute(65535, 0, image.Rect(0, 0, 100, 100), image.Rectangle{})
	if windowsPixel(x, 0, 100, false) != 99 {
		t.Errorf("fallback x %d", x)
	}
	// Nothing known at all gives the origin.
	if x, y := absolute(30000, 30000, image.Rectangle{}, image.Rectangle{}); x != 0 || y != 0 {
		t.Errorf("empty: %d,%d", x, y)
	}
}

func TestMouseMoveUsesMonitor(t *testing.T) {
	j, rec, _ := newTest(t)
	feed(t, j, input.MouseMove{X: 0, Y: 0})
	j.SetMonitor(image.Rect(-1920, -200, 0, 880))
	feed(t, j, input.MouseMove{X: 65535, Y: 65535})
	got := rec.all()
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	for _, e := range got {
		if !e.mouse || e.flags != mouseeventfMove|mouseeventfAbsolute|mouseeventfVirtualDesk {
			t.Errorf("flags %+v", e)
		}
	}
	// Before SetMonitor: the primary monitor's top-left pixel (0,0).
	if px := windowsPixel(got[0].dx, testVirtual.Min.X, testVirtual.Dx(), false); px != 0 {
		t.Errorf("primary origin maps to x %d", px)
	}
	if py := windowsPixel(got[0].dy, testVirtual.Min.Y, testVirtual.Dy(), false); py != 0 {
		t.Errorf("primary origin maps to y %d", py)
	}
	// After: the bottom-right pixel of the left monitor (-1, 879).
	if px := windowsPixel(got[1].dx, testVirtual.Min.X, testVirtual.Dx(), false); px != -1 {
		t.Errorf("left monitor corner maps to x %d", px)
	}
	if py := windowsPixel(got[1].dy, testVirtual.Min.Y, testVirtual.Dy(), false); py != 879 {
		t.Errorf("left monitor corner maps to y %d", py)
	}
}

func TestConcurrent(t *testing.T) {
	j, rec, _ := newTest(t)
	var wg sync.WaitGroup
	codes := []string{"KeyA", "KeyB", "ShiftLeft", "ControlRight", "Numpad5"}
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 300 {
				c := codes[(g+i)%len(codes)]
				feed(t, j, input.Key{Code: c, Down: i%3 != 2})
				feed(t, j, input.MouseButton{Button: uint8(i % 5), Down: i%2 == 0})
				feed(t, j, input.MouseMove{X: uint16(i * 200), Y: uint16(g * 8000)})
				j.SetMonitor(image.Rect(0, 0, 100+i, 100+g))
				if i%50 == 0 {
					j.ReleaseAll()
				}
			}
		}()
	}
	wg.Wait()
	j.Close()

	// After Close, every key and button that went down has come up.
	down := map[[3]uint32]int{}
	for _, e := range rec.all() {
		var id [3]uint32
		var isDown, isUp bool
		if e.mouse {
			switch {
			case e.flags&mouseeventfMove != 0, e.flags&(mouseeventfWheel|mouseeventfHWheel) != 0:
				continue
			case e.flags&(mouseeventfLeftDown|mouseeventfRightDown|mouseeventfMiddleDown|mouseeventfXDown) != 0:
				isDown = true
				id = [3]uint32{1, e.flags, e.data}
			default:
				isUp = true
				id = [3]uint32{1, e.flags >> 1, e.data} // the matching down flag
			}
		} else {
			id = [3]uint32{0, uint32(e.scan), e.flags &^ keyeventfKeyUp}
			isUp = e.flags&keyeventfKeyUp != 0
			isDown = !isUp
		}
		if isDown {
			down[id] = 1 // repeats keep one press
		} else if isUp {
			if down[id] == 0 {
				t.Fatalf("up without down: %+v", e)
			}
			down[id] = 0
		}
	}
	for id, v := range down {
		if v != 0 {
			t.Errorf("still down after Close: %v", id)
		}
	}
	if len(j.held) != 0 {
		t.Errorf("held after Close: %v", j.held)
	}
}
