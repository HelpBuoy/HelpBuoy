package app

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/help-me-win/client-windows/config"
	"github.com/mevdschee/help-me-win/client-windows/identity"
	"github.com/mevdschee/help-me-win/client-windows/internal/fakerelay"
	"github.com/mevdschee/help-me-win/client-windows/update"
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

// registeredWithHelper is registered, with a pinned helper "alice".
func registeredWithHelper(t *testing.T, e *env) {
	t.Helper()
	registered(t, e)
	cfg := e.config()
	cfg.KnownHelpers = map[string]string{"alice": "ssh-ed25519 AAAA alice"}
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

func TestChangeRelayRegistered(t *testing.T) {
	e := newEnv(t)
	registeredWithHelper(t, e)
	relay2 := startRelay(t)
	relay2.Register(e.fp(), "anna@PC")
	c, ui := e.controller()
	c.Start()
	v := ui.wait(t, "ready", readyMain)
	if v.Relay.Blocked != "" {
		t.Fatalf("blocked: %+v", v.Relay)
	}

	c.ChangeRelay(relay2.Addr())
	ui.wait(t, "connecting", func(v View) bool { return v.Relay.Connecting })
	v = ui.wait(t, "done", func(v View) bool { return v.Relay.Done != "" && readyMain(v) })
	fp2 := ssh.FingerprintSHA256(relay2.HostKey())
	if !strings.Contains(v.Relay.Done, fp2) || !strings.Contains(v.Relay.Done, relay2.Addr()) {
		t.Fatalf("done %q", v.Relay.Done)
	}
	cfg := e.config()
	if cfg.RelayAddress != relay2.Addr() || cfg.RelayHostKey != identity.KeyLine(relay2.HostKey(), "") ||
		len(cfg.KnownHelpers) != 0 || cfg.Name != "anna@PC" {
		t.Fatalf("config %+v", cfg)
	}
	if in := c.Info(); in.RelayAddress != relay2.Addr() || in.RelayFingerprint != fp2 || in.KeyFingerprint != e.fp() {
		t.Fatalf("info %+v", in)
	}
	waitOffline(t, e.relay, e.fp())

	// The new relay works: a helper there can connect.
	connectHelper(t, relay2, e.fp())
	<-relay2.Sessions()
	ui.wait(t, "in session", func(v View) bool { return v.InSession })
}

func TestChangeRelayPending(t *testing.T) {
	e := newEnv(t)
	registeredWithHelper(t, e)
	relay2 := startRelay(t)
	c, ui := e.controller()
	c.Start()
	ui.wait(t, "ready", readyMain)

	c.ChangeRelay(relay2.Addr())
	v := ui.wait(t, "key screen", func(v View) bool { return v.Screen == ScreenKey && v.Relay.Done != "" })
	if v.RelayFingerprint != ssh.FingerprintSHA256(relay2.HostKey()) || v.KeyLine != e.key.Line ||
		!strings.Contains(v.Notice, "this relay") || v.CanChangeAddress {
		t.Fatalf("key view %+v", v)
	}
	if v.Relay.Blocked != "" {
		t.Fatalf("a pending client can change the relay again: %+v", v.Relay)
	}
	cfg := e.config()
	if cfg.RelayAddress != relay2.Addr() || cfg.RelayHostKey != identity.KeyLine(relay2.HostKey(), "") ||
		len(cfg.KnownHelpers) != 0 {
		t.Fatalf("config %+v", cfg)
	}
	waitOffline(t, e.relay, e.fp())

	relay2.Register(e.fp(), "anna@PC")
	ui.wait(t, "main", readyMain)

	// After a restart the client uses the new relay with its pinned key.
	c.Close()
	c2, ui2 := e.controller()
	c2.Start()
	ui2.wait(t, "main after restart", readyMain)
	if !relay2.Online(e.fp()) {
		t.Fatal("not on the new relay")
	}
}

func TestChangeRelayFailureKeepsOldRelay(t *testing.T) {
	e := newEnv(t)
	registeredWithHelper(t, e)
	before := e.config()
	c, ui := e.controller()
	c.Start()
	ui.wait(t, "ready", readyMain)

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	refused := ln.Addr().String()
	ln.Close()

	// Something that is not a relay: accepts and closes.
	notRelay, _ := net.Listen("tcp", "127.0.0.1:0")
	defer notRelay.Close()
	go func() {
		for {
			nc, err := notRelay.Accept()
			if err != nil {
				return
			}
			nc.Write([]byte("HTTP/1.0 400 Bad Request\r\n\r\n"))
			nc.Close()
		}
	}()

	for _, tc := range []struct{ addr, want string }{
		{"bad address/path", ""},
		{e.relay.Addr(), "already uses"},
		{refused, "Nothing is answering"},
		{notRelay.Addr().String(), "help-me-win relay"},
	} {
		c.ChangeRelay(tc.addr)
		v := ui.wait(t, "error for "+tc.addr, func(v View) bool { return v.Relay.Error != "" })
		if !strings.Contains(v.Relay.Error, tc.want) || v.Relay.Connecting || v.Screen != ScreenMain {
			t.Fatalf("%s: %+v", tc.addr, v)
		}
		cfg := e.config()
		if cfg.RelayAddress != before.RelayAddress || cfg.RelayHostKey != before.RelayHostKey ||
			cfg.KnownHelpers["alice"] == "" {
			t.Fatalf("%s: config changed %+v", tc.addr, cfg)
		}
		if !e.relay.Online(e.fp()) {
			t.Fatalf("%s: old relay dropped", tc.addr)
		}
	}
	c.EndRelayChange()
	v := ui.wait(t, "cleared", func(v View) bool { return v.Relay.Error == "" })
	if !readyMain(v) {
		t.Fatalf("%+v", v)
	}
	connectHelper(t, e.relay, e.fp())
	<-e.relay.Sessions()
	ui.wait(t, "old relay still works", func(v View) bool { return v.InSession })
}

func TestChangeRelayCancelled(t *testing.T) {
	e := newEnv(t)
	registered(t, e)
	// A listener that never answers keeps the attempt running.
	silent, _ := net.Listen("tcp", "127.0.0.1:0")
	defer silent.Close()
	c, ui := e.controller()
	c.Start()
	ui.wait(t, "ready", readyMain)
	c.ChangeRelay(silent.Addr().String())
	ui.wait(t, "connecting", func(v View) bool { return v.Relay.Connecting })
	c.EndRelayChange()
	v := ui.wait(t, "stopped", func(v View) bool { return !v.Relay.Connecting })
	if v.Relay.Error != "" || !readyMain(v) {
		t.Fatalf("%+v", v)
	}
	time.Sleep(50 * time.Millisecond)
	if v := c.View(); v.Relay != (RelayChange{}) || e.config().RelayAddress != e.relay.Addr() {
		t.Fatalf("%+v", v.Relay)
	}
}

func TestChangeRelayBlocked(t *testing.T) {
	e := newEnv(t)
	registered(t, e)
	relay2 := startRelay(t)
	c, ui := e.controller()
	c.Start()
	ui.wait(t, "ready", readyMain)

	gate := make(chan struct{})
	ui.answer(false, gate)
	connectHelper(t, e.relay, e.fp())
	v := ui.wait(t, "asking", func(v View) bool { return v.Relay.Blocked != "" })
	if !strings.Contains(v.Relay.Blocked, "connecting") {
		t.Fatalf("not blocked while a helper connects: %+v", v.Relay)
	}
	c.ChangeRelay(relay2.Addr()) // ignored
	time.Sleep(50 * time.Millisecond)
	if relay2.Online(e.fp()) || c.View().Relay.Connecting {
		t.Fatal("changed while a helper connects")
	}

	close(gate)
	sess := <-e.relay.Sessions()
	v = ui.wait(t, "in session", func(v View) bool { return v.InSession })
	if !strings.Contains(v.Relay.Blocked, "session") {
		t.Fatalf("not blocked during a session: %+v", v.Relay)
	}
	c.ChangeRelay(relay2.Addr())
	time.Sleep(50 * time.Millisecond)
	if relay2.Online(e.fp()) {
		t.Fatal("changed during a session")
	}
	sess.Channel.Close()
	v = ui.wait(t, "back", readyMain)
	if v.Relay.Blocked != "" {
		t.Fatalf("still blocked: %+v", v.Relay)
	}
}

func TestChangeRelayBlockedOnFirstRun(t *testing.T) {
	e := newEnv(t)
	c, ui := e.controller()
	c.Start()
	v := ui.wait(t, "address", screenIs(ScreenAddress))
	if v.Relay.Blocked == "" {
		t.Fatal("not blocked on the first-run window")
	}
	c.ChangeRelay(e.relay.Addr())
	time.Sleep(50 * time.Millisecond)
	if e.relay.Online(e.fp()) {
		t.Fatal("connected")
	}
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
	c.Start()
	ui.wait(t, "offer", func(v View) bool { return v.Update == release && readyMain(v) })

	gate := make(chan struct{})
	ui.answer(true, gate)
	connectHelper(t, e.relay, e.fp())
	v := ui.wait(t, "asking", func(v View) bool { return v.Screen == ScreenMain && !v.Ready })
	if v.Update != nil {
		t.Fatal("offered while a helper connects")
	}
	close(gate)
	ui.wait(t, "offer again", func(v View) bool { return v.Update == release && readyMain(v) })
	c.DismissUpdate()
	ui.wait(t, "dismissed", func(v View) bool { return v.Update == nil })
	if len(checked) != 1 {
		t.Fatal("not checked once")
	}
}

func TestUpdateCheckSetting(t *testing.T) {
	e := newEnv(t)
	registered(t, e)
	c, ui := e.controller()
	called := make(chan struct{}, 1)
	c.d.CheckUpdate = func(context.Context) (*update.Release, error) {
		called <- struct{}{}
		return nil, nil
	}
	c.Start()
	ui.wait(t, "ready", readyMain)
	if !c.Info().CheckUpdates {
		t.Fatal("off by default")
	}
	<-called
	c.SetCheckUpdates(false)
	if !e.config().NoUpdateCheck || c.Info().CheckUpdates {
		t.Fatal("not stored")
	}
	if err := c.Reset(); err != nil {
		t.Fatal(err)
	}
	if cfg := e.config(); !cfg.NoUpdateCheck || cfg.Registered() {
		t.Fatalf("after reset %+v", cfg)
	}
	c.Close()

	c2, ui2 := e.controller()
	c2.d.CheckUpdate = c.d.CheckUpdate
	c2.Start()
	ui2.wait(t, "address", screenIs(ScreenAddress))
	select {
	case <-called:
		t.Fatal("checked although turned off")
	case <-time.After(50 * time.Millisecond):
	}
}
