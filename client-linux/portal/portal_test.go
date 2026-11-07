package portal

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// privateBus starts a dbus-daemon of its own and points the session bus at
// it for the test.
func privateBus(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("no dbus-daemon")
	}
	// A config of its own, without service directories: the session
	// config would start the real portal.
	conf := filepath.Join(t.TempDir(), "bus.conf")
	err := os.WriteFile(conf, []byte(`<busconfig><type>session</type>
<listen>unix:tmpdir=`+t.TempDir()+`</listen>
<auth>EXTERNAL</auth>
<policy context="default"><allow send_destination="*" eavesdrop="true"/><allow eavesdrop="true"/><allow own="*"/></policy>
</busconfig>`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("dbus-daemon", "--config-file="+conf, "--nofork", "--print-address")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	addr, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", strings.TrimSpace(addr))
}

// fakePortal implements the parts of the portal the client uses.
type fakePortal struct {
	conn    *dbus.Conn
	decline bool // answer Start with "cancelled"
	noToken bool // RemoteDesktop version 1: no persistence

	mu      sync.Mutex
	session dbus.ObjectPath
	devOpts map[string]dbus.Variant
	srcOpts map[string]dbus.Variant
	calls   []string
	closed  bool
}

func (f *fakePortal) respond(sender dbus.Sender, opts map[string]dbus.Variant, code uint32, results map[string]dbus.Variant) dbus.ObjectPath {
	tok, _ := opts["handle_token"].Value().(string)
	path := requestPath(string(sender), tok)
	go func() {
		time.Sleep(10 * time.Millisecond)
		f.conn.Emit(path, ifaceRequest+".Response", code, results)
	}()
	return path
}

type fakeRemote struct{ *fakePortal }

func (f fakeRemote) CreateSession(sender dbus.Sender, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	st, _ := opts["session_handle_token"].Value().(string)
	f.mu.Lock()
	f.session = dbus.ObjectPath(objectPath + "/session/x/" + st)
	s := f.session
	f.mu.Unlock()
	f.conn.Export(fakeSession{f.fakePortal}, s, ifaceSession)
	return f.respond(sender, opts, 0, map[string]dbus.Variant{"session_handle": dbus.MakeVariant(string(s))}), nil
}

func (f fakeRemote) SelectDevices(sender dbus.Sender, s dbus.ObjectPath, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	f.mu.Lock()
	f.devOpts = opts
	f.mu.Unlock()
	return f.respond(sender, opts, 0, nil), nil
}

func (f fakeRemote) Start(sender dbus.Sender, s dbus.ObjectPath, parent string, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	if f.decline {
		return f.respond(sender, opts, 1, nil), nil
	}
	type pt struct{ X, Y int32 }
	streams := []struct {
		Node  uint32
		Props map[string]dbus.Variant
	}{{Node: 42, Props: map[string]dbus.Variant{
		"position": dbus.MakeVariant(pt{10, 20}),
		"size":     dbus.MakeVariant(pt{1280, 720}),
	}}}
	res := map[string]dbus.Variant{
		"devices": dbus.MakeVariant(uint32(3)),
		"streams": dbus.MakeVariant(streams),
	}
	if !f.noToken {
		res["restore_token"] = dbus.MakeVariant("tok-2")
	}
	return f.respond(sender, opts, 0, res), nil
}

func (f fakeRemote) record(call string) *dbus.Error {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	return nil
}

func (f fakeRemote) NotifyPointerMotionAbsolute(s dbus.ObjectPath, o map[string]dbus.Variant, stream uint32, x, y float64) *dbus.Error {
	return f.record("motion " + fmtArgs(stream, x, y))
}
func (f fakeRemote) NotifyPointerButton(s dbus.ObjectPath, o map[string]dbus.Variant, b int32, st uint32) *dbus.Error {
	return f.record("button " + fmtArgs(b, st))
}
func (f fakeRemote) NotifyPointerAxisDiscrete(s dbus.ObjectPath, o map[string]dbus.Variant, axis uint32, steps int32) *dbus.Error {
	return f.record("axis " + fmtArgs(axis, steps))
}
func (f fakeRemote) NotifyKeyboardKeycode(s dbus.ObjectPath, o map[string]dbus.Variant, k int32, st uint32) *dbus.Error {
	return f.record("keycode " + fmtArgs(k, st))
}
func (f fakeRemote) NotifyKeyboardKeysym(s dbus.ObjectPath, o map[string]dbus.Variant, k int32, st uint32) *dbus.Error {
	return f.record("keysym " + fmtArgs(k, st))
}

type fakeCast struct{ *fakePortal }

func (f fakeCast) SelectSources(sender dbus.Sender, s dbus.ObjectPath, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	f.mu.Lock()
	f.srcOpts = opts
	f.mu.Unlock()
	return f.respond(sender, opts, 0, nil), nil
}

func (f fakeCast) OpenPipeWireRemote(s dbus.ObjectPath, opts map[string]dbus.Variant) (dbus.UnixFD, *dbus.Error) {
	r, w, err := os.Pipe()
	if err != nil {
		return 0, dbus.MakeFailedError(err)
	}
	w.WriteString("pipewire")
	w.Close()
	return dbus.UnixFD(r.Fd()), nil // godbus dups it into the message
}

type fakeSession struct{ *fakePortal }

func (f fakeSession) Close() *dbus.Error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

type fakeProps struct{ *fakePortal }

func (f fakeProps) Get(iface, prop string) (dbus.Variant, *dbus.Error) {
	if prop != "version" {
		return dbus.Variant{}, dbus.MakeFailedError(errors.New("no such property"))
	}
	v := uint32(2)
	if f.noToken && iface == ifaceRemote {
		v = 1
	}
	return dbus.MakeVariant(v), nil
}

func fmtArgs(a ...any) string { return strings.TrimSuffix(fmt.Sprintln(a...), "\n") }

func newFake(t *testing.T, f *fakePortal) {
	t.Helper()
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	f.conn = conn
	for iface, v := range map[string]any{
		ifaceRemote:                       fakeRemote{f},
		ifaceCast:                         fakeCast{f},
		"org.freedesktop.DBus.Properties": fakeProps{f},
	} {
		if err := conn.Export(v, objectPath, iface); err != nil {
			t.Fatal(err)
		}
	}
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("request name: %v %v", reply, err)
	}
}

