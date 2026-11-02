//go:build windows

package injector

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32                  = syscall.NewLazyDLL("user32.dll")
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procSetWindowsHookExW   = user32.NewProc("SetWindowsHookExW")
	procCallNextHookEx      = user32.NewProc("CallNextHookEx")
	procUnhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procPeekMessageW        = user32.NewProc("PeekMessageW")
	procPostThreadMessageW  = user32.NewProc("PostThreadMessageW")
	procGetModuleHandleW    = kernel32.NewProc("GetModuleHandleW")
	procGetCurrentThreadId  = kernel32.NewProc("GetCurrentThreadId")
)

const (
	whKeyboardLL  = 13
	hcAction      = 0
	wmKeyDown     = 0x0100
	wmSysKeyDown  = 0x0104
	wmQuit        = 0x0012
	wmUser        = 0x0400
	pmNoRemove    = 0
	vkEscape      = 0x1b
	llkhfInjected = 0x10
)

// kbdllHookStruct is KBDLLHOOKSTRUCT.
type kbdllHookStruct struct {
	VkCode    uint32
	ScanCode  uint32
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

// msg is MSG.
type msg struct {
	Hwnd     uintptr
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       [2]int32
	LPrivate uint32
}

// onEsc is the callback of the installed hook; nil when none is installed.
var onEsc atomic.Pointer[func()]

// hookProc is created once: syscall.NewCallback slots are limited, so it
// must not be created per session.
var hookProc = syscall.NewCallback(func(code, wParam uintptr, k *kbdllHookStruct) uintptr {
	if int32(code) == hcAction && k != nil {
		if k.VkCode == vkEscape && k.Flags&llkhfInjected == 0 {
			if f := onEsc.Load(); f != nil {
				if wParam == wmKeyDown || wParam == wmSysKeyDown {
					go (*f)() // the hook must return quickly
				}
				return 1 // swallow the local Esc, down and up
			}
		}
	}
	r, _, _ := procCallNextHookEx.Call(0, code, wParam, uintptr(unsafe.Pointer(k)))
	return r
})

var installMu sync.Mutex

// InstallEscHook installs a WH_KEYBOARD_LL hook on its own locked OS thread
// with a message loop. A local Esc press (without LLKHF_INJECTED) is
// swallowed and calls fn in a new goroutine; an injected Esc passes through.
// stop removes the hook. Only one hook is active at a time; installing a new
// one replaces the callback of the previous one.
func InstallEscHook(fn func()) (stop func(), err error) {
	installMu.Lock()
	defer installMu.Unlock()

	ready := make(chan error, 1)
	exited := make(chan struct{})
	var tid uintptr
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(exited)
		tid, _, _ = procGetCurrentThreadId.Call()
		var m msg
		// Create the thread's message queue before anyone posts to it.
		procPeekMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, wmUser, wmUser, pmNoRemove)
		hMod, _, _ := procGetModuleHandleW.Call(0)
		h, _, e := procSetWindowsHookExW.Call(whKeyboardLL, hookProc, hMod, 0)
		if h == 0 {
			ready <- e
			return
		}
		ready <- nil
		for {
			r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 {
				break // WM_QUIT or error
			}
		}
		procUnhookWindowsHookEx.Call(h)
	}()
	if err := <-ready; err != nil {
		<-exited
		if err == syscall.Errno(0) {
			err = errors.New("SetWindowsHookEx failed")
		}
		return nil, err
	}
	onEsc.Store(&fn)

	var once sync.Once
	return func() {
		once.Do(func() {
			onEsc.CompareAndSwap(&fn, nil)
			procPostThreadMessageW.Call(tid, wmQuit, 0, 0)
			select {
			case <-exited:
			case <-time.After(2 * time.Second):
			}
		})
	}, nil
}
