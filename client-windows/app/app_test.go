package app

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/help-me-win/client-windows/config"
	"github.com/mevdschee/help-me-win/client-windows/identity"
	"github.com/mevdschee/help-me-win/client-windows/internal/fakerelay"
	"github.com/mevdschee/help-me-win/client-windows/session"
	"github.com/mevdschee/help-me-win/proto/appkey"
)

type fakeUI struct {
	views   chan View
	confirm bool
}

func (u *fakeUI) Render(v View) { u.views <- v }
func (u *fakeUI) ConfirmHelperKeyChange(string, string, string) bool {
	return u.confirm
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
	relay   *fakerelay.Relay
}

func newEnv(t *testing.T) *env {
	k, err := identity.Ephemeral("anna@PC")
	if err != nil {
		t.Fatal(err)
	}
	relay, err := fakerelay.Start("", fakerelay.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { relay.Close() })
	return &env{t: t, dir: t.TempDir(), key: k, relay: relay}
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

	c.Connect(e.relay.Addr())
	v = ui.wait(t, "key screen", screenIs(ScreenKey))
	if v.KeyLine != e.key.Line || v.KeyFingerprint != e.key.Fingerprint ||
		v.RelayFingerprint != ssh.FingerprintSHA256(e.relay.HostKey()) {
		t.Fatalf("key view %+v", v)
	}
	if cfg := e.config(); cfg.LastAddress != e.relay.Addr() || cfg.Registered() {
		t.Fatalf("config while pending %+v", cfg)
	}

	e.relay.Register(e.fp(), "anna@PC")
	v = ui.wait(t, "main screen", func(v View) bool { return v.Screen == ScreenMain && v.ButtonEnabled })
	if v.Name != "anna@PC" || v.Button != ButtonRequest || v.Notice != "" || v.Error != "" {
		t.Fatalf("main view %+v", v)
	}
	cfg := e.config()
	if !cfg.Registered() || cfg.RelayAddress != e.relay.Addr() || cfg.LastAddress != "" ||
		cfg.RelayHostKey != identity.KeyLine(e.relay.HostKey(), "") {
		t.Fatalf("config %+v", cfg)
	}
	in := c.Info()
	if in.RelayAddress != e.relay.Addr() || in.Name != "anna@PC" || in.KeyFingerprint != e.fp() {
		t.Fatalf("info %+v", in)
	}
}

func registered(t *testing.T, e *env) {
	t.Helper()
	e.relay.Register(e.fp(), "anna@PC")
	config.Store{Dir: e.dir}.Save(&config.Config{
		RelayAddress: e.relay.Addr(),
		RelayHostKey: identity.KeyLine(e.relay.HostKey(), ""),
		Name:         "anna@PC",
	})
}

func TestRequestCancelAndSession(t *testing.T) {
	e := newEnv(t)
	registered(t, e)
	c, ui := e.controller()
	c.Start()
	ui.wait(t, "ready", func(v View) bool { return v.Screen == ScreenMain && v.ButtonEnabled })

	c.RequestHelp()
	ui.wait(t, "cancel button", func(v View) bool { return v.Button == ButtonCancel && v.ButtonEnabled })
	c.CancelRequest()
	ui.wait(t, "request button", func(v View) bool { return v.Button == ButtonRequest && v.ButtonEnabled })

	c.RequestHelp()
	ui.wait(t, "cancel button", func(v View) bool { return v.Button == ButtonCancel && v.ButtonEnabled })
	// Wait until the relay has the request open.
	var err error
	helper, _ := identity.Ephemeral("alice")
	for i := 0; i < 100; i++ {
		if err = e.relay.Take(e.fp(), "alice", helper.Line); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	sess := <-e.relay.Sessions()
	v := ui.wait(t, "in session", func(v View) bool { return v.InSession })
	if v.Screen != ScreenMain || v.ButtonEnabled {
		t.Fatalf("session view %+v", v)
	}
	if got := e.config().KnownHelpers["alice"]; got != identity.KeyLine(helper.Public, "") {
		t.Fatalf("pinned %q", got)
	}
	sess.Channel.Close() // the helper ends the session
	ui.wait(t, "back", func(v View) bool { return !v.InSession && v.ButtonEnabled && v.Button == ButtonRequest })
	if in := c.Info(); !strings.HasPrefix(in.LastHelper, "alice (SHA256:") {
		t.Fatalf("last helper %q", in.LastHelper)
	}
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

func TestPrefillAfterCloseBeforeRegistration(t *testing.T) {
	e := newEnv(t)
	c, ui := e.controller()
	c.Start()
	c.Connect(e.relay.Addr())
	ui.wait(t, "key screen", screenIs(ScreenKey))
	c.Close()

	c2, ui2 := e.controller()
	c2.Start()
	v := ui2.wait(t, "address screen", screenIs(ScreenAddress))
	if v.Address != e.relay.Addr() {
		t.Fatalf("prefill %q", v.Address)
	}
}

func TestChangeAddress(t *testing.T) {
	e := newEnv(t)
	c, ui := e.controller()
	c.Start()
	c.Connect(e.relay.Addr())
	ui.wait(t, "key screen", screenIs(ScreenKey))
	c.ChangeAddress()
	v := ui.wait(t, "address screen", screenIs(ScreenAddress))
	if v.Address != e.relay.Addr() || v.Connecting {
		t.Fatalf("%+v", v)
	}
}

func TestResetAndRemoveKey(t *testing.T) {
	e := newEnv(t)
	registered(t, e)
	c, ui := e.controller()
	c.Start()
	ui.wait(t, "main", screenIs(ScreenMain))
	if err := c.Reset(); err != nil {
		t.Fatal(err)
	}
	ui.wait(t, "address screen", screenIs(ScreenAddress))
	if e.config().Registered() || e.loads != 1 {
		t.Fatalf("config kept or key reloaded (%d loads)", e.loads)
	}

	old := e.key.Fingerprint
	if err := c.RemoveKey(); err != nil {
		t.Fatal(err)
	}
	v := ui.wait(t, "address screen", screenIs(ScreenAddress))
	if e.removed != 1 || v.KeyFingerprint == old || v.KeyFingerprint != e.key.Fingerprint {
		t.Fatalf("removed %d, key %s (old %s)", e.removed, v.KeyFingerprint, old)
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
	c.Connect(e.relay.Addr()) // ignored without a key
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
	// A relay that does not know the key (a helper removed it): pin a
	// fresh fake relay on which the key is not registered.
	relay2, err := fakerelay.Start("", fakerelay.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer relay2.Close()
	config.Store{Dir: e.dir}.Save(&config.Config{
		RelayAddress: relay2.Addr(),
		RelayHostKey: identity.KeyLine(relay2.HostKey(), ""),
		Name:         "anna@PC",
	})
	c, ui := e.controller()
	c.Start()
	v := ui.wait(t, "key screen", screenIs(ScreenKey))
	if v.Notice == "" {
		t.Fatal("no notice")
	}
	relay2.Register(e.fp(), "anna@PC")
	ui.wait(t, "main", func(v View) bool { return v.Screen == ScreenMain && v.ButtonEnabled })
}

func TestCapitalize(t *testing.T) {
	if capitalize("relay address is empty") != "Relay address is empty" || capitalize("") != "" {
		t.Fatal()
	}
}
