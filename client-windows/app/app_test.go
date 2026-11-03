package app

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/helpbuoy/client-windows/config"
	"github.com/mevdschee/helpbuoy/client-windows/identity"
	"github.com/mevdschee/helpbuoy/client-windows/internal/fakeserver"
	"github.com/mevdschee/helpbuoy/client-windows/session"
	"github.com/mevdschee/helpbuoy/proto/appkey"
)

type fakeUI struct {
	views chan View

	mu     sync.Mutex
	refuse bool
	// gate, if set, holds ConfirmTechie until it is closed (then the
	// answer is "allow") or the question becomes moot.
	gate chan struct{}
}

// answer sets how ConfirmTechie answers from now on.
func (u *fakeUI) answer(refuse bool, gate chan struct{}) {
	u.mu.Lock()
	u.refuse, u.gate = refuse, gate
	u.mu.Unlock()
}

func (u *fakeUI) Render(v View) { u.views <- v }
func (u *fakeUI) ConfirmTechie(ctx context.Context, _ session.Techie, _ string) bool {
	u.mu.Lock()
	refuse, gate := u.refuse, u.gate
	u.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return false
		}
	}
	return !refuse
}

// wait returns the first rendered View for which ok is true.
func (u *fakeUI) wait(t *testing.T, what string, ok func(View) bool) View {
	t.Helper()
	timeout := time.After(5 * time.Second)
	var last View
	for {
		select {
		case v := <-u.views:
			if ok(v) {
				return v
			}
			last = v
		case <-timeout:
			t.Fatalf("timed out waiting for %s; last view %+v", what, last)
		}
	}
}

type runner struct{}

func (runner) Run(ctx context.Context, ch io.ReadWriteCloser, _ ssh.PublicKey) error {
	done := make(chan struct{})
	go func() { io.Copy(io.Discard, ch); close(done) }()
	select {
	case <-ctx.Done():
	case <-done:
	}
	return nil
}

type env struct {
	t       *testing.T
	dir     string
	key     *appkey.Key
	keyErr  error
	loads   int
	removed int
	server  *fakeserver.Server
}

