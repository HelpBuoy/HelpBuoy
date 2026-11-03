package views

import "github.com/mevdschee/helpbuoy/proto/input"

// hostKey is the key that, held down, turns the next key into a viewer
// shortcut instead of input for the remote side, like Remmina's host key.
const hostKey = "ControlRight"

// hostAction is a viewer shortcut.
type hostAction int

const (
	actNone       hostAction = iota
	actFullscreen            // Right Ctrl+F
	actToggleMode            // Right Ctrl+S: Fit or 1:1
	actResize                // Right Ctrl+R: resize the window to the client
)

// hostCombos maps the W3C code of the second key to its shortcut. Codes are
// physical keys, so the shortcuts sit in the same place on every layout.
var hostCombos = map[string]hostAction{
	"KeyF": actFullscreen,
	"KeyS": actToggleMode,
	"KeyR": actResize,
}

// hostState is where the host key filter is between key events.
type hostState int

const (
	hostIdle      hostState = iota // Right Ctrl is up
	hostPending                    // Right Ctrl is down and not sent yet
	hostForwarded                  // Right Ctrl is down and was sent down
)

// hostFilter decides which key events reach the remote side.
//
// The rule: pressing Right Ctrl sends nothing yet. While it is held,
//   - one of the shortcut keys (F, S, R, H) runs the shortcut and is never
//     sent, neither its press nor its release, and Right Ctrl stays unsent;
//     this holds for every shortcut key while Right Ctrl is held, also after
//     Right Ctrl itself has been sent;
//   - any other key first sends the delayed Right Ctrl down and then the key,
//     so remote Ctrl shortcuts work with Right Ctrl as well; from then on
//     Right Ctrl behaves as a normal key until it is released;
//   - a mouse button press (Flush) also sends the delayed Right Ctrl down, so
//     Ctrl+click works.
//
// Releasing Right Ctrl sends its release if its press was sent. Releasing it
// when nothing else was pressed sends a full press and release, so a lone
// Right Ctrl tap still reaches the remote side. Releasing it after only
// shortcuts sends nothing.
type hostFilter struct {
	state   hostState
	used    bool            // a shortcut ran during this hold
	swallow map[string]bool // shortcut keys whose release must not be sent
}

// key filters one key event: a press (down, possibly a repeat) or a release.
// It returns the key events to send, in order, and the shortcut to run.
func (f *hostFilter) key(code string, down, repeat bool) ([]input.Key, hostAction) {
	if f.swallow == nil {
		f.swallow = map[string]bool{}
	}
	if code == hostKey {
		switch {
		case down && f.state == hostIdle:
			f.state, f.used = hostPending, false
			return nil, actNone
		case down && f.state == hostForwarded:
			return []input.Key{{Code: hostKey, Down: true}}, actNone // a repeat
		case down:
			return nil, actNone
		}
		st, used := f.state, f.used
		f.state, f.used = hostIdle, false
		switch {
		case st == hostForwarded:
			return []input.Key{{Code: hostKey}}, actNone
		case st == hostPending && !used:
			return []input.Key{{Code: hostKey, Down: true}, {Code: hostKey}}, actNone
		}
		return nil, actNone
	}

	if !down {
		if f.swallow[code] {
			delete(f.swallow, code)
			return nil, actNone
		}
		return []input.Key{{Code: code}}, actNone
	}
	if f.state != hostIdle {
		if act, ok := hostCombos[code]; ok {
			f.swallow[code] = true
			f.used = true
			if repeat {
				return nil, actNone
			}
			return nil, act
		}
	}
	if f.swallow[code] {
		return nil, actNone // a repeat of a shortcut key after Right Ctrl went up
	}
	out := f.flush()
	return append(out, input.Key{Code: code, Down: true}), actNone
}

// flush sends a delayed Right Ctrl press, for input that should see Ctrl
// held (a mouse click). It returns the events to send.
func (f *hostFilter) flush() []input.Key {
	if f.state != hostPending {
		return nil
	}
	f.state = hostForwarded
	return []input.Key{{Code: hostKey, Down: true}}
}

// held reports whether Right Ctrl is down locally, sent or not.
func (f *hostFilter) held() bool { return f.state != hostIdle }

// reset forgets all state, when the session loses the keyboard. Keys that
// were sent are released by the caller.
func (f *hostFilter) reset() {
	f.state, f.used = hostIdle, false
	clear(f.swallow)
}
