package injector

import (
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/helpbuoy/helpbuoy/client-linux/xkeys"
)

// escPoll is how often the Esc hook reads the key state.
const escPoll = 30 * time.Millisecond

// InstallEscHook watches the local keyboard on its own X connection and
// calls fn in a new goroutine when Esc is pressed. Esc presses the injector
// makes for the techie are told apart and ignored.
//
// Unlike the Windows hook, the local Esc is not swallowed: X has no way to
// take one key away from the other applications without also taking the
// injected presses. It reads the key state with QueryKeymap, which works on
// every X server without extra extensions. stop ends the watch.
func InstallEscHook(fn func()) (stop func(), err error) {
	c, err := xgb.NewConn()
	if err != nil {
		return nil, err
	}
	codes := []uint8{escKeycode}
	if km, err := xkeys.Load(c); err == nil {
		if cs := km.Keycodes(xkeys.Escape); len(cs) > 0 {
			codes = cs
		}
	}
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer c.Close()
		go func() { // drain events and errors, see xBackend.events
			for {
				if ev, err := c.WaitForEvent(); ev == nil && err == nil {
					return
				}
			}
		}()
		// An Esc already down when the hook starts (for example the key
		// that started all this) only counts once it was released.
		wasDown := true
		tick := time.NewTicker(escPoll)
		defer tick.Stop()
		for {
			select {
			case <-quit:
				return
			case <-tick.C:
			}
			r, err := xproto.QueryKeymap(c).Reply()
			if err != nil {
				return
			}
			down := false
			for _, kc := range codes {
				if r.Keys[kc/8]&(1<<(kc%8)) != 0 {
					down = true
				}
			}
			local := down && !escInjected.Load() && time.Now().UnixNano() >= escQuietUntil.Load()
			if local && !wasDown {
				go fn()
			}
			wasDown = down
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(quit)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
		})
	}, nil
}
