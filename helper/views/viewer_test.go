package views

import (
	"testing"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/mevdschee/help-me-win/proto/input"
	"github.com/mevdschee/help-me-win/proto/screen"
)

func TestWheel(t *testing.T) {
	cases := []struct {
		v    rl.Vector2
		want input.Wheel
	}{
		{rl.NewVector2(0, 1), input.Wheel{DY: 120}},    // away from the user
		{rl.NewVector2(0, -2), input.Wheel{DY: -240}},  // toward the user
		{rl.NewVector2(-1, 0), input.Wheel{DX: 120}},   // GLFW negative X is right
		{rl.NewVector2(0.25, 0), input.Wheel{DX: -30}}, // touchpads give fractions
		{rl.NewVector2(0, 1e6), input.Wheel{DY: 32767}},
	}
	for _, c := range cases {
		if got := wheel(c.v); got != c.want {
			t.Errorf("wheel(%v) = %+v, want %+v", c.v, got, c.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	if normalize(0, 100) != 0 || normalize(99, 100) != 65535 || normalize(-5, 100) != 0 || normalize(500, 100) != 65535 {
		t.Error("normalize edges")
	}
	if normalize(5, 1) != 0 {
		t.Error("zero-size view")
	}
}

func TestMonitorTip(t *testing.T) {
	if got := monitorTip(screen.Monitor{W: 1920, H: 1080, Primary: true, Name: "DELL"}); got != "DELL, 1920 x 1080, primary" {
		t.Errorf("tip %q", got)
	}
	if got := monitorTip(screen.Monitor{W: 800, H: 600}); got != "800 x 600" {
		t.Errorf("tip %q", got)
	}
}

func TestDemoShapes(t *testing.T) {
	for _, c := range []screen.CursorShape{arrowShape(), beamShape()} {
		if len(c.RGBA) != c.W*c.H*4 || c.HotX >= c.W || c.HotY >= c.H {
			t.Errorf("shape %d: %dx%d hot %d,%d", c.ID, c.W, c.H, c.HotX, c.HotY)
		}
		if _, err := screen.DecodeCursorShape(screen.EncodeCursorShape(c)); err != nil {
			t.Errorf("shape %d: %v", c.ID, err)
		}
	}
}
