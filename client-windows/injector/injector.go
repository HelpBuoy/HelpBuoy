package injector

import (
	"fmt"
	"image"
	"log/slog"
	"sync"

	"github.com/mevdschee/helpbuoy/proto/input"
)

// Injector injects the techie's input into the local desktop with SendInput
// (PLAN.md 6.4). It implements session.InputSink.
//
// CONTRACT: the session/capture code (another owner) uses exactly this API:
// it creates one Injector per session, calls SetMonitor whenever the shared
// monitor changes, passes every input message to Input, and calls Close when
// the session ends (which releases all held keys and buttons). OnInjected is
// called after each injected event so capture can be kicked right away.
//
// All methods are safe for concurrent use. Keys and buttons are sent with
// KEYEVENTF_SCANCODE and MOUSEEVENTF_ABSOLUTE|MOUSEEVENTF_VIRTUALDESK, and
// every key and button the injector presses is remembered until it is
// released, so ReleaseAll and Close can lift them.
type Injector struct {
	// Logger may be nil.
	Logger *slog.Logger
	// OnInjected, if set, is called (from the Input goroutine) after every
	// injected event batch, outside the injector's lock.
	OnInjected func()

	// send injects a batch; nil means decode and log only. It defaults to
	// sendInput.
	send func([]event) error
	// screens returns the virtual desktop and the primary monitor in
	// virtual-desktop pixels. It defaults to screenRects.
	screens func() (virtual, primary image.Rectangle)

	mu      sync.Mutex
	closed  bool
	monitor image.Rectangle
	held    []heldInput     // in press order
	logged  map[string]bool // problems already logged once
}

// heldInput is a key or button the injector holds down.
type heldInput struct {
	id string // "key:KeyA" or "button:0"
	up event  // the event that releases it
}

// New returns an Injector ready for Input; on non-Windows systems it only
// decodes and logs, like Counter.
func New(logger *slog.Logger, onInjected func()) *Injector {
	return &Injector{Logger: logger, OnInjected: onInjected, send: sendInput, screens: screenRects}
}

func (j *Injector) log() *slog.Logger {
	if j.Logger != nil {
		return j.Logger
	}
	return slog.Default()
}

// logOnce logs a warning the first time key is seen; later ones are dropped,
// so a blocked target or an unknown key does not flood the log. The caller
// holds j.mu.
func (j *Injector) logOnce(key, msg string, args ...any) {
	if j.logged[key] {
		return
	}
	if j.logged == nil {
		j.logged = make(map[string]bool)
	}
	j.logged[key] = true
	j.log().Warn(msg, args...)
}

// SetMonitor sets the shared monitor in virtual-desktop pixels; normalized
// mouse coordinates (0..65535) are mapped onto it. Until it is called, or
// when r is empty, the primary monitor is used.
func (j *Injector) SetMonitor(r image.Rectangle) {
	j.mu.Lock()
	j.monitor = r.Canon()
	j.mu.Unlock()
}

// Input implements session.InputSink: decode and inject one message.
func (j *Injector) Input(typ uint8, payload []byte) {
	m, err := input.Decode(typ, payload)
	if err != nil {
		j.mu.Lock()
		j.logOnce(fmt.Sprintf("decode:%d", typ), "input message not decoded", "type", typ, "bytes", len(payload), "err", err)
		j.mu.Unlock()
		return
	}
	j.mu.Lock()
	if j.closed {
		j.mu.Unlock()
		return
	}
	if j.send == nil {
		j.mu.Unlock()
		j.log().Debug("input message (not injected on this OS)", "type", typ, "msg", m)
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
		var virt, primary image.Rectangle
		if j.screens != nil {
			virt, primary = j.screens()
		}
		mon := j.monitor
		if mon.Empty() {
			mon = primary
		}
		x, y := absolute(v.X, v.Y, mon, virt)
		return j.inject("mouse", []event{{mouse: true, dx: x, dy: y,
			flags: mouseeventfMove | mouseeventfAbsolute | mouseeventfVirtualDesk}})

	case input.MouseButton:
		e, ok := buttonEvent(v.Button, v.Down)
		if !ok {
			return false
		}
		id := fmt.Sprintf("button:%d", v.Button)
		if !v.Down {
			return j.lift(id, "mouse", e)
		}
		if j.isHeld(id) {
			return false // a second press of a held button means nothing
		}
		up, _ := buttonEvent(v.Button, false)
		if !j.inject("mouse", []event{e}) {
			return false
		}
		j.held = append(j.held, heldInput{id: id, up: up})
		return true

	case input.Wheel:
		evs := wheelEvents(v)
		return len(evs) > 0 && j.inject("mouse", evs)

	case input.Key:
		k, ok := lookupKey(v.Code)
		if !ok {
			j.logOnce("code:"+v.Code, "input key code not supported", "code", v.Code)
			return false
		}
		id := "key:" + v.Code
		if !v.Down {
			return j.lift(id, "keyboard", keyEvent(k, false))
		}
		// A press of a held key is auto-repeat: send it, track it once.
		if !j.inject("keyboard", []event{keyEvent(k, true)}) {
			return false
		}
		if !j.isHeld(id) {
			j.held = append(j.held, heldInput{id: id, up: keyEvent(k, false)})
		}
		return true

	case input.Text:
		evs := textEvents(v.Text)
		return len(evs) > 0 && j.inject("text", evs)

	case input.ReleaseAll:
		return j.releaseLocked()
	}
	return false
}

// isHeld reports whether id is held. The caller holds j.mu.
func (j *Injector) isHeld(id string) bool {
	for _, h := range j.held {
		if h.id == id {
			return true
		}
	}
	return false
}

// lift sends up for id if the injector holds it, and forgets it. A release
// of something the injector never pressed is ignored, so it cannot release
// a key the local user holds. The caller holds j.mu.
func (j *Injector) lift(id, kind string, up event) bool {
	for i, h := range j.held {
		if h.id == id {
			j.held = append(j.held[:i], j.held[i+1:]...)
			return j.inject(kind, []event{up})
		}
	}
	return false
}

// inject sends a batch and logs a failure once per kind and error. The
// caller holds j.mu.
func (j *Injector) inject(kind string, evs []event) bool {
	if err := j.send(evs); err != nil {
		j.logOnce("send:"+kind+":"+err.Error(), "SendInput failed; the target may be elevated or on the secure desktop",
			"kind", kind, "err", err)
		return false
	}
	return true
}

// releaseLocked lifts every held key and button in reverse press order, in
// one batch. The caller holds j.mu.
func (j *Injector) releaseLocked() bool {
	if len(j.held) == 0 || j.send == nil {
		j.held = nil
		return false
	}
	evs := make([]event, 0, len(j.held))
	for i := len(j.held) - 1; i >= 0; i-- {
		evs = append(evs, j.held[i].up)
	}
	j.held = nil
	return j.inject("release", evs)
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

// Close releases everything and stops the injector. Further Input is ignored.
// It is safe to call more than once.
func (j *Injector) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.releaseLocked()
	j.closed = true
	return nil
}
