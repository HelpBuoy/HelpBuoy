package injector

// #include "cgevent.h"
import "C"

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// onEsc is the callback of the installed tap; nil when none is installed.
var onEsc atomic.Pointer[func()]

// escMu allows one tap at a time.
var escMu sync.Mutex

//export hbEscPressed
func hbEscPressed() {
	if f := onEsc.Load(); f != nil {
		go (*f)() // the tap must return quickly
	}
}

// InstallEscHook calls fn when Esc is pressed on the local keyboard, until
// stop is called. Like the Windows hook it swallows the local Esc, while
// the techie's injected Esc, marked by the injector, passes. It needs the
// Accessibility permission.
func InstallEscHook(fn func()) (stop func(), err error) {
	escMu.Lock()
	started := make(chan error, 1)
	done := make(chan struct{})
	onEsc.Store(&fn)
	go func() {
		runtime.LockOSThread() // the tap lives in this thread's run loop
		defer runtime.UnlockOSThread()
		defer close(done)
		if C.hb_esc_create() != 0 {
			started <- errors.New("the keyboard can't be watched: HelpBuoy needs the Accessibility permission")
			return
		}
		started <- nil
		C.hb_esc_run()
	}()
	if err := <-started; err != nil {
		<-done
		onEsc.Store(nil)
		escMu.Unlock()
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			onEsc.Store(nil)
			// A stop before the run loop runs does nothing, so repeat it.
			for stopped := false; !stopped; {
				C.hb_esc_stop()
				select {
				case <-done:
					stopped = true
				case <-time.After(50 * time.Millisecond):
				}
			}
			escMu.Unlock()
		})
	}, nil
}
