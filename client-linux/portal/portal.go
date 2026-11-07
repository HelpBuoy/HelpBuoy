// Package portal shares a Wayland desktop through the XDG desktop portal:
// one combined RemoteDesktop and ScreenCast session gives a PipeWire stream
// of the monitor the user picks and lets the client inject pointer and
// keyboard input into it. The compositor asks the user before it starts.
//
// It speaks D-Bus with godbus, so it needs no C libraries. See
// https://flatpak.github.io/xdg-desktop-portal/docs/ for the interfaces.
package portal

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/godbus/dbus/v5"
)

const (
	busName      = "org.freedesktop.portal.Desktop"
	objectPath   = "/org/freedesktop/portal/desktop"
	ifaceRemote  = "org.freedesktop.portal.RemoteDesktop"
	ifaceCast    = "org.freedesktop.portal.ScreenCast"
	ifaceRequest = "org.freedesktop.portal.Request"
	ifaceSession = "org.freedesktop.portal.Session"
)

// Device types of RemoteDesktop.SelectDevices.
const (
	deviceKeyboard = 1
	devicePointer  = 2
)

// Source and cursor modes of ScreenCast.SelectSources.
const (
	sourceMonitor  = 1
	cursorEmbedded = 2
)

// persistUntilRevoked keeps the permission until the user revokes it, so a
// restore token from an earlier session skips the monitor choice.
const persistUntilRevoked = 2

// Axes of NotifyPointerAxisDiscrete.
const (
	AxisVertical   = 0
	AxisHorizontal = 1
)

// ErrCancelled is returned by Start when the user declined the portal's
// dialog.
var ErrCancelled = errors.New("portal: sharing was declined")

// ErrUnavailable is returned when the desktop has no remote desktop portal.
var ErrUnavailable = errors.New("portal: this desktop offers no remote desktop portal")

// Stream is one shared monitor.
type Stream struct {
	Node uint32          // PipeWire node id
	Rect image.Rectangle // in compositor (logical) coordinates; Min may be zero when unknown
}

// Options configure Start.
type Options struct {
	// RestoreToken is the token an earlier session returned; with it the
	// compositor may skip asking which monitor to share.
	RestoreToken string
	// ParentWindow identifies the window the dialogs belong to, such as
	// "x11:1a00004", or is empty.
	ParentWindow string
}

// Session is a started remote desktop session. Its methods are safe for
// concurrent use.
type Session struct {
	conn    *dbus.Conn
	path    dbus.ObjectPath
	obj     dbus.BusObject
	Streams []Stream
	// RestoreToken is the token to pass to the next Start, or empty.
	RestoreToken string

	closed    atomic.Bool
	done      chan struct{}
	closeOnce sync.Once
}

// Version returns the version of the RemoteDesktop portal, or ErrUnavailable
// when the desktop has none.
func Version() (uint32, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	return version(conn, ifaceRemote)
}

func version(conn *dbus.Conn, iface string) (uint32, error) {
	v, err := conn.Object(busName, objectPath).GetProperty(iface + ".version")
	if err != nil {
		return 0, fmt.Errorf("%w (%v)", ErrUnavailable, err)
	}
	n, ok := v.Value().(uint32)
	if !ok {
		return 0, fmt.Errorf("%w (version is %s)", ErrUnavailable, v.Signature())
	}
	return n, nil
}

// Start creates a session for the keyboard, the pointer and one monitor, and
// starts it, which shows the compositor's dialog. It returns when the user
// answered it, or with ctx's error when ctx ends first.
func Start(ctx context.Context, opt Options) (*Session, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	s, err := start(ctx, conn, opt)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return s, nil
}

