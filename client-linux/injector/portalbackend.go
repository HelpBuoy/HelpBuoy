package injector

import (
	"fmt"
	"image"
	"log/slog"

	"github.com/helpbuoy/helpbuoy/client-linux/xkeys"
)

// PortalDevice is the input side of a remote desktop portal session;
// *portal.Session implements it.
type PortalDevice interface {
	PointerMotionAbsolute(stream uint32, x, y float64) error
	PointerButton(button int32, pressed bool) error
	PointerAxisDiscrete(axis uint32, steps int32) error
	KeyboardKeycode(code int32, pressed bool) error
	KeyboardKeysym(sym int32, pressed bool) error
}

// evdev button codes (linux/input-event-codes.h).
const (
	btnLeft   = 0x110
	btnRight  = 0x111
	btnMiddle = 0x112
	btnSide   = 0x113
	btnExtra  = 0x114
)

// NewPortal returns an injector for a Wayland desktop that sends the input
// through a portal session to stream, whose logical size is size. It does
// not close dev.
func NewPortal(log *slog.Logger, onInjected func(), dev PortalDevice, stream uint32, size image.Point) *Injector {
	if log == nil {
		log = slog.Default()
	}
	return &Injector{OnInjected: onInjected, log: log, be: &portalBackend{dev: dev, stream: stream, size: size}}
}

// portalBackend turns the X events of the injector into portal calls: X
// keycodes are evdev codes plus 8, X buttons 4 to 7 are wheel notches.
type portalBackend struct {
	dev    PortalDevice
	stream uint32
	size   image.Point
}

func (b *portalBackend) fake(typ, detail uint8, x, y int) error {
	switch typ {
	case xMotionNotify:
		return b.dev.PointerMotionAbsolute(b.stream, float64(x), float64(y))
	case xKeyPress, xKeyRelease:
		if detail < 8 {
			return fmt.Errorf("keycode %d has no evdev code", detail)
		}
		return b.dev.KeyboardKeycode(int32(detail)-8, typ == xKeyPress)
	case xButtonPress, xButtonRelease:
		down := typ == xButtonPress
		switch detail {
		case 4, 5, 6, 7:
			if !down {
				return nil // a notch is one call
			}
			axis, steps := uint32(1), int32(-1) // portal: negative is up or left
			if detail == 4 || detail == 5 {
				axis = 0
			}
			if detail == 5 || detail == 7 {
				steps = 1
			}
			return b.dev.PointerAxisDiscrete(axis, steps)
		}
		btn, ok := map[uint8]int32{1: btnLeft, 2: btnMiddle, 3: btnRight, 8: btnSide, 9: btnExtra}[detail]
		if !ok {
			return fmt.Errorf("button %d has no evdev code", detail)
		}
		return b.dev.PointerButton(btn, down)
	}
	return fmt.Errorf("event type %d not supported", typ)
}

// typeRune presses the keysym of r; the compositor finds the key and the
// shift level in the local layout.
func (b *portalBackend) typeRune(r rune) error {
	ks := int32(xkeys.Keysym(r))
	if err := b.dev.KeyboardKeysym(ks, true); err != nil {
		return err
	}
	return b.dev.KeyboardKeysym(ks, false)
}

func (b *portalBackend) screen() image.Rectangle { return image.Rectangle{Max: b.size} }
func (b *portalBackend) flush() error            { return nil }
func (b *portalBackend) close()                  {}
