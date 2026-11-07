// Package injector injects the techie's input into the local X11 display
// with the XTEST extension (PLAN.md 6.4), and watches for a local Esc press
// that ends the session. It talks the X protocol directly with jezek/xgb, so
// it needs no C libraries.
package injector

import (
	"fmt"
	"image"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/helpbuoy/helpbuoy/proto/input"
)

// X core event types, as XTEST FakeInput takes them.
const (
	xKeyPress      = 2
	xKeyRelease    = 3
	xButtonPress   = 4
	xButtonRelease = 5
	xMotionNotify  = 6
)

// escKeycode is the X keycode of Escape (evdev 1 + 8).
const escKeycode = 9

// backend sends events to the display. The X implementation is xBackend;
// tests use a recorder.
type backend interface {
	// fake sends one XTEST event; x and y are only used for motion.
	fake(typ, detail uint8, x, y int) error
	// typeRune types r with the local keyboard layout, remapping a spare
	// keycode when no key produces it.
	typeRune(r rune) error
	// screen is the whole root window, used when no monitor was set.
	screen() image.Rectangle
	// flush waits until the display processed everything sent so far.
	flush() error
	close()
}

// Injector implements session.Injector. Every key and button it presses is
// remembered until it is released, so ReleaseAll and Close can lift them.
// All methods are safe for concurrent use.
type Injector struct {
	// OnInjected, if set, is called after every injected message, outside
	// the injector's lock.
	OnInjected func()

	log *slog.Logger
	be  backend // nil: decode and log only

	mu      sync.Mutex
	closed  bool
	monitor image.Rectangle
	held    []held
	wheelX  int // wheel remainders below one notch, in 1/120 notch
	wheelY  int
	logged  map[string]bool
}

// held is a key or button the injector holds down.
type held struct {
	id     string // "key:KeyA" or "button:1"
	typ    uint8  // the release event type
	detail uint8
}

// New connects to the display in $DISPLAY. If that fails, the Injector only
// logs the input it gets, and the error is logged once.
func New(log *slog.Logger, onInjected func()) *Injector {
	if log == nil {
		log = slog.Default()
	}
	j := &Injector{OnInjected: onInjected, log: log}
	be, err := newXBackend(log)
	if err != nil {
		log.Error("input injection is not available", "err", err)
		return j
	}
	j.be = be
	return j
}

func (j *Injector) logOnce(key, msg string, args ...any) {
	if j.logged[key] {
		return
	}
	if j.logged == nil {
		j.logged = make(map[string]bool)
	}
	j.logged[key] = true
	j.log.Warn(msg, args...)
}

// SetMonitor sets the shared monitor in root window pixels; normalized
// mouse coordinates (0..65535) are mapped onto it. Until it is called, or
// when r is empty, the whole screen is used.
func (j *Injector) SetMonitor(r image.Rectangle) {
	j.mu.Lock()
	j.monitor = r.Canon()
	j.mu.Unlock()
}

// Input implements session.InputSink: decode and inject one message.
func (j *Injector) Input(typ uint8, payload []byte) {
	m, err := input.Decode(typ, payload)
	j.mu.Lock()
	if err != nil {
		j.logOnce(fmt.Sprintf("decode:%d", typ), "input message not decoded", "type", typ, "bytes", len(payload), "err", err)
		j.mu.Unlock()
		return
	}
	if j.closed {
		j.mu.Unlock()
		return
	}
	if j.be == nil {
		j.mu.Unlock()
		j.log.Debug("input message (no display to inject into)", "type", typ, "msg", m)
		return
	}
	injected := j.handle(m)
	j.mu.Unlock()
	if injected && j.OnInjected != nil {
		j.OnInjected()
	}
}

// handle injects one decoded message and reports whether anything was sent.
// The caller holds j.mu.
func (j *Injector) handle(m any) bool {
	switch v := m.(type) {
	case input.MouseMove:
		mon := j.monitor
		if mon.Empty() {
			mon = j.be.screen()
		}
		return j.send("mouse", xMotionNotify, 0, scale(v.X, mon.Min.X, mon.Dx()), scale(v.Y, mon.Min.Y, mon.Dy()))

	case input.MouseButton:
		b, ok := xButton(v.Button)
		if !ok {
			return false
		}
		id := fmt.Sprintf("button:%d", b)
		if !v.Down {
			return j.lift(id)
		}
		if j.isHeld(id) {
			return false // a second press of a held button means nothing
		}
		if !j.send("mouse", xButtonPress, b, 0, 0) {
			return false
		}
		j.held = append(j.held, held{id: id, typ: xButtonRelease, detail: b})
		return true

	case input.Wheel:
		return j.wheel(v)

	case input.Key:
		kc, ok := keycode(v.Code)
		if !ok {
			j.logOnce("code:"+v.Code, "input key code not supported", "code", v.Code)
			return false
		}
		id := "key:" + v.Code
		if !v.Down {
			return j.lift(id)
		}
		// A press of a held key is auto-repeat: send it, track it once.
		if kc == escKeycode {
			escInjected.Store(true)
		}
		if !j.send("keyboard", xKeyPress, kc, 0, 0) {
			if kc == escKeycode && !j.isHeld(id) {
				escReleased()
			}
			return false
		}
		if !j.isHeld(id) {
			j.held = append(j.held, held{id: id, typ: xKeyRelease, detail: kc})
		}
		return true

	case input.Text:
		return j.text(v.Text)

	case input.ReleaseAll:
		return j.releaseLocked()
	}
	return false
}