func TestStart(t *testing.T) {
	privateBus(t)
	f := &fakePortal{}
	newFake(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := Start(ctx, Options{RestoreToken: "tok-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Streams) != 1 || s.Streams[0].Node != 42 || s.Streams[0].Rect != image.Rect(10, 20, 1290, 740) {
		t.Fatalf("streams = %+v", s.Streams)
	}
	if s.RestoreToken != "tok-2" {
		t.Errorf("restore token = %q", s.RestoreToken)
	}
	f.mu.Lock()
	if got := f.devOpts["restore_token"].Value(); got != "tok-1" {
		t.Errorf("restore token sent = %v", got)
	}
	if got := f.devOpts["persist_mode"].Value(); got != uint32(2) {
		t.Errorf("persist mode = %v", got)
	}
	if got := f.srcOpts["cursor_mode"].Value(); got != uint32(cursorEmbedded) {
		t.Errorf("cursor mode = %v", got)
	}
	f.mu.Unlock()

	fd, err := s.OpenPipeWireRemote()
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 16)
	n, _ := fd.Read(b)
	fd.Close()
	if string(b[:n]) != "pipewire" {
		t.Errorf("fd read %q", b[:n])
	}

	for _, err := range []error{
		s.PointerMotionAbsolute(42, 1.5, 2),
		s.PointerButton(0x110, true),
		s.PointerAxisDiscrete(AxisVertical, -1),
		s.KeyboardKeycode(30, false),
		s.KeyboardKeysym(0x61, true),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	want := []string{"motion 42 1.5 2", "button 272 1", "axis 0 -1", "keycode 30 0", "keysym 97 1"}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %q, want %q", f.calls, want)
	}
	f.mu.Unlock()

	// The compositor ends the session.
	f.conn.Emit(s.path, ifaceSession+".Closed", map[string]dbus.Variant{})
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done not closed after Closed")
	}
	if err := s.KeyboardKeycode(30, true); err == nil {
		t.Error("input after Closed worked")
	}
	s.Close()
}

func TestStartDeclined(t *testing.T) {
	privateBus(t)
	f := &fakePortal{decline: true, noToken: true}
	newFake(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Start(ctx, Options{RestoreToken: "tok-1"})
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("err = %v, want ErrCancelled", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.devOpts["restore_token"]; ok {
		t.Error("restore token sent to a version 1 portal")
	}
}

func TestUnavailable(t *testing.T) {
	privateBus(t)
	if _, err := Version(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	_, err := Start(context.Background(), Options{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Start err = %v, want ErrUnavailable", err)
	}
}
