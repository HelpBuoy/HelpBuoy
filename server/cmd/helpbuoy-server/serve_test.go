package main

import (
	"bytes"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/proto/transport"
	"github.com/helpbuoy/helpbuoy/server/fake"
	"github.com/helpbuoy/helpbuoy/server/front"
	"github.com/helpbuoy/helpbuoy/server/store"
)

func TestParseServe(t *testing.T) {
	t.Setenv("HELPBUOY_SERVER_VHOSTS", "")
	state := t.TempDir()
	ok := []struct {
		args   []string
		listen string
		mode   string
	}{
		{[]string{"--state", state}, "127.0.0.1:5959", "http"},
		{[]string{"--state", state, "--mode", "https"}, ":443", "https"},
		{[]string{"--state", state, "--mode", "https", "--listen", ":8443"}, ":8443", "https"},
		{[]string{"--state", state, "--mode", "https", "--cert", "c.pem", "--key", "k.pem"}, ":443", "https"},
		{[]string{"--state", state, "--mode", "https", "--acme", "--acme-host", "help.example.com"}, ":443", "https"},
		{[]string{"--state", state, "--mode", "ssh"}, ":5959", "ssh"},
		{[]string{"--vhosts", state, "--mode", "https", "--acme"}, ":443", "https"},
		{[]string{"--vhosts", state}, "127.0.0.1:5959", "http"},
	}
	for _, tt := range ok {
		cfg, err := parseServe(tt.args, io.Discard)
		if err != nil {
			t.Errorf("%q: %v", tt.args, err)
			continue
		}
		if cfg.listen != tt.listen || cfg.mode != tt.mode {
			t.Errorf("%q: listen %q mode %q", tt.args, cfg.listen, cfg.mode)
		}
	}
	for _, args := range [][]string{
		{"--state", state, "--mode", "tls"},
		{"--state", state, "--cert", "c.pem", "--key", "k.pem"}, // needs --mode https
		{"--state", state, "--mode", "ssh", "--acme", "--acme-host", "a.example.com"},
		{"--state", state, "--mode", "https", "--cert", "c.pem"},
		{"--state", state, "--mode", "https", "--acme"}, // no host names
		{"--state", state, "--mode", "https", "--acme", "--acme-host", "a.example.com", "--cert", "c", "--key", "k"},
		{"--state", state, "--vhosts", state},
		{"--vhosts", state, "--mode", "https", "--acme", "--acme-host", "a.example.com"},
		{"--vhosts", state, "--mode", "ssh"}, // plain SSH can't name the host
		{"--state", state, "--trusted-proxy", "proxy.example.com"},
	} {
		if _, err := parseServe(args, io.Discard); err == nil {
			t.Errorf("%q: no error", args)
		}
	}
	cfg, _ := parseServe([]string{"--state", state, "--trusted-proxy", ""}, io.Discard)
	if len(cfg.trusted) != 0 {
		t.Errorf("empty --trusted-proxy trusts %v", cfg.trusted)
	}
}

