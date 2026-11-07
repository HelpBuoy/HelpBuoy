package injector

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework AppKit -framework ApplicationServices
#include "cgevent.h"
*/
import "C"

import (
	"errors"
	"image"
	"time"
	"unicode/utf16"
)

var errPost = errors.New("CGEvent could not be created")

// cgBackend posts CGEvents to the HID event tap.
type cgBackend struct{}

func newBackend() backend {
	if C.hb_inject_open() != 0 {
		return nil
	}
	return cgBackend{}
}

func check(r C.int) error {
	if r != 0 {
		return errPost
	}
	return nil
}

func (cgBackend) key(code uint16, down bool, flags uint64) error {
	return check(C.hb_inject_key(C.uint16_t(code), cbool(down), C.uint64_t(flags), cbool(modifierFlag(code) != 0)))
}

func (cgBackend) mouse(typ, button uint32, x, y float64, clicks int64, flags uint64) error {
	return check(C.hb_inject_mouse(C.uint32_t(typ), C.uint32_t(button), C.double(x), C.double(y), C.int64_t(clicks), C.uint64_t(flags)))
}

func (cgBackend) scroll(dy, dx int32, flags uint64) error {
	return check(C.hb_inject_scroll(C.int32_t(dy), C.int32_t(dx), C.uint64_t(flags)))
}

func (cgBackend) text(s string) error {
	u := utf16.Encode([]rune(s))
	if len(u) == 0 {
		return nil
	}
	return check(C.hb_inject_text((*C.uint16_t)(&u[0]), C.int(len(u))))
}

func (cgBackend) doubleClick() time.Duration {
	return time.Duration(float64(C.hb_double_click()) * float64(time.Second))
}

func (cgBackend) screen() image.Rectangle {
	var x, y, w, h C.double
	C.hb_main_display(&x, &y, &w, &h)
	return image.Rect(int(x), int(y), int(x+w), int(y+h))
}

// close keeps the event source: it is shared by every session.
func (cgBackend) close() {}

func cbool(b bool) C.int {
	if b {
		return 1
	}
	return 0
}
