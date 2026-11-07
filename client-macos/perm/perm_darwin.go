package perm

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework AppKit -framework ApplicationServices
#include <stdlib.h>
#include "perm.h"
*/
import "C"

import (
	"sync"
	"unsafe"
)

// The panes of System Settings (System Preferences before macOS 13).
const (
	screenPane = "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture"
	inputPane  = "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"
)

// ScreenRecording reports whether the app may record the screen. It stays
// false after the user allows it until the app restarts.
func ScreenRecording() bool { return C.hb_screen_allowed() != 0 }

// Accessibility reports whether the app may control the computer.
func Accessibility() bool { return C.hb_input_allowed(0) != 0 }

var screenAsked, inputAsked sync.Once

// OpenScreenRecording asks for Screen Recording: the first time macOS
// shows its own question and adds the app to the list; then the pane opens.
func OpenScreenRecording() {
	screenAsked.Do(func() { C.hb_screen_request() })
	open(screenPane)
}

// OpenAccessibility asks for Accessibility the same way.
func OpenAccessibility() {
	inputAsked.Do(func() { C.hb_input_allowed(1) })
	open(inputPane)
}

func open(url string) {
	u := C.CString(url)
	defer C.free(unsafe.Pointer(u))
	C.hb_open_url(u)
}
