package injector

import (
	"fmt"
	"image"
	"slices"
	"testing"

	"github.com/helpbuoy/helpbuoy/proto/input"
)

type portalRecorder struct{ calls []string }

func (p *portalRecorder) add(f string, a ...any) error {
	p.calls = append(p.calls, fmt.Sprintf(f, a...))
	return nil
}
func (p *portalRecorder) PointerMotionAbsolute(s uint32, x, y float64) error {
	return p.add("move %d %g,%g", s, x, y)
}
func (p *portalRecorder) PointerButton(b int32, down bool) error {
	return p.add("button %#x %v", b, down)
}
func (p *portalRecorder) PointerAxisDiscrete(axis uint32, steps int32) error {
	return p.add("axis %d %d", axis, steps)
}
func (p *portalRecorder) KeyboardKeycode(c int32, down bool) error {
	return p.add("key %d %v", c, down)
}
func (p *portalRecorder) KeyboardKeysym(s int32, down bool) error {
	return p.add("sym %#x %v", s, down)
}

func TestPortalBackend(t *testing.T) {
	p := &portalRecorder{}
	j := NewPortal(nil, nil, p, 7, image.Pt(1280, 720))
	j.SetMonitor(image.Rect(0, 0, 1280, 720))
	j.msg(t, input.MouseMove{X: 65535, Y: 0})
	j.msg(t, input.MouseButton{Button: input.ButtonRight, Down: true})
	j.msg(t, input.MouseButton{Button: input.ButtonRight, Down: false})
	j.msg(t, input.Wheel{DY: 120, DX: -120})
	j.msg(t, input.Key{Code: "KeyA", Down: true})
	j.msg(t, input.Text{Text: "é\n"})
	j.msg(t, input.MouseButton{Button: input.ButtonBack, Down: true})
	j.Close() // releases KeyA and the back button
	want := []string{
		"move 7 1279,0",
		"button 0x111 true", "button 0x111 false",
		"axis 0 -1", "axis 1 -1",
		"key 30 true",
		"sym 0xe9 true", "sym 0xe9 false", "key 28 true", "key 28 false",
		"button 0x113 true",
		"button 0x113 false", "key 30 false",
	}
	if !slices.Equal(p.calls, want) {
		t.Fatalf("got\n%q\nwant\n%q", p.calls, want)
	}
}
