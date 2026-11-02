package capture

import (
	"bytes"
	"image"
	"testing"
)

func TestDefaultMonitor(t *testing.T) {
	ms := []Monitor{
		{Rect: image.Rect(-1920, 0, 0, 1080), Name: "DISPLAY2"},
		{Rect: image.Rect(0, 0, 2560, 1440), Primary: true, Name: "DISPLAY1"},
	}
	at := image.Pt(-5, 10)
	if i := DefaultMonitor(ms, &at); i != 0 {
		t.Errorf("under cursor: %d", i)
	}
	off := image.Pt(99999, 0)
	if i := DefaultMonitor(ms, &off); i != 1 {
		t.Errorf("cursor nowhere: %d", i)
	}
	if i := DefaultMonitor(ms, nil); i != 1 {
		t.Errorf("no cursor: %d", i)
	}
	if i := DefaultMonitor(ms[:1], nil); i != 0 {
		t.Errorf("no primary: %d", i)
	}
	if i := DefaultMonitor(nil, nil); i != -1 {
		t.Errorf("empty: %d", i)
	}
}

func TestFindMonitor(t *testing.T) {
	ms := []Monitor{
		{Rect: image.Rect(0, 0, 10, 10), Primary: true, Name: "A"},
		{Rect: image.Rect(10, 0, 20, 10), Name: "B"},
	}
	if i := FindMonitor(ms, "B", image.Rectangle{}); i != 1 {
		t.Errorf("by name %d", i)
	}
	if i := FindMonitor(ms, "gone", image.Rect(10, 0, 20, 10)); i != 1 {
		t.Errorf("by rect %d", i)
	}
	if i := FindMonitor(ms, "gone", image.Rect(5, 5, 6, 6)); i != 0 {
		t.Errorf("fallback %d", i)
	}
	if !SameMonitors(ms, append([]Monitor(nil), ms...)) || SameMonitors(ms, ms[:1]) {
		t.Error("SameMonitors")
	}
	other := append([]Monitor(nil), ms...)
	other[1].Rect.Max.X++
	if SameMonitors(ms, other) {
		t.Error("SameMonitors ignores the size")
	}
}

// bgra builds 32-bit pixels from B, G, R, A quadruples.
func bgra(px ...[4]byte) []byte {
	var b []byte
	for _, p := range px {
		b = append(b, p[:]...)
	}
	return b
}

var (
	black = [4]byte{0, 0, 0, 0}
	white = [4]byte{255, 255, 255, 0}
)

func TestShapeFromColorMask(t *testing.T) {
	// No alpha anywhere: the mask decides. Pixel 0 opaque red, 1 transparent,
	// 2 an "inverting" pixel drawn as its color, 3 opaque black.
	color := bgra([4]byte{0, 0, 255, 0}, black, [4]byte{0, 255, 0, 0}, black)
	mask := bgra(black, white, white, black)
	got := ShapeFromColor(color, mask, 2, 2)
	want := []byte{255, 0, 0, 255, 0, 0, 0, 0, 0, 255, 0, 255, 0, 0, 0, 255}
	if !bytes.Equal(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestShapeFromColorAlpha(t *testing.T) {
	// An alpha cursor keeps its alpha channel and ignores the mask.
	color := bgra([4]byte{10, 20, 30, 128}, [4]byte{0, 0, 0, 0})
	mask := bgra(white, black)
	got := ShapeFromColor(color, mask, 2, 1)
	want := []byte{30, 20, 10, 128, 0, 0, 0, 0}
	if !bytes.Equal(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestShapeFromMonochrome(t *testing.T) {
	// 3x1 cursor: black, white, transparent; then 3x3 with one inverted
	// pixel in the middle, which gets a white outline.
	mask := bgra(black, black, white /* AND */, black, white, black /* XOR */)
	got := ShapeFromMonochrome(mask, 3, 1)
	want := []byte{0, 0, 0, 255, 255, 255, 255, 255, 0, 0, 0, 0}
	if !bytes.Equal(got, want) {
		t.Errorf("got %v want %v", got, want)
	}

	and := bgra(white, white, white, white, white, white, white, white, white)
	xor := bgra(black, black, black, black, white, black, black, black, black)
	got = ShapeFromMonochrome(append(and, xor...), 3, 3)
	for i := 0; i < 9; i++ {
		p := got[i*4 : i*4+4]
		if i == 4 {
			if !bytes.Equal(p, []byte{0, 0, 0, 255}) {
				t.Errorf("inverted pixel %v", p)
			}
		} else if !bytes.Equal(p, []byte{255, 255, 255, 255}) {
			t.Errorf("outline pixel %d: %v", i, p)
		}
	}
}

func TestShapeIDAndClip(t *testing.T) {
	a := Shape{W: 1, H: 1, RGBA: []byte{1, 2, 3, 4}}
	b := a
	b.HotX = 1
	if ShapeID(a) == ShapeID(b) || ShapeID(a) != ShapeID(Shape{W: 1, H: 1, RGBA: []byte{1, 2, 3, 4}}) {
		t.Error("ShapeID")
	}
	big := Shape{W: 300, H: 2, HotX: 299, RGBA: make([]byte, 300*2*4)}
	big.RGBA[256*4] = 9 // first pixel cut off
	c := ClipShape(big)
	if c.W != 256 || c.H != 2 || len(c.RGBA) != 256*2*4 || c.HotX != 255 {
		t.Fatalf("clip %dx%d hot %d len %d", c.W, c.H, c.HotX, len(c.RGBA))
	}
	for _, v := range c.RGBA {
		if v != 0 {
			t.Fatal("clipped pixel kept")
		}
	}
}

func TestSwapRB(t *testing.T) {
	p := []byte{1, 2, 3, 0, 4, 5, 6, 7}
	SwapRB(p)
	if !bytes.Equal(p, []byte{3, 2, 1, 255, 6, 5, 4, 255}) {
		t.Errorf("%v", p)
	}
}

func TestArrow(t *testing.T) {
	a := Arrow()
	if len(a.RGBA) != a.W*a.H*4 {
		t.Fatal("size")
	}
	var black, white int
	for i := 0; i < len(a.RGBA); i += 4 {
		if a.RGBA[i+3] == 255 {
			if a.RGBA[i] == 0 {
				black++
			} else {
				white++
			}
		}
	}
	if black < 30 || white < 30 {
		t.Errorf("arrow has %d black and %d white pixels", black, white)
	}
}
