package views

import (
	"testing"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/helpbuoy/helpbuoy/desk/config"
	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/proto/i18n"
	"github.com/helpbuoy/helpbuoy/proto/input"
	"github.com/helpbuoy/helpbuoy/proto/screen"
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
	if got := monitorTip(screen.Monitor{W: 1920, H: 1080, Primary: true, Name: "DELL"}); got != i18n.Trans("{0}, primary", "DELL, 1920 x 1080") {
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

func TestSortedTechiesAndHelping(t *testing.T) {
	p := &control.Presence{
		Techies:   []control.TechieInfo{{Name: "zoe", Online: true}, {Name: "bob"}, {Name: "Alice", Online: true}},
		Computers: []control.ComputerInfo{{Name: "jan@PC", InSessionWith: "zoe"}, {Name: "anna@PC", InSessionWith: "zoe"}, {Name: "piet@PC"}},
	}
	hs := sortedTechies(p)
	if len(hs) != 3 || hs[0].Name != "Alice" || hs[1].Name != "zoe" || hs[2].Name != "bob" {
		t.Fatalf("order %+v", hs)
	}
	if got := helping(p, "zoe"); len(got) != 2 || got[0] != "anna@PC" || got[1] != "jan@PC" {
		t.Fatalf("helping %q", got)
	}
	if got := helping(p, "bob"); len(got) != 0 {
		t.Fatalf("bob helping %q", got)
	}
}

func TestTechieNames(t *testing.T) {
	for comment, want := range map[string]string{"bob@PC": "bob", "Bob Smith@pc": "", "": "", "alice": "alice"} {
		if got := techieNameFrom(comment); got != want {
			t.Errorf("techieNameFrom(%q) = %q, want %q", comment, got, want)
		}
	}
	if validTechieName("with space") || !validTechieName("bob") || validTechieName("") {
		t.Error("validTechieName")
	}
}

func TestFieldText(t *testing.T) {
	for in, want := range map[string]string{"localhost": "", "localhost:2300": "localhost:2300", "helpme.example.com": "helpme.example.com:443"} {
		ad, err := config.ParseAddr(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := fieldText(ad); got != want {
			t.Errorf("fieldText(%s) = %q, want %q", in, got, want)
		}
	}
}
