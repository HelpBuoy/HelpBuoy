// Package injector injects the techie's input on macOS with CGEvent
// (PLAN.md 6.4), and watches for a local Esc press that ends the session.
// Injecting needs the Accessibility permission. The logic here is platform
// neutral and tested anywhere; the CGEvent calls are in the backend.
//
// Synthetic events get none of the help real devices get, so the injector
// does what the HID system would: it sends drag events while a button is
// held, counts clicks so double clicks work, and puts the held modifiers on
// every event.
package injector

import (
	"fmt"
	"image"
	"log/slog"
	"sync"
	"time"

	"github.com/helpbuoy/helpbuoy/proto/input"
)

// CGEventType values.
const (
	evLeftDown     = 1
	evLeftUp       = 2
	evRightDown    = 3
	evRightUp      = 4
	evMoved        = 5
	evLeftDragged  = 6
	evRightDragged = 7
	evOtherDown    = 25
	evOtherUp      = 26
	evOtherDragged = 27
)

// CGMouseButton values.
const (
	btnLeft   = 0
	btnRight  = 1
	btnCenter = 2
	btnBack   = 3
	btnFwd    = 4
)

// clickSlop is how far, in points, a press may be from the previous one
// and still add to its click count.
const clickSlop = 4

// backend posts events. The CGEvent implementation is cgBackend; tests use
// a recorder.
type backend interface {
	// key posts a key press or release. A modifier key is posted as a flags
	// change with flags as the new state.
	key(code uint16, down bool, flags uint64) error
	// mouse posts a mouse event at (x, y) in global points.
	mouse(typ, button uint32, x, y float64, clicks int64, flags uint64) error
	// scroll posts whole wheel lines: dy up, dx left.
	scroll(dy, dx int32, flags uint64) error
	// text types s with one key press and release.
	text(s string) error
	// doubleClick is the user's double click interval.
	doubleClick() time.Duration
	// screen is the main display, used when no monitor was set.
	screen() image.Rectangle
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
	now func() time.Time

	mu      sync.Mutex
	closed  bool
	monitor image.Rectangle
	held    []held
	pos     image.Point // last mouse position in points
	wheelX  int         // wheel remainders below one notch, in 1/120 notch
	wheelY  int
	logged  map[string]bool

	// The last button press, for click counting.
	lastButton uint32
	lastDown   time.Time
	lastPos    image.Point
	clicks     int64
}

// held is a key or button the injector holds down.
type held struct {
	id     string // "key:KeyA" or "button:1"
	key    bool
	code   uint16 // virtual key code
	button uint32 // CGMouseButton
	clicks int64  // click count of the press, repeated on the release
}

// New returns an injector that posts CGEvents. Without the Accessibility
// permission macOS drops them; that is checked before a session starts.
func New(log *slog.Logger, onInjected func()) *Injector {
	return newInjector(log, onInjected, newBackend())
}

func newInjector(log *slog.Logger, onInjected func(), be backend) *Injector {
	if log == nil {
		log = slog.Default()
	}
	return &Injector{OnInjected: onInjected, log: log, be: be, now: time.Now}
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

// SetMonitor sets the shared monitor in global points; normalized mouse
// coordinates (0..65535) are mapped onto it. Until it is called, or when r
// is empty, the main display is used.
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
		j.log.Debug("input message (no backend)", "type", typ, "msg", m)
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
		j.pos = image.Pt(scale(v.X, mon.Min.X, mon.Dx()), scale(v.Y, mon.Min.Y, mon.Dy()))
		typ, button := uint32(evMoved), uint32(btnLeft)
		// While a button is held a move is a drag of the first held one.
		for _, h := range j.held {
			if !h.key {
				typ, button = dragType(h.button), h.button
				break
			}
		}
		return j.sendMouse(typ, button, 0)

	case input.MouseButton:
		b, ok := cgButton(v.Button)
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
		clicks := j.countClick(b)
		down, _ := buttonTypes(b)
		if !j.sendMouse(down, b, clicks) {
			return false
		}
		j.held = append(j.held, held{id: id, button: b, clicks: clicks})
		return true

	case input.Wheel:
		return j.wheel(v)

	case input.Key:
		code, ok := vk[v.Code]
		if !ok {
			j.logOnce("code:"+v.Code, "input key code not supported", "code", v.Code)
			return false
		}
		id := "key:" + v.Code
		if !v.Down {
			return j.lift(id)
		}
		wasHeld := j.isHeld(id)
		if !wasHeld {
			j.held = append(j.held, held{id: id, key: true, code: code})
		}
		// A press of a held key is auto-repeat: send it, track it once.
		if !j.sendKey(code, true) {
			if !wasHeld {
				j.held = j.held[:len(j.held)-1]
			}
			return false
		}
		return true

	case input.Text:
		return j.text(v.Text)

	case input.ReleaseAll:
		return j.releaseLocked()
	}
	return false
}

