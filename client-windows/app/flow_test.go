package app

import (
	"context"
	"io"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/helpbuoy/client-windows/config"
	"github.com/mevdschee/helpbuoy/client-windows/identity"
	"github.com/mevdschee/helpbuoy/client-windows/internal/fakerelay"
	"github.com/mevdschee/helpbuoy/client-windows/update"
)

func startRelay(t *testing.T) *fakerelay.Relay {
	t.Helper()
	r, err := fakerelay.Start("", fakerelay.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// registeredWithAgent is registered, with a pinned agent "alice".
func registeredWithAgent(t *testing.T, e *env) {
	t.Helper()
	registered(t, e)
	cfg := e.config()
	cfg.KnownAgents = map[string]string{"alice": "ssh-ed25519 AAAA alice"}
	config.Store{Dir: e.dir}.Save(cfg)
}

func readyMain(v View) bool {
	return v.Screen == ScreenMain && v.Ready
}

func waitOffline(t *testing.T, r *fakerelay.Relay, fp string) {
	t.Helper()
	for i := 0; i < 200 && r.Online(fp); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if r.Online(fp) {
		t.Fatal("still connected to the old relay")
	}
}

// startAndConnect starts the controller and checks that it connects to the
// stored address by itself, with the address field locked meanwhile.
func startAndConnect(t *testing.T, c *Controller, ui *fakeUI, want string) {
	t.Helper()
	c.Start()
	v := ui.wait(t, "auto-connect", func(v View) bool { return v.Connecting || v.Screen != ScreenAddress })
	if v.Screen == ScreenAddress && v.Address != want {
		t.Fatalf("connecting to %q, want %q", v.Address, want)
	}
	c.Connect("other.example.com") // ignored while connecting
	if c.View().Screen == ScreenAddress && c.View().Address != want {
		t.Fatal("address changed while connecting")
	}
}

// TestOtherRelay: Disconnect, then Connect to another relay. Nothing about
// the old relay is forgotten until this PC is registered on the new one.
func TestOtherRelay(t *testing.T) {
	e := newEnv(t)
	registeredWithAgent(t, e)
	relay2 := startRelay(t)
	c, ui := e.controller()
	startAndConnect(t, c, ui, e.relay.Addr())
	ui.wait(t, "ready", readyMain)

	c.Disconnect()
	ui.wait(t, "address screen", screenIs(ScreenAddress))
	waitOffline(t, e.relay, e.fp())
	c.Connect(relay2.Addr())
	v := ui.wait(t, "key screen", screenIs(ScreenKey))
	if v.RelayFingerprint != ssh.FingerprintSHA256(relay2.HostKey()) || v.KeyLine != e.key.Line {
		t.Fatalf("key view %+v", v)
	}
	if cfg := e.config(); cfg.RelayAddress != e.relay.Addr() || cfg.KnownAgents["alice"] == "" || cfg.LastAddress != relay2.Addr() {
		t.Fatalf("config changed before registration %+v", cfg)
	}

	relay2.Register(e.fp())
	ui.wait(t, "main", readyMain)
	cfg := e.config()
	if cfg.RelayAddress != relay2.Addr() || cfg.RelayHostKey != identity.KeyLine(relay2.HostKey(), "") ||
		len(cfg.KnownAgents) != 0 {
		t.Fatalf("config %+v", cfg)
	}

	// After a restart the new relay is prefilled and pinned.
	c.Close()
	c2, ui2 := e.controller()
	startAndConnect(t, c2, ui2, relay2.Addr())
	ui2.wait(t, "main after restart", readyMain)
}

func TestDisconnect(t *testing.T) {
	e := newEnv(t)
	registered(t, e)
	c, ui := e.controller()
	startAndConnect(t, c, ui, e.relay.Addr())
	ui.wait(t, "ready", readyMain)

	// End session ends only the session; the client keeps waiting.
	connectAgent(t, e.relay, e.fp())
	first := <-e.relay.Sessions()
	ui.wait(t, "in session", func(v View) bool { return v.InSession })
	c.EndSession()
	if _, err := io.ReadAll(first.Channel); err != nil {
		t.Fatalf("session channel: %v", err)
	}
	ui.wait(t, "ready again", readyMain)
	if !e.relay.Online(e.fp()) {
		t.Fatal("End session disconnected")
	}

	// During a session Disconnect ends the session too.
	connectAgent(t, e.relay, e.fp())
	sess := <-e.relay.Sessions()
	ui.wait(t, "in session", func(v View) bool { return v.InSession })
	c.Disconnect()
	v := ui.wait(t, "address screen", screenIs(ScreenAddress))
	if _, err := io.ReadAll(sess.Channel); err != nil {
		t.Fatalf("session channel: %v", err)
	}
	waitOffline(t, e.relay, e.fp())

	// When ready, back to the address screen with the address kept.
	c.Connect(e.relay.Addr())
	ui.wait(t, "ready", readyMain)
	c.Disconnect()
	v = ui.wait(t, "address screen", screenIs(ScreenAddress))
	if v.Address != e.relay.Addr() || v.Error != "" || v.Connecting {
		t.Fatalf("%+v", v)
	}
	waitOffline(t, e.relay, e.fp())

	// From the key screen as well.
	relay2 := startRelay(t)
	c.Connect(relay2.Addr())
	ui.wait(t, "key screen", screenIs(ScreenKey))
	c.Disconnect()
	v = ui.wait(t, "address screen", screenIs(ScreenAddress))
	if v.Address != relay2.Addr() {
		t.Fatalf("%+v", v)
	}
	waitOffline(t, relay2, e.fp())
}

func TestUpdateOfferedWhenIdle(t *testing.T) {
	e := newEnv(t)
	registered(t, e)
	release := &update.Release{Version: "9.9.9", MsiURL: "https://x/a.msi"}
	checked := make(chan struct{}, 1)
	c, ui := e.controller()
	c.d.CheckUpdate = func(context.Context) (*update.Release, error) {
		checked <- struct{}{}
		return release, nil
	}
	c.Start() // connects to the stored relay by itself
	ui.wait(t, "offer", func(v View) bool { return v.Update == release && readyMain(v) })

	gate := make(chan struct{})
	ui.answer(true, gate)
	connectAgent(t, e.relay, e.fp())
	v := ui.wait(t, "asking", func(v View) bool { return v.Asking })
	if v.Update != nil {
		t.Fatal("offered while an agent connects")
	}
	close(gate)
	ui.wait(t, "offer again", func(v View) bool { return v.Update == release && readyMain(v) })
	c.DismissUpdate()
	ui.wait(t, "dismissed", func(v View) bool { return v.Update == nil })
	if len(checked) != 1 {
		t.Fatal("not checked once")
	}
}
