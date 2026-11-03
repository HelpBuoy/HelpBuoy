package capture

import (
	"bytes"
	"fmt"
	"os"
	"testing"
)

// TestShmMatchesGetImage needs an X server: HELPBUOY_X11_TEST=:77.
func TestShmMatchesGetImage(t *testing.T) {
	d := os.Getenv("HELPBUOY_X11_TEST")
	if d == "" {
		t.Skip("set HELPBUOY_X11_TEST to an X display")
	}
	t.Setenv("DISPLAY", d)
	s, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.shm {
		t.Skip("no MIT-SHM")
	}
	ms, err := s.Monitors()
	if err != nil || len(ms) == 0 {
		t.Fatal(ms, err)
	}
	c, err := s.Open(ms[0])
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// A live desktop changes between two captures (a clock, a blinking
	// caret), so a few attempts may be needed.
	var diff string
	for range 5 {
		a, err := c.Capture()
		if err != nil {
			t.Fatal(err)
		}
		b, err := c.(*Capturer).getImage()
		if err != nil {
			t.Fatal(err)
		}
		if a.Rect != b.Rect || len(a.Pix) != len(b.Pix) {
			t.Fatalf("sizes %v and %v", a.Rect, b.Rect)
		}
		diff = ""
		// The fourth byte is padding, which the two paths may fill differently.
		for i := 0; i < len(a.Pix); i += 4 {
			if !bytes.Equal(a.Pix[i:i+3], b.Pix[i:i+3]) {
				diff = fmt.Sprintf("captures differ at byte %d: % x and % x", i, a.Pix[i:i+4], b.Pix[i:i+4])
				break
			}
		}
		if diff == "" {
			break
		}
	}
	if diff != "" {
		t.Fatal(diff)
	}
	if _, err := s.Cursor(); err != nil {
		t.Fatal(err)
	}
}