func TestValidVhost(t *testing.T) {
	for _, name := range []string{"acme.relay.example.com", "localhost", "a-b.example"} {
		if err := validVhost(name); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
	for _, name := range []string{"", ".tls", "Acme.example.com", "acme.example.com.", "192.0.2.1", "a:1", "a b", "acme.example.com:443"} {
		if validVhost(name) == nil {
			t.Errorf("%q accepted", name)
		}
	}
}

func TestVhostCommands(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HELPBUOY_SERVER_VHOSTS", base)
	out := runOK(t, "vhost", "add", "Acme.Relay.Example.com")
	if !strings.Contains(out, "added acme.relay.example.com, host key SHA256:") {
		t.Errorf("add: %q", out)
	}
	runFail(t, "vhost", "add", "acme.relay.example.com") // exists
	runFail(t, "vhost", "add", "not a host")
	runOK(t, "vhost", "add", "beta.relay.example.com")

	line, _ := keyLine(t, "alice@desk")
	runOK(t, "techie", "add", "alice", line, "--vhost", "acme.relay.example.com")
	runOK(t, "info", "--vhost", "acme.relay.example.com", "--company", "Acme", "--phone", "+1 234")
	if out := runOK(t, "techie", "list", "--vhost", "acme.relay.example.com"); !strings.Contains(out, "alice") {
		t.Errorf("acme techies: %q", out)
	}
	if out := runOK(t, "techie", "list", "--vhost", "beta.relay.example.com"); strings.Contains(out, "alice") {
		t.Errorf("beta techies: %q", out)
	}
	runFail(t, "techie", "list", "--vhost", "gamma.relay.example.com") // never made by a typo
	if _, err := os.Stat(filepath.Join(base, "gamma.relay.example.com")); err == nil {
		t.Error("a typo made a virtual host")
	}
	runFail(t, "techie", "list", "--vhosts", base) // which one?

	out = runOK(t, "vhost", "list")
	if !strings.Contains(out, "acme.relay.example.com") || !strings.Contains(out, "Acme") || !strings.Contains(out, "beta.relay.example.com") {
		t.Errorf("list: %q", out)
	}
	ents, _ := os.ReadDir(base)
	if len(ents) != 2 {
		t.Errorf("left %d entries in the vhosts directory", len(ents))
	}
}

// TestVhostsEndToEnd runs two virtual hosts behind one listener: each has
// its own host key and techies, a removed directory stops its host, and a
// new directory starts one.
func TestVhostsEndToEnd(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HELPBUOY_SERVER_VHOSTS", base)
	runOK(t, "vhost", "add", "acme.example.com")
	runOK(t, "vhost", "add", "beta.example.com")
	runOK(t, "info", "--vhost", "beta.example.com", "--company", "Beta", "--phone", "+1 234")
	hk := fake.NewKey()
	runOK(t, "techie", "add", "alice", fake.KeyLine(hk.PublicKey(), ""), "--vhost", "acme.example.com")

	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := transport.SelfSigned()
	if err != nil {
		t.Fatal(err)
	}
	ln := tls.NewListener(tcp, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}})
	f := front.New(ln.Addr(), front.DefaultTrustedProxies)
	hs := transport.NewHTTPServer(f)
	go hs.Serve(ln)
	defer hs.Close()
	v := &vhosts{dir: base, front: f, log: slog.New(slog.DiscardHandler), running: map[string]*vhost{}, done: make(chan struct{})}
	v.sync()
	defer v.closeAll()

	dial := func(host string) (*fake.Conn, error) {
		return fake.Dial(host+":5959", hk, fake.Options{Timeout: 5 * time.Second, Via: ln.Addr().String()})
	}
	c, err := dial("acme.example.com")
	if err != nil {
		t.Fatal(err)
	}
	h, err := fake.WaitFor[control.Hello](c, 5*time.Second, nil)
	if err != nil || h.Role != control.RoleTechie {
		t.Fatalf("acme: hello %+v, %v", h, err)
	}
	acmeKey := c.HostKey
	c.Close()

	c, err = dial("beta.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(c.HostKey.Marshal(), acmeKey.Marshal()) {
		t.Error("virtual hosts share a host key")
	}
	h, err = fake.WaitFor[control.Hello](c, 5*time.Second, nil)
	if err != nil || h.Role != control.RolePending {
		t.Fatalf("beta: hello %+v, %v (alice is no techie there)", h, err)
	}
	c.Close()

	if _, err := dial("other.example.com"); err == nil {
		t.Error("unknown host accepted")
	}

	os.RemoveAll(filepath.Join(base, "beta.example.com"))
	v.sync()
	if _, err := dial("beta.example.com"); err == nil {
		t.Error("removed host still accepted")
	}
	st, _ := store.Open(filepath.Join(base, "acme.example.com"))
	if idx, _, err := st.Load(); err != nil || len(idx.State.Techies) != 1 {
		t.Errorf("acme state: %v", err)
	}
}

// TestSSHMode: a server in ssh mode takes plain SSH, and the apps get there
// after their HTTPS attempt fails.
func TestSSHMode(t *testing.T) {
	dir := t.TempDir()
	hk := fake.NewKey()
	runOK(t, "techie", "add", "alice", fake.KeyLine(hk.PublicKey(), ""), "--state", dir)
	srv, lf, err := startServer(dir, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()
	defer srv.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)

	c, err := fake.Dial(ln.Addr().String(), hk, fake.Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if h, err := fake.WaitFor[control.Hello](c, 5*time.Second, nil); err != nil || h.Role != control.RoleTechie {
		t.Fatalf("hello %+v, %v", h, err)
	}
}
