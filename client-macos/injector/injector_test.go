package injector

import (
	"fmt"
	"image"
	"reflect"
	"testing"
	"time"

	"github.com/helpbuoy/helpbuoy/proto/input"
)

// recorder is a backend that writes down what it is asked to post.
type recorder struct {
	got []string
}

func (r *recorder) key(code uint16, down bool, flags uint64) error {
	r.got = append(r.got, fmt.Sprintf("key %#x %v %#x", code, down, flags))
	return nil
}

func (r *recorder) mouse(typ, button uint32, x, y float64, clicks int64, flags uint64) error {
	r.got = append(r.got, fmt.Sprintf("mouse %d b%d %v,%v c%d %#x", typ, button, x, y, clicks, flags))
	return nil
}

func (r *recorder) scroll(dy, dx int32, flags uint64) error {
	r.got = append(r.got, fmt.Sprintf("scroll %d %d", dy, dx))
	return nil
}

func (r *recorder) text(s string) error {
	r.got = append(r.got, "text "+s)
	return nil
}

func (r *recorder) doubleClick() time.Duration { return 500 * time.Millisecond }
func (r *recorder) screen() image.Rectangle    { return image.Rect(0, 0, 1441, 901) }
func (r *recorder) close()                     {}

type fixture struct {
	j   *Injector
	rec *recorder
	now time.Time
}

func newFixture() *fixture {
	f := &fixture{rec: &recorder{}, now: time.Unix(1000, 0)}
	f.j = newInjector(nil, nil, f.rec)
	f.j.now = func() time.Time { return f.now }
	return f
}

func (f *fixture) send(t *testing.T, ms ...any) {
	t.Helper()
	for _, m := range ms {
		typ, b, err := input.Encode(m)
		if err != nil {
			t.Fatal(err)
		}
		f.j.Input(typ, b)
	}
}

func (f *fixture) expect(t *testing.T, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(f.rec.got, want) {
		t.Errorf("posted\n%q\nwant\n%q", f.rec.got, want)
	}
	f.rec.got = nil
}

func TestDragAndRelease(t *testing.T) {
	f := newFixture()
	f.send(t,
		input.MouseMove{X: 0, Y: 0},
		input.MouseButton{Button: input.ButtonLeft, Down: true},
		input.MouseMove{X: 65535, Y: 65535},
		input.MouseButton{Button: input.ButtonLeft, Down: false},
		input.MouseMove{X: 0, Y: 0},
	)
	f.expect(t,
		"mouse 5 b0 0,0 c0 0x0",
		"mouse 1 b0 0,0 c1 0x0",
		"mouse 6 b0 1440,900 c0 0x0",
		"mouse 2 b0 1440,900 c1 0x0",
		"mouse 5 b0 0,0 c0 0x0",
	)
}

func TestClickCount(t *testing.T) {
	f := newFixture()
	click := func() {
		f.send(t, input.MouseButton{Button: input.ButtonLeft, Down: true}, input.MouseButton{Button: input.ButtonLeft, Down: false})
	}
	click()
	f.now = f.now.Add(200 * time.Millisecond)
	click()
	f.now = f.now.Add(200 * time.Millisecond)
	click()
	f.expect(t,
		"mouse 1 b0 0,0 c1 0x0", "mouse 2 b0 0,0 c1 0x0",
		"mouse 1 b0 0,0 c2 0x0", "mouse 2 b0 0,0 c2 0x0",
		"mouse 1 b0 0,0 c3 0x0", "mouse 2 b0 0,0 c3 0x0",
	)
	// Too late, then moved away, then another button: each starts over.
	f.now = f.now.Add(time.Second)
	click()
	f.now = f.now.Add(100 * time.Millisecond)
	f.send(t, input.MouseMove{X: 65535, Y: 0})
	click()
	f.now = f.now.Add(100 * time.Millisecond)
	f.send(t, input.MouseButton{Button: input.ButtonRight, Down: true})
	f.expect(t,
		"mouse 1 b0 0,0 c1 0x0", "mouse 2 b0 0,0 c1 0x0",
		"mouse 5 b0 1440,0 c0 0x0",
		"mouse 1 b0 1440,0 c1 0x0", "mouse 2 b0 1440,0 c1 0x0",
		"mouse 3 b1 1440,0 c1 0x0",
	)
}

func TestModifiers(t *testing.T) {
	f := newFixture()
	f.j.SetMonitor(image.Rect(-100, 0, -99, 1))
	f.send(t,
		input.Key{Code: "MetaLeft", Down: true},
		input.Key{Code: "KeyC", Down: true},
		input.Key{Code: "KeyC", Down: false},
		input.MouseButton{Button: input.ButtonLeft, Down: true},
		input.Key{Code: "MetaLeft", Down: false},
		input.Key{Code: "ArrowLeft", Down: true},
	)
	f.expect(t,
		"key 0x37 true 0x100000",
		"key 0x8 true 0x100000",
		"key 0x8 false 0x100000",
		"mouse 1 b0 0,0 c1 0x100000",
		"key 0x37 false 0x0",
		"key 0x7b true 0xa00000",
	)
	// Release all lifts in reverse order of pressing.
	f.j.ReleaseAll()
	f.expect(t, "key 0x7b false 0xa00000", "mouse 2 b0 0,0 c1 0x0")
}

func TestReleaseOnlyWhatWasPressed(t *testing.T) {
	f := newFixture()
	f.send(t,
		input.Key{Code: "ShiftLeft", Down: false},
		input.MouseButton{Button: input.ButtonLeft, Down: false},
		input.Key{Code: "Power", Down: true},
	)
	f.expect(t)
}

func TestAutoRepeat(t *testing.T) {
	f := newFixture()
	f.send(t,
		input.Key{Code: "KeyA", Down: true},
		input.Key{Code: "KeyA", Down: true},
	)
	f.j.Close()
	f.expect(t, "key 0x0 true 0x0", "key 0x0 true 0x0", "key 0x0 false 0x0")
}

func TestTextAndWheel(t *testing.T) {
	f := newFixture()
	f.send(t,
		input.Text{Text: "é\r\nx\t"},
		input.Wheel{DY: 60},
		input.Wheel{DY: 60, DX: 240},
		input.Wheel{DY: -120},
	)
	f.expect(t,
		"text é",
		"key 0x24 true 0x0", "key 0x24 false 0x0",
		"text x",
		"key 0x30 true 0x0", "key 0x30 false 0x0",
		"scroll 1 -2",
		"scroll -1 0",
	)
}