// xButton maps an input button to an X button number.
func xButton(b uint8) (uint8, bool) {
	switch b {
	case input.ButtonLeft:
		return 1, true
	case input.ButtonMiddle:
		return 2, true
	case input.ButtonRight:
		return 3, true
	case input.ButtonBack:
		return 8, true
	case input.ButtonForward:
		return 9, true
	}
	return 0, false
}

// wheel clicks X buttons 4/5 (up/down) and 6/7 (left/right) once per whole
// notch; smaller amounts add up until they make one.
func (j *Injector) wheel(w input.Wheel) bool {
	sent := false
	click := func(acc *int, delta int, pos, neg uint8) {
		*acc += delta
		for *acc >= 120 || *acc <= -120 {
			b := pos
			if *acc < 0 {
				b, *acc = neg, *acc+120
			} else {
				*acc -= 120
			}
			if j.send("mouse", xButtonPress, b, 0, 0) && j.send("mouse", xButtonRelease, b, 0, 0) {
				sent = true
			}
		}
	}
	// input: DY positive is away from the user (up), DX positive is right.
	click(&j.wheelY, int(w.DY), 4, 5)
	click(&j.wheelX, int(w.DX), 7, 6)
	return sent
}

// text types s. Line breaks press Enter and "\t" presses Tab, because
// applications act on those keys rather than on the characters.
func (j *Injector) text(s string) bool {
	sent := false
	tap := func(code string) {
		kc, _ := keycode(code)
		if j.send("text", xKeyPress, kc, 0, 0) && j.send("text", xKeyRelease, kc, 0, 0) {
			sent = true
		}
	}
	prevCR := false
	for _, r := range s {
		cr := r == '\r'
		switch {
		case cr:
			tap("Enter")
		case r == '\n':
			if !prevCR {
				tap("Enter")
			}
		case r == '\t':
			tap("Tab")
		case r < 0x20 || r == 0x7f:
			// Other control characters have no key.
		default:
			if err := j.be.typeRune(r); err != nil {
				j.logOnce("text:"+err.Error(), "character not typed", "err", err)
			} else {
				sent = true
			}
		}
		prevCR = cr
	}
	return sent
}

func (j *Injector) isHeld(id string) bool {
	for _, h := range j.held {
		if h.id == id {
			return true
		}
	}
	return false
}

// lift releases id if the injector holds it, and forgets it. A release of
// something the injector never pressed is ignored, so it cannot release a
// key the local user holds. The caller holds j.mu.
func (j *Injector) lift(id string) bool {
	for i, h := range j.held {
		if h.id == id {
			j.held = append(j.held[:i], j.held[i+1:]...)
			return j.release(h)
		}
	}
	return false
}

func (j *Injector) release(h held) bool {
	ok := j.send("release", h.typ, h.detail, 0, 0)
	if h.typ == xKeyRelease && h.detail == escKeycode {
		escReleased()
	}
	return ok
}

// send sends one event and logs a failure once per kind and error.
func (j *Injector) send(kind string, typ, detail uint8, x, y int) bool {
	if err := j.be.fake(typ, detail, x, y); err != nil {
		j.logOnce("send:"+kind+":"+err.Error(), "input injection failed", "kind", kind, "err", err)
		return false
	}
	return true
}

// releaseLocked lifts every held key and button in reverse press order.
func (j *Injector) releaseLocked() bool {
	if len(j.held) == 0 || j.be == nil {
		j.held = nil
		return false
	}
	sent := false
	for i := len(j.held) - 1; i >= 0; i-- {
		if j.release(j.held[i]) {
			sent = true
		}
	}
	j.held = nil
	return sent
}

// ReleaseAll lifts every key and mouse button this injector pressed.
func (j *Injector) ReleaseAll() {
	j.mu.Lock()
	injected := !j.closed && j.releaseLocked()
	j.mu.Unlock()
	if injected && j.OnInjected != nil {
		j.OnInjected()
	}
}

// Close releases everything and disconnects from the display. Further Input
// is ignored. It is safe to call more than once.
func (j *Injector) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.releaseLocked()
	j.closed = true
	if j.be != nil {
		j.be.flush()
		j.be.close()
	}
	return nil
}

// scale maps n in 0..65535 to a pixel in [origin, origin+size), rounding.
func scale(n uint16, origin, size int) int {
	if size <= 1 {
		return origin
	}
	return origin + int((int64(n)*int64(size-1)+32767)/65535)
}

// escInjected is true while an injector holds Escape, and escQuietUntil is
// when the Esc hook may trust the key state again after the injector
// released it: the hook reads the key state from its own connection, where
// an injected release can show up a little late.
var (
	escInjected   atomic.Bool
	escQuietUntil atomic.Int64
)

func escReleased() {
	escQuietUntil.Store(time.Now().Add(escQuiet).UnixNano())
	escInjected.Store(false)
}

const escQuiet = 300 * time.Millisecond