func start(ctx context.Context, conn *dbus.Conn, opt Options) (*Session, error) {
	rdVersion, err := version(conn, ifaceRemote)
	if err != nil {
		return nil, err
	}
	if _, err := version(conn, ifaceCast); err != nil {
		return nil, err
	}
	signals := make(chan *dbus.Signal, 16)
	conn.Signal(signals)
	defer conn.RemoveSignal(signals)

	r := &requester{conn: conn, obj: conn.Object(busName, objectPath), signals: signals}
	res, err := r.call(ctx, ifaceRemote+".CreateSession", nil, map[string]dbus.Variant{
		"session_handle_token": dbus.MakeVariant(token("s")),
	})
	if err != nil {
		return nil, fmt.Errorf("portal: create session: %w", err)
	}
	var handle string
	if v, ok := res["session_handle"]; ok {
		switch h := v.Value().(type) {
		case string:
			handle = h
		case dbus.ObjectPath:
			handle = string(h)
		}
	}
	if handle == "" {
		return nil, errors.New("portal: create session: no session handle")
	}
	s := &Session{conn: conn, path: dbus.ObjectPath(handle), obj: r.obj, done: make(chan struct{})}
	ok := false
	defer func() {
		if !ok {
			s.closeSession()
		}
	}()

	devices := map[string]dbus.Variant{
		"types": dbus.MakeVariant(uint32(deviceKeyboard | devicePointer)),
	}
	if rdVersion >= 2 {
		devices["persist_mode"] = dbus.MakeVariant(uint32(persistUntilRevoked))
		if opt.RestoreToken != "" {
			devices["restore_token"] = dbus.MakeVariant(opt.RestoreToken)
		}
	}
	if _, err := r.call(ctx, ifaceRemote+".SelectDevices", []any{s.path}, devices); err != nil {
		return nil, fmt.Errorf("portal: select devices: %w", err)
	}
	// The persistence lives with the RemoteDesktop devices of a combined
	// session; the sources take no token of their own then.
	if _, err := r.call(ctx, ifaceCast+".SelectSources", []any{s.path}, map[string]dbus.Variant{
		"types":       dbus.MakeVariant(uint32(sourceMonitor)),
		"multiple":    dbus.MakeVariant(false),
		"cursor_mode": dbus.MakeVariant(uint32(cursorEmbedded)),
	}); err != nil {
		return nil, fmt.Errorf("portal: select sources: %w", err)
	}
	res, err = r.call(ctx, ifaceRemote+".Start", []any{s.path, opt.ParentWindow}, nil)
	if err != nil {
		return nil, fmt.Errorf("portal: start: %w", err)
	}
	if d, ok := res["devices"].Value().(uint32); !ok || d&devicePointer == 0 || d&deviceKeyboard == 0 {
		return nil, errors.New("portal: start: the keyboard and the pointer were not both granted")
	}
	s.Streams, err = parseStreams(res["streams"])
	if err != nil {
		return nil, fmt.Errorf("portal: start: %w", err)
	}
	if t, ok := res["restore_token"].Value().(string); ok {
		s.RestoreToken = t
	}

	// The compositor or the user can end the session (GNOME's "Stop
	// sharing" button): Done tells.
	if err := conn.AddMatchSignal(dbus.WithMatchObjectPath(s.path), dbus.WithMatchInterface(ifaceSession), dbus.WithMatchMember("Closed")); err != nil {
		return nil, err
	}
	closed := make(chan *dbus.Signal, 4)
	conn.Signal(closed)
	go func() {
		for sig := range closed {
			if sig.Path == s.path && sig.Name == ifaceSession+".Closed" {
				s.closed.Store(true)
				s.closeOnce.Do(func() { close(s.done) })
			}
		}
		// The connection is gone.
		s.closeOnce.Do(func() { close(s.done) })
	}()
	ok = true
	return s, nil
}

// parseStreams reads the a(ua{sv}) streams of a Start response.
func parseStreams(v dbus.Variant) ([]Stream, error) {
	var raw []struct {
		Node  uint32
		Props map[string]dbus.Variant
	}
	if v.Value() == nil {
		return nil, errors.New("no stream")
	}
	if err := dbus.Store([]any{v.Value()}, &raw); err != nil {
		return nil, fmt.Errorf("streams: %w", err)
	}
	var out []Stream
	for _, r := range raw {
		st := Stream{Node: r.Node}
		var pos, size struct{ X, Y int32 }
		if p, ok := r.Props["position"]; ok {
			_ = dbus.Store([]any{p.Value()}, &pos)
		}
		if p, ok := r.Props["size"]; ok {
			if err := dbus.Store([]any{p.Value()}, &size); err != nil {
				return nil, fmt.Errorf("stream size: %w", err)
			}
		}
		st.Rect = image.Rect(int(pos.X), int(pos.Y), int(pos.X+size.X), int(pos.Y+size.Y))
		out = append(out, st)
	}
	if len(out) == 0 {
		return nil, errors.New("no stream")
	}
	return out, nil
}

// Done is closed when the compositor or the user ended the session, or the
// bus connection was lost.
func (s *Session) Done() <-chan struct{} { return s.done }

// OpenPipeWireRemote returns a connection to PipeWire that can only see
// this session's streams.
func (s *Session) OpenPipeWireRemote() (*os.File, error) {
	var fd dbus.UnixFD
	err := s.obj.Call(ifaceCast+".OpenPipeWireRemote", 0, s.path, map[string]dbus.Variant{}).Store(&fd)
	if err != nil {
		return nil, fmt.Errorf("portal: open PipeWire remote: %w", err)
	}
	return os.NewFile(uintptr(fd), "pipewire"), nil
}