// countClick returns the click count of a press of button b: one more than
// the last press when it is the same button, within the double click
// interval and close by, else 1.
func (j *Injector) countClick(b uint32) int64 {
	now := j.now()
	d := j.pos.Sub(j.lastPos)
	if b == j.lastButton && j.clicks > 0 && now.Sub(j.lastDown) <= j.be.doubleClick() &&
		d.X >= -clickSlop && d.X <= clickSlop && d.Y >= -clickSlop && d.Y <= clickSlop {
		j.clicks++
	} else {
		j.clicks = 1
	}
	j.lastButton, j.lastDown, j.lastPos = b, now, j.pos
	return j.clicks
}

// cgButton maps an input button to a CGMouseButton.
func cgButton(b uint8) (uint32, bool) {
	switch b {
	case input.ButtonLeft:
		return btnLeft, true
	case input.ButtonRight:
		return btnRight, true
	case input.ButtonMiddle:
		return btnCenter, true
	case input.ButtonBack:
		return btnBack, true
	case input.ButtonForward:
		return btnFwd, true
	}
	return 0, false
}

// buttonTypes returns the event types that press and release b.
func buttonTypes(b uint32) (down, up uint32) {
	switch b {
	case btnLeft:
		return evLeftDown, evLeftUp
	case btnRight:
		return evRightDown, evRightUp
	}
	return evOtherDown, evOtherUp
}

// dragType returns the type of a move while b is held.
func dragType(b uint32) uint32 {
	switch b {
	case btnLeft:
		return evLeftDragged
	case btnRight:
		return evRightDragged
	}
	return evOtherDragged
}

// flags returns the modifiers the injector holds as CGEventFlags.
func (j *Injector) flags() uint64 {
	var f uint64
	for _, h := range j.held {
		if h.key {
			f |= modifierFlag(h.code)
		}
	}
	return f
}

// wheel scrolls one line per whole notch; smaller amounts add up until they
// make one.
func (j *Injector) wheel(w input.Wheel) bool {
	j.wheelY += int(w.DY)
	j.wheelX += int(w.DX)
	dy, dx := j.wheelY/120, j.wheelX/120
	j.wheelY -= dy * 120
	j.wheelX -= dx * 120
	if dy == 0 && dx == 0 {
		return false
	}
	// input: DY positive is away from the user (up), DX positive is right;
	// macOS: positive lines scroll up and left.
	if err := j.be.scroll(int32(dy), int32(-dx), j.flags()); err != nil {
		j.logOnce("scroll:"+err.Error(), "CGEvent scroll failed", "err", err)
		return false
	}
	return true
}

// text types s. Line breaks press Return and "\t" presses Tab, because
// applications act on those keys rather than on the characters.
func (j *Injector) text(s string) bool {
	sent := false
	tap := func(code uint16) {
		if j.sendKey(code, true) && j.sendKey(code, false) {
			sent = true
		}
	}
	prevCR := false
	for _, r := range s {
		cr := r == '\r'
		switch {
		case cr:
			tap(vk["Enter"])
		case r == '\n':
			if !prevCR {
				tap(vk["Enter"])
			}
		case r == '\t':
			tap(vk["Tab"])
		case r < 0x20 || r == 0x7f:
			// Other control characters have no key.
		default:
			if err := j.be.text(string(r)); err != nil {
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

// release sends the release of h, which is no longer in j.held, so a
// modifier's flag is already off.
func (j *Injector) release(h held) bool {
	if h.key {
		return j.sendKey(h.code, false)
	}
	_, up := buttonTypes(h.button)
	return j.sendMouse(up, h.button, h.clicks)
}

// sendKey posts a key with the held modifiers and logs a failure once.
func (j *Injector) sendKey(code uint16, down bool) bool {
	f := j.flags() | keyFlags(code)
	if err := j.be.key(code, down, f); err != nil {
		j.logOnce("key:"+err.Error(), "CGEvent key failed", "err", err)
		return false
	}
	return true
}

// sendMouse posts a mouse event at the last position.
func (j *Injector) sendMouse(typ, button uint32, clicks int64) bool {
	if err := j.be.mouse(typ, button, float64(j.pos.X), float64(j.pos.Y), clicks, j.flags()); err != nil {
		j.logOnce("mouse:"+err.Error(), "CGEvent mouse failed", "err", err)
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
	for len(j.held) > 0 {
		h := j.held[len(j.held)-1]
		j.held = j.held[:len(j.held)-1]
		if j.release(h) {
			sent = true
		}
	}
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

// Close releases everything. Further Input is ignored. It is safe to call
// more than once.
func (j *Injector) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.releaseLocked()
	j.closed = true
	if j.be != nil {
		j.be.close()
	}
	return nil
}

// scale maps n in 0..65535 to a point in [origin, origin+size), rounding.
func scale(n uint16, origin, size int) int {
	if size <= 1 {
		return origin
	}
	return origin + int((int64(n)*int64(size-1)+32767)/65535)
}
