package injector

import (
	"fmt"
	"image"
	"log/slog"
	"slices"
	"testing"

	"github.com/mevdschee/helpbuoy/proto/input"
)

// recorder is a backend that records events as strings.
type recorder struct{ evs []string }

func (r *recorder) fake(typ, detail uint8, x, y int) error {
	if typ == xMotionNotify {
		r.evs = append(r.evs, fmt.Sprintf("move %d,%d", x, y))
	} else {
		r.evs = append(r.evs, fmt.Sprintf("%d:%d", typ, detail))
	}
	return nil
}
func (r *recorder) typeRune(c rune) error {
	r.evs = append(r.evs, "type "+string(c))
	return nil
}
func (r *recorder) screen() image.Rectangle { return image.Rect(0, 0, 1000, 500) }
func (r *recorder) flush() error            { return nil }
func (r *recorder) close()                  {}

func newTest() (*Injector, *recorder) {
	r := &recorder{}
	return &Injector{log: slog.Default(), be: r}, r
}

func (j *Injector) msg(t *testing.T, m any) {
	t.Helper()
	typ, b, err := input.Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	j.Input(typ, b)
}

func TestEveryCodeHasAKey(t *testing.T) {
	for _, c := range input.Codes {
		if _, ok := keycode(c); !ok {
			t.Errorf("no key for %q", c)
		}
	}
}

func TestMouse(t *testing.T) {
	j, r := newTest()
	j.msg(t, input.MouseMove{X: 65535, Y: 0})
	j.SetMonitor(image.Rect(1000, 0, 1200, 100))
	j.msg(t, input.MouseMove{X: 0, Y: 65535})
	want := []string{"move 999,0", "move 1000,99"}
	if !slices.Equal(r.evs, want) {
		t.Fatalf("got %v, want %v", r.evs, want)
	}
}

func TestWheelAddsUpNotches(t *testing.T) {
	j, r := newTest()
	j.msg(t, input.Wheel{DY: 60})
	j.msg(t, input.Wheel{DY: 60})
	j.msg(t, input.Wheel{DY: -240, DX: 120})
	want := []string{"4:4", "5:4", "4:5", "5:5", "4:5", "5:5", "4:7", "5:7"}
	if !slices.Equal(r.evs, want) {
		t.Fatalf("got %v, want %v", r.evs, want)
	}
}

func TestReleaseOnlyWhatWasPressed(t *testing.T) {
	j, r := newTest()
	j.msg(t, input.Key{Code: "KeyB", Down: false}) // never pressed: ignored
	j.msg(t, input.Key{Code: "ShiftLeft", Down: true})
	j.msg(t, input.MouseButton{Button: input.ButtonLeft, Down: true})
	j.msg(t, input.MouseButton{Button: input.ButtonLeft, Down: true}) // no second press
	j.Close()
	j.msg(t, input.Key{Code: "KeyA", Down: true}) // after Close: ignored
	want := []string{"2:50", "4:1", "5:1", "3:50"}
	if !slices.Equal(r.evs, want) {
		t.Fatalf("got %v, want %v", r.evs, want)
	}
}

func TestText(t *testing.T) {
	j, r := newTest()
	j.msg(t, input.Text{Text: "a\r\nb\té"})
	want := []string{"type a", "2:36", "3:36", "type b", "2:23", "3:23", "type é"}
	if !slices.Equal(r.evs, want) {
		t.Fatalf("got %v, want %v", r.evs, want)
	}
}

func TestInjectedEscIsMarked(t *testing.T) {
	j, _ := newTest()
	j.msg(t, input.Key{Code: "Escape", Down: true})
	if !escInjected.Load() {
		t.Fatal("Esc down not marked")
	}
	j.msg(t, input.Key{Code: "Escape", Down: false})
	if escInjected.Load() {
		t.Fatal("Esc up not unmarked")
	}
}
