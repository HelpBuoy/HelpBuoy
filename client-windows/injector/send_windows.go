//go:build windows

package injector

import (
	"errors"
	"image"
	"syscall"
	"unsafe"
)

var (
	procSendInput        = user32.NewProc("SendInput")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
	procMapVirtualKeyW   = user32.NewProc("MapVirtualKeyW")
)

const (
	inputMouse    = 0
	inputKeyboard = 1

	smCxScreen        = 0
	smCyScreen        = 1
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCxVirtualScreen = 78
	smCyVirtualScreen = 79

	mapvkVscToVkEx = 3
)

// mouseInput is MOUSEINPUT.
type mouseInput struct {
	Dx, Dy    int32
	MouseData uint32
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

// keybdInput is KEYBDINPUT, padded to the size of MOUSEINPUT, the largest
// member of the INPUT union.
type keybdInput struct {
	Vk, Scan  uint16
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
	_         [8]byte
}

// winInput is INPUT with the union held as a MOUSEINPUT.
type winInput struct {
	Type uint32
	Mi   mouseInput
}

// Compile-time check that both union members have the same size.
var (
	_ [unsafe.Sizeof(mouseInput{}) - unsafe.Sizeof(keybdInput{})]struct{}
	_ [unsafe.Sizeof(keybdInput{}) - unsafe.Sizeof(mouseInput{})]struct{}
)

// toWin converts events to INPUT structures.
func toWin(evs []event) []winInput {
	in := make([]winInput, len(evs))
	for i, e := range evs {
		if e.mouse {
			in[i].Type = inputMouse
			in[i].Mi = mouseInput{Dx: e.dx, Dy: e.dy, MouseData: e.data, Flags: e.flags}
			continue
		}
		in[i].Type = inputKeyboard
		if e.flags&keyeventfScancode != 0 && e.vk == 0 {
			e.vk = scanToVK(e.scan, e.flags&keyeventfExtendedKey != 0)
		}
		*(*keybdInput)(unsafe.Pointer(&in[i].Mi)) = keybdInput{Vk: e.vk, Scan: e.scan, Flags: e.flags}
	}
	return in
}

// scanToVK returns the virtual-key code the current layout gives a scan
// code, or 0. Windows ignores wVk when KEYEVENTF_SCANCODE is set, but Wine
// uses it instead of the scan code, so it is filled in for both.
func scanToVK(scan uint16, ext bool) uint16 {
	code := uintptr(scan)
	if ext {
		code |= 0xe000
	}
	vk, _, _ := procMapVirtualKeyW.Call(code, mapvkVscToVkEx)
	return uint16(vk)
}

// sendInput injects evs with one SendInput call, so they are not interleaved
// with other input. Windows marks them LLKHF_INJECTED / LLMHF_INJECTED, which
// lets the Esc hook tell them from the local keyboard.
func sendInput(evs []event) error {
	if len(evs) == 0 {
		return nil
	}
	in := toWin(evs)
	n, _, e := procSendInput.Call(uintptr(len(in)), uintptr(unsafe.Pointer(&in[0])), unsafe.Sizeof(in[0]))
	if int(n) == len(in) {
		return nil
	}
	if e != nil && e != syscall.Errno(0) {
		return e
	}
	return errors.New("SendInput inserted fewer events than given (blocked by UIPI?)")
}

func metric(i uintptr) int {
	r, _, _ := procGetSystemMetrics.Call(i)
	return int(int32(r))
}

// screenRects returns the virtual desktop and the primary monitor in
// virtual-desktop pixels. It asks every time, so display changes during a
// session are followed.
func screenRects() (virtual, primary image.Rectangle) {
	x, y := metric(smXVirtualScreen), metric(smYVirtualScreen)
	virtual = image.Rect(x, y, x+metric(smCxVirtualScreen), y+metric(smCyVirtualScreen))
	primary = image.Rect(0, 0, metric(smCxScreen), metric(smCyScreen))
	return virtual, primary
}