func newEnv(t *testing.T) *env {
	k, err := identity.Ephemeral("anna@PC")
	if err != nil {
		t.Fatal(err)
	}
	server, err := fakeserver.Start("", fakeserver.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	return &env{t: t, dir: t.TempDir(), key: k, server: server}
}

func (e *env) fp() string { return e.key.Fingerprint }

func (e *env) controller() (*Controller, *fakeUI) {
	ui := &fakeUI{views: make(chan View, 256)}
	c := New(Deps{
		UI:    ui,
		Store: config.Store{Dir: e.dir},
		LoadKey: func() (*appkey.Key, error) {
			e.loads++
			return e.key, e.keyErr
		},
		RemoveKey: func() error {
			e.removed++
			k, _ := identity.Ephemeral("anna@PC")
			e.key = k
			return config.Store{Dir: e.dir}.Reset()
		},
		NewRunner:   func(*appkey.Key) session.Runner { return runner{} },
		AppVersion:  "test",
		UserMachine: "anna@PC",
		Tune: func(o *session.Options) {
			o.DialTimeout = time.Second
			o.MinBackoff, o.MaxBackoff = 10*time.Millisecond, 20*time.Millisecond
			o.ParseKey = func(line string) (ssh.PublicKey, error) {
				k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
				return k, err
			}
		},
	})
	e.t.Cleanup(c.Close)
	return c, ui
}

func (e *env) config() *config.Config {
	cfg, err := config.Store{Dir: e.dir}.Load()
	if err != nil {
		e.t.Fatal(err)
	}
	return cfg
}

func screenIs(s Screen) func(View) bool { return func(v View) bool { return v.Screen == s } }

func TestFirstRunToMain(t *testing.T) {
	e := newEnv(t)
	c, ui := e.controller()
	c.Start()
	v := ui.wait(t, "address screen", screenIs(ScreenAddress))
	if v.Address != "" || v.Error != "" || v.Connecting {
		t.Fatalf("first view %+v", v)
	}

	c.Connect("bad address/path")
	v = ui.wait(t, "parse error", func(v View) bool { return v.Error != "" })
	if v.Screen != ScreenAddress || v.Connecting {
		t.Fatalf("%+v", v)
	}

	c.Connect(e.server.Addr())
	v = ui.wait(t, "key screen", screenIs(ScreenKey))
	if v.KeyLine != e.key.Line || v.KeyFingerprint != e.key.Fingerprint ||
		v.ServerFingerprint != ssh.FingerprintSHA256(e.server.HostKey()) {
		t.Fatalf("key view %+v", v)
	}
	if cfg := e.config(); cfg.LastAddress != e.server.Addr() || cfg.Registered() {
		t.Fatalf("config while pending %+v", cfg)
	}

	e.server.Register(e.fp())
	v = ui.wait(t, "main screen", func(v View) bool { return v.Screen == ScreenMain && v.Ready })
	if v.KeyComment != e.key.Comment || v.KeyComment == "" || v.Notice != "" || v.Error != "" {
		t.Fatalf("main view %+v", v)
	}
	cfg := e.config()
	if !cfg.Registered() || cfg.ServerAddress != e.server.Addr() ||
		cfg.ServerHostKey != identity.KeyLine(e.server.HostKey(), "") {
		t.Fatalf("config %+v", cfg)
	}

	// The next start connects to the same server by itself.
	c.Close()
	waitOffline(t, e.server, e.fp())
	c2, ui2 := e.controller()
	startAndConnect(t, c2, ui2, e.server.Addr())
	ui2.wait(t, "main after restart", readyMain)
}

// TestAutoConnectFails: when the stored server can't be reached at start,
// the address screen shows why and the address can be changed.
func TestAutoConnectFails(t *testing.T) {
	e := newEnv(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	config.Store{Dir: e.dir}.Save(&config.Config{LastAddress: addr})
	c, ui := e.controller()
	c.Start()
	v := ui.wait(t, "error", func(v View) bool { return v.Error != "" })
	if v.Screen != ScreenAddress || v.Connecting || v.Address != addr {
		t.Fatalf("%+v", v)
	}
	c.Connect(e.server.Addr()) // now the address can be changed
	ui.wait(t, "key screen", screenIs(ScreenKey))
}

func registered(t *testing.T, e *env) {
	t.Helper()
	e.server.Register(e.fp())
	config.Store{Dir: e.dir}.Save(&config.Config{
		ServerAddress: e.server.Addr(),
		ServerHostKey: identity.KeyLine(e.server.HostKey(), ""),
	})
}

// connectTechie plays techie alice connecting through the fake server.
func connectTechie(t *testing.T, r *fakeserver.Server, fp string) *appkey.Key {
	t.Helper()
	techie, _ := identity.Ephemeral("alice")
	if _, err := r.Connect(fp, "alice", techie.Line); err != nil {
		t.Fatal(err)
	}
	return techie
}

func TestTechieConnects(t *testing.T) {
	e := newEnv(t)
	registered(t, e)
	c, ui := e.controller()
	startAndConnect(t, c, ui, e.server.Addr())
	ui.wait(t, "ready", readyMain)

	// The user refuses: no session, still ready.
	ui.answer(true, nil)
	connectTechie(t, e.server, e.fp())
	select {
	case <-e.server.Sessions():
		t.Fatal("session despite refusal")
	case <-time.After(100 * time.Millisecond):
	}
	ui.wait(t, "ready", readyMain)
	if len(e.config().KnownTechies) != 0 {
		t.Fatal("refused techie pinned")
	}

	// While the user is asked the window says so.
	gate := make(chan struct{})
	ui.answer(false, gate)
	techie := connectTechie(t, e.server, e.fp())
	ui.wait(t, "asking", func(v View) bool { return v.Screen == ScreenMain && v.Asking && v.Notice != "" })
	close(gate)
	sess := <-e.server.Sessions()
	v := ui.wait(t, "in session", func(v View) bool { return v.InSession })
	if v.Screen != ScreenMain || v.Ready {
		t.Fatalf("session view %+v", v)
	}
	if got := e.config().KnownTechies["alice"]; got != identity.KeyLine(techie.Public, "") {
		t.Fatalf("pinned %q", got)
	}
	sess.Channel.Close() // the techie ends the session
	ui.wait(t, "back", func(v View) bool { return !v.InSession && readyMain(v) })
}

func TestConnectError(t *testing.T) {
	e := newEnv(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	c, ui := e.controller()
	c.Start()
	c.Connect(addr)
	ui.wait(t, "connecting", func(v View) bool { return v.Connecting })
	v := ui.wait(t, "error", func(v View) bool { return v.Error != "" })
	if v.Screen != ScreenAddress || v.Connecting || v.Address != addr {
		t.Fatalf("%+v", v)
	}
	if e.config().LastAddress != "" {
		t.Fatal("failed address stored")
	}
}

func TestAutoConnectBeforeRegistration(t *testing.T) {
	e := newEnv(t)
	c, ui := e.controller()
	c.Start()
	c.Connect(e.server.Addr())
	ui.wait(t, "key screen", screenIs(ScreenKey))
	c.Close()

	// The next start goes back to the same server and shows the key again.
	c2, ui2 := e.controller()
	startAndConnect(t, c2, ui2, e.server.Addr())
	ui2.wait(t, "key screen", screenIs(ScreenKey))
}

func TestRemoveKey(t *testing.T) {
	e := newEnv(t)
	registered(t, e)
	c, ui := e.controller()
	startAndConnect(t, c, ui, e.server.Addr())
	ui.wait(t, "main", screenIs(ScreenMain))
	old := e.key.Fingerprint
	if err := c.RemoveKey(); err != nil {
		t.Fatal(err)
	}
	v := ui.wait(t, "address screen", screenIs(ScreenAddress))
	if e.removed != 1 || v.KeyFingerprint == old || v.KeyFingerprint != e.key.Fingerprint {
		t.Fatalf("removed %d, key %s (old %s)", e.removed, v.KeyFingerprint, old)
	}
	if e.config().Registered() {
		t.Fatal("config kept")
	}
}

func TestDamagedKey(t *testing.T) {
	e := newEnv(t)
	e.keyErr = fmt.Errorf("%w: id_ed25519: bad PEM", appkey.ErrDamaged)
	e.key = nil
	c, ui := e.controller()
	c.Start()
	v := ui.wait(t, "key error", screenIs(ScreenKeyError))
	if !strings.Contains(v.Error, "bad PEM") || v.KeyDir != e.dir {
		t.Fatalf("%+v", v)
	}
	c.Connect(e.server.Addr()) // ignored without a key
	e.keyErr = nil
	e.key, _ = identity.Ephemeral("x")
	if err := c.RemoveKey(); err != nil {
		t.Fatal(err)
	}
	ui.wait(t, "address screen", screenIs(ScreenAddress))
}

func TestUnregisteredShowsKeyAgain(t *testing.T) {
	e := newEnv(t)
	registered(t, e)
	// A server that does not know the key (a techie removed it): pin a
	// fresh fake server on which the key is not registered.
	server2, err := fakeserver.Start("", fakeserver.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer server2.Close()
	config.Store{Dir: e.dir}.Save(&config.Config{
		ServerAddress: server2.Addr(),
		ServerHostKey: identity.KeyLine(server2.HostKey(), ""),
	})
	c, ui := e.controller()
	startAndConnect(t, c, ui, server2.Addr())
	ui.wait(t, "key screen", screenIs(ScreenKey))
	server2.Register(e.fp())
	ui.wait(t, "main", func(v View) bool { return v.Screen == ScreenMain && v.Ready })
}

func TestCapitalize(t *testing.T) {
	if capitalize("server address is empty") != "Server address is empty" || capitalize("") != "" {
		t.Fatal()
	}
}

func TestServerKeyChanged(t *testing.T) {
	e := newEnv(t)
	e.server.Register(e.fp())
	old, _ := identity.Ephemeral("old server")
	config.Store{Dir: e.dir}.Save(&config.Config{
		ServerAddress: e.server.Addr(),
		ServerHostKey: old.Line,
	})
	c, ui := e.controller()
	startAndConnect(t, c, ui, e.server.Addr())
	v := ui.wait(t, "warning", func(v View) bool { return v.ServerKeyChange != nil })
	if v.Screen != ScreenAddress {
		t.Fatalf("warning on %v", v.Screen)
	}
	newFP := ssh.FingerprintSHA256(e.server.HostKey())
	if v.ServerKeyChange.OldFingerprint != old.Fingerprint || v.ServerKeyChange.NewFingerprint != newFP || v.Error == "" {
		t.Fatalf("view %+v", v)
	}
	if e.server.Online(e.fp()) {
		t.Fatal("connected despite the changed key")
	}
	c.TrustServerKey("SHA256:something-else") // a stale answer does nothing
	time.Sleep(50 * time.Millisecond)
	if c.View().ServerKeyChange == nil {
		t.Fatal("stale answer accepted")
	}
	c.TrustServerKey(newFP)
	ui.wait(t, "ready", readyMain)
	if got := e.config().ServerHostKey; got != identity.KeyLine(e.server.HostKey(), "") {
		t.Fatalf("pinned %q", got)
	}
}
