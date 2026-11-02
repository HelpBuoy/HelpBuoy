package injector

import (
	"image"
	"unicode/utf16"

	"github.com/mevdschee/help-me-win/proto/input"
)

// event is one platform-neutral SendInput event: a MOUSEINPUT when mouse is
// set, a KEYBDINPUT otherwise. The Windows sender copies it into an INPUT.
type event struct {
	mouse bool

	// Mouse fields (MOUSEINPUT).
	dx, dy int32
	data   uint32

	// Keyboard fields (KEYBDINPUT).
	vk, scan uint16

	// flags is dwFlags of either kind.
	flags uint32
}

// KEYBDINPUT flags.
const (
	keyeventfExtendedKey = 0x0001
	keyeventfKeyUp       = 0x0002
	keyeventfUnicode     = 0x0004
	keyeventfScancode    = 0x0008
)

// MOUSEINPUT flags and data.
const (
	mouseeventfMove        = 0x0001
	mouseeventfLeftDown    = 0x0002
	mouseeventfLeftUp      = 0x0004
	mouseeventfRightDown   = 0x0008
	mouseeventfRightUp     = 0x0010
	mouseeventfMiddleDown  = 0x0020
	mouseeventfMiddleUp    = 0x0040
	mouseeventfXDown       = 0x0080
	mouseeventfXUp         = 0x0100
	mouseeventfWheel       = 0x0800
	mouseeventfHWheel      = 0x1000
	mouseeventfVirtualDesk = 0x4000
	mouseeventfAbsolute    = 0x8000

	xbutton1 = 0x0001
	xbutton2 = 0x0002
)

// buttonEvent returns the event that presses or releases a mouse button, or
// false for an unknown button.
func buttonEvent(button uint8, down bool) (event, bool) {
	var dn, up, data uint32
	switch button {
	case input.ButtonLeft:
		dn, up = mouseeventfLeftDown, mouseeventfLeftUp
	case input.ButtonRight:
		dn, up = mouseeventfRightDown, mouseeventfRightUp
	case input.ButtonMiddle:
		dn, up = mouseeventfMiddleDown, mouseeventfMiddleUp
	case input.ButtonBack:
		dn, up, data = mouseeventfXDown, mouseeventfXUp, xbutton1
	case input.ButtonForward:
		dn, up, data = mouseeventfXDown, mouseeventfXUp, xbutton2
	default:
		return event{}, false
	}
	e := event{mouse: true, data: data, flags: up}
	if down {
		e.flags = dn
	}
	return e, true
}

// wheelEvents returns the events for one Wheel message: vertical first, then
// horizontal. Both input and Windows use positive for away from the user
// (vertical) and for right (horizontal), in units of 1/120 notch.
func wheelEvents(w input.Wheel) []event {
	var evs []event
	if w.DY != 0 {
		evs = append(evs, event{mouse: true, flags: mouseeventfWheel, data: uint32(int32(w.DY))})
	}
	if w.DX != 0 {
		evs = append(evs, event{mouse: true, flags: mouseeventfHWheel, data: uint32(int32(w.DX))})
	}
	return evs
}

// textEvents returns the events that type s. Characters are sent as
// KEYEVENTF_UNICODE down and up per UTF-16 code unit, so characters outside
// the BMP become a surrogate pair. Line breaks ("\r\n", "\r", "\n") press
// Enter and "\t" presses Tab, because applications act on those keys rather
// than on the characters.
func textEvents(s string) []event {
	evs := make([]event, 0, 2*len(s))
	press := func(code string) {
		k := keyTable[code]
		evs = append(evs, keyEvent(k, true), keyEvent(k, false))
	}
	prevCR := false
	for _, r := range s {
		cr := r == '\r'
		switch {
		case cr:
			press("Enter")
		case r == '\n':
			if !prevCR {
				press("Enter")
			}
		case r == '\t':
			press("Tab")
		default:
			var units []uint16
			if r1, r2 := utf16.EncodeRune(r); r1 != '�' {
				units = []uint16{uint16(r1), uint16(r2)}
			} else {
				units = []uint16{uint16(r)}
			}
			for _, u := range units {
				evs = append(evs,
					event{scan: u, flags: keyeventfUnicode},
					event{scan: u, flags: keyeventfUnicode | keyeventfKeyUp})
			}
		}
		prevCR = cr
	}
	return evs
}

// absolute maps a normalized position (0..65535 over mon, where 65535 is the
// last pixel) to SendInput's absolute coordinates over the virtual desktop
// virt (0..65535 with MOUSEEVENTF_VIRTUALDESK). Both rectangles are in
// virtual-desktop pixels; origins may be negative when a monitor is left of
// or above the primary one.
//
// The result points at the center of the target pixel, so it lands on that
// pixel whether Windows scales by width/65536 or by (width-1)/65535.
func absolute(nx, ny uint16, mon, virt image.Rectangle) (int32, int32) {
	if mon.Empty() {
		mon = virt
	}
	if virt.Empty() {
		virt = mon
	}
	px := scale(nx, mon.Min.X, mon.Dx())
	py := scale(ny, mon.Min.Y, mon.Dy())
	return toDesk(px, virt.Min.X, virt.Dx()), toDesk(py, virt.Min.Y, virt.Dy())
}

// scale maps n in 0..65535 to a pixel in [origin, origin+size), rounding.
func scale(n uint16, origin, size int) int {
	if size <= 1 {
		return origin
	}
	return origin + int((int64(n)*int64(size-1)+32767)/65535)
}

// toDesk maps pixel p of a desktop axis starting at origin with size pixels
// to the center of that pixel in 0..65535.
func toDesk(p, origin, size int) int32 {
	if size <= 0 {
		return 0
	}
	p = min(max(p-origin, 0), size-1)
	return int32(min((int64(p)*65536+32768)/int64(size), 65535))
}