// notify calls one of the RemoteDesktop Notify methods.
func (s *Session) notify(method string, args ...any) error {
	if s.closed.Load() {
		return errors.New("portal: session closed")
	}
	all := append([]any{s.path, map[string]dbus.Variant{}}, args...)
	return s.obj.Call(ifaceRemote+".Notify"+method, 0, all...).Err
}

// PointerMotionAbsolute moves the pointer to x, y in the stream's logical
// coordinates.
func (s *Session) PointerMotionAbsolute(stream uint32, x, y float64) error {
	return s.notify("PointerMotionAbsolute", stream, x, y)
}

// PointerButton presses or releases an evdev button such as BTN_LEFT.
func (s *Session) PointerButton(button int32, pressed bool) error {
	return s.notify("PointerButton", button, state(pressed))
}

// PointerAxisDiscrete scrolls steps notches; negative is up or left.
func (s *Session) PointerAxisDiscrete(axis uint32, steps int32) error {
	return s.notify("PointerAxisDiscrete", axis, steps)
}

// KeyboardKeycode presses or releases an evdev key code.
func (s *Session) KeyboardKeycode(code int32, pressed bool) error {
	return s.notify("KeyboardKeycode", code, state(pressed))
}

// KeyboardKeysym presses or releases the key of an X keysym.
func (s *Session) KeyboardKeysym(sym int32, pressed bool) error {
	return s.notify("KeyboardKeysym", sym, state(pressed))
}

func state(pressed bool) uint32 {
	if pressed {
		return 1
	}
	return 0
}

// Close ends the session and the bus connection. It is safe to call more
// than once.
func (s *Session) Close() error {
	s.closeSession()
	s.closeOnce.Do(func() { close(s.done) })
	return s.conn.Close()
}

func (s *Session) closeSession() {
	if s.closed.Swap(true) {
		return
	}
	s.conn.Object(busName, s.path).Call(ifaceSession+".Close", 0)
}

// requester calls portal methods that answer with a Request object.
type requester struct {
	conn    *dbus.Conn
	obj     dbus.BusObject
	signals chan *dbus.Signal
}

// call calls method with args followed by options (which gets a
// handle_token) and waits for the Response of its request.
func (r *requester) call(ctx context.Context, method string, args []any, options map[string]dbus.Variant) (map[string]dbus.Variant, error) {
	if options == nil {
		options = map[string]dbus.Variant{}
	}
	tok := token("r")
	options["handle_token"] = dbus.MakeVariant(tok)
	// Subscribe before calling, at the path the request will get, so the
	// response can't come before the match.
	path := requestPath(r.conn.Names()[0], tok)
	match := []dbus.MatchOption{dbus.WithMatchObjectPath(path), dbus.WithMatchInterface(ifaceRequest), dbus.WithMatchMember("Response")}
	if err := r.conn.AddMatchSignal(match...); err != nil {
		return nil, err
	}
	defer r.conn.RemoveMatchSignal(match...)

	var got dbus.ObjectPath
	if err := r.obj.CallWithContext(ctx, method, 0, append(args, options)...).Store(&got); err != nil {
		return nil, err
	}
	if got != path {
		// Portals before 0.9 picked their own path.
		path = got
		match = []dbus.MatchOption{dbus.WithMatchObjectPath(path), dbus.WithMatchInterface(ifaceRequest), dbus.WithMatchMember("Response")}
		if err := r.conn.AddMatchSignal(match...); err != nil {
			return nil, err
		}
		defer r.conn.RemoveMatchSignal(match...)
	}
	for {
		select {
		case <-ctx.Done():
			r.conn.Object(busName, path).Call(ifaceRequest+".Close", 0)
			return nil, ctx.Err()
		case sig, ok := <-r.signals:
			if !ok {
				return nil, errors.New("bus connection lost")
			}
			if sig.Path != path || sig.Name != ifaceRequest+".Response" || len(sig.Body) < 2 {
				continue
			}
			code, _ := sig.Body[0].(uint32)
			results, _ := sig.Body[1].(map[string]dbus.Variant)
			switch code {
			case 0:
				return results, nil
			case 1:
				return nil, ErrCancelled
			default:
				return nil, errors.New("the portal ended the request")
			}
		}
	}
}

// requestPath is where a portal puts the request of a call with handle
// token tok from the connection with unique name sender.
func requestPath(sender, tok string) dbus.ObjectPath {
	s := strings.ReplaceAll(strings.TrimPrefix(sender, ":"), ".", "_")
	return dbus.ObjectPath(objectPath + "/request/" + s + "/" + tok)
}

var tokens atomic.Uint64

// token returns a new handle token, unique within this process.
func token(prefix string) string {
	return fmt.Sprintf("helpbuoy_%s%d_%d", prefix, os.Getpid(), tokens.Add(1))
}
