package app

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/client-windows/identity"
	"github.com/helpbuoy/helpbuoy/client-windows/internal/fakeserver"
	"github.com/helpbuoy/helpbuoy/proto/update"
)

func startServer(t *testing.T) *fakeserver.Server {
	t.Helper()
	r, err := fakeserver.Start("", fakeserver.Options{Info: testInfo})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func readyMain(v View) bool {
	return v.Screen == ScreenMain && v.Ready
}

func waitOffline(t *testing.T, r *fakeserver.Server, fp string) {
	t.Helper()
	for i := 0; i < 200 && r.Online(fp); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if r.Online(fp) {
		t.Fatal("still connected to the old server")
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

// TestOtherServer: Disconnect, then Connect to another server. Nothing about
// the old server is forgotten until this PC is registered on the new one.
func TestOtherServer(t *testing.T) {
	e := newEnv(t)
	registered(t, e)
	server2 := startServer(t)
	c, ui := e.controller()
	startAndConnect(t, c, ui, e.server.Addr())
	ui.wait(t, "ready", readyMain)

	c.Disconnect()
	ui.wait(t, "address screen", screenIs(ScreenAddress))
	waitOffline(t, e.server, e.fp())
	c.Connect(server2.Addr())
	v := ui.wait(t, "key screen", screenIs(ScreenKey))
	if v.ServerFingerprint != ssh.FingerprintSHA256(server2.HostKey()) || v.KeyLine != e.key.Line {
		t.Fatalf("key view %+v", v)
	}
	if cfg := e.config(); cfg.ServerAddress != e.server.Addr() || cfg.LastAddress != server2.Addr() {
		t.Fatalf("config changed before registration %+v", cfg)
	}

	server2.Register(e.fp())
	ui.wait(t, "main", readyMain)
	cfg := e.config()
	if cfg.ServerAddress != server2.Addr() || cfg.ServerHostKey != identity.KeyLine(server2.HostKey(), "") {
		t.Fatalf("config %+v", cfg)
	}

	// After a restart the new server is prefilled and pinned.
	c.Close()
	c2, ui2 := e.controller()
	startAndConnect(t, c2, ui2, server2.Addr())
	ui2.wait(t, "main after restart", readyMain)
}

func TestDisconnect(t *testing.T) {
	e := newEnv(t)
	registered(t, e)
	c, ui := e.controller()
	startAndConnect(t, c, ui, e.server.Addr())
	ui.wait(t, "ready", readyMain)

	// End session ends only the session; the computer keeps waiting.
	connectTechie(t, e.server, e.fp())
	first := <-e.server.Sessions()
	ui.wait(t, "in session", func(v View) bool { return v.InSession })
	c.EndSession()
	if _, err := io.ReadAll(first.Channel); err != nil {
		t.Fatalf("session channel: %v", err)
	}
	ui.wait(t, "ready again", readyMain)
	if !e.server.Online(e.fp()) {
		t.Fatal("End session disconnected")
	}

	// During a session Disconnect ends the session too.
	connectTechie(t, e.server, e.fp())
	sess := <-e.server.Sessions()
	ui.wait(t, "in session", func(v View) bool { return v.InSession })
	c.Disconnect()
	v := ui.wait(t, "address screen", screenIs(ScreenAddress))
	if _, err := io.ReadAll(sess.Channel); err != nil {
		t.Fatalf("session channel: %v", err)
	}
	waitOffline(t, e.server, e.fp())

	// When ready, back to the address screen with the address kept.
	c.Connect(e.server.Addr())
	ui.wait(t, "ready", readyMain)
	c.Disconnect()
	v = ui.wait(t, "address screen", screenIs(ScreenAddress))
	if v.Address != e.server.Addr() || v.Error != "" || v.Connecting {
		t.Fatalf("%+v", v)
	}
	waitOffline(t, e.server, e.fp())

	// From the key screen as well.
	server2 := startServer(t)
	c.Connect(server2.Addr())
	ui.wait(t, "key screen", screenIs(ScreenKey))
	c.Disconnect()
	v = ui.wait(t, "address screen", screenIs(ScreenAddress))
	if v.Address != server2.Addr() {
		t.Fatalf("%+v", v)
	}
	waitOffline(t, server2, e.fp())
}

// TestUpdateOnlyOnClick: the start never checks for updates; only
// LatestRelease (clicking update) does, and it returns what it finds.
func TestUpdateOnlyOnClick(t *testing.T) {
	e := newEnv(t)
	registered(t, e)
	release := &update.Release{Version: "9.9.9", MsiURL: "https://x/a.msi"}
	var checks atomic.Int32
	c, ui := e.controller()
	c.d.CheckUpdate = func(context.Context) (*update.Release, error) {
		checks.Add(1)
		return release, nil
	}
	c.Start()
	ui.wait(t, "ready", readyMain)
	if n := checks.Load(); n != 0 {
		t.Fatalf("%d checks at the start", n)
	}
	r, err := c.LatestRelease(context.Background())
	if err != nil || r != release || checks.Load() != 1 {
		t.Fatalf("LatestRelease = %v, %v after %d checks", r, err, checks.Load())
	}

	c.d.CheckUpdate = nil
	if r, err := c.LatestRelease(context.Background()); r != nil || err != nil {
		t.Fatalf("without CheckUpdate: %v, %v", r, err)
	}
}
