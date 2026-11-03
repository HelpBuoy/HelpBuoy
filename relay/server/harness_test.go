package server_test

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/help-me-win/proto/control"
	"github.com/mevdschee/help-me-win/relay/auth"
	"github.com/mevdschee/help-me-win/relay/fake"
	"github.com/mevdschee/help-me-win/relay/limits"
	"github.com/mevdschee/help-me-win/relay/server"
	"github.com/mevdschee/help-me-win/relay/store"
)

const wait = 5 * time.Second

// clock is a fake clock that only moves when told to.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// env is a relay on loopback with a fresh state directory.
type env struct {
	t     *testing.T
	dir   string
	clock *clock
	cfg   server.Config
	srv   *server.Server
	addr  string
	st    *store.Store
}

// looseLimits keeps the abuse limits out of the way of tests that are not
// about them.
func looseLimits() limits.Config {
	return limits.Config{
		KeyInterval:     -1, // the fake clock stands still; reconnects must not be refused
		PendingPerIP:    10000,
		HandshakesPerIP: 1000,
		PendingTotal:    10000,
	}
}

func testLogger() *slog.Logger {
	if os.Getenv("HMW_RELAY_TEST_LOG") != "" {
		return slog.New(slog.NewJSONHandler(os.Stderr, nil))
	}
	return slog.New(slog.DiscardHandler)
}

func newEnv(t *testing.T, mod func(*server.Config)) *env {
	t.Helper()
	e := &env{t: t, dir: t.TempDir(), clock: newClock()}
	e.cfg = server.Config{
		StateDir:       e.dir,
		Version:        "test",
		Log:            testLogger(),
		Now:            e.clock.Now,
		Limits:         looseLimits(),
		ReloadInterval: 20 * time.Millisecond,
		SweepInterval:  10 * time.Millisecond,
		// Presence coalescing is on, but short, so tests see changes quickly.
		PresenceInterval: 5 * time.Millisecond,
	}
	if mod != nil {
		mod(&e.cfg)
	}
	var err error
	if e.st, err = store.Open(e.dir); err != nil {
		t.Fatal(err)
	}
	e.start()
	return e
}

func (e *env) start() {
	e.t.Helper()
	srv, err := server.New(e.cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		e.t.Fatal(err)
	}
	go srv.Serve(ln)
	e.srv, e.addr = srv, ln.Addr().String()
	e.t.Cleanup(func() { srv.Close() })
}

func (e *env) restart() {
	e.t.Helper()
	e.srv.Close()
	e.start()
}

// update changes state.json the way the admin CLI does and waits until the
// running relay has picked it up.
func (e *env) update(fn func(*store.State) error) {
	e.t.Helper()
	if _, _, err := e.st.Update(fn); err != nil {
		e.t.Fatal(err)
	}
	e.srv.Reload()
}

func (e *env) addHelper(name string) ssh.Signer {
	e.t.Helper()
	k := fake.NewKey()
	e.update(func(st *store.State) error {
		return st.AddHelper(name, fake.KeyLine(k.PublicKey(), ""))
	})
	return k
}

func (e *env) addClient(name string) ssh.Signer {
	e.t.Helper()
	k := fake.NewKey()
	e.update(func(st *store.State) error {
		_, _, err := st.AddClient(fake.KeyLine(k.PublicKey(), name), "", "admin", e.clock.Now())
		return err
	})
	return k
}

func (e *env) dial(k ssh.Signer) *fake.Conn {
	e.t.Helper()
	c, err := fake.Dial(e.addr, k, fake.Options{HostKey: e.srv.HostKey()})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { c.Close() })
	return c
}

// connect dials and waits for Hello with the wanted role. Clients and
// pending connections send ClientHello first, as the apps do.
func (e *env) connect(k ssh.Signer, want control.Role) (*fake.Conn, *control.Hello) {
	e.t.Helper()
	c := e.dial(k)
	if want != control.RoleHelper {
		if err := c.Send(&control.ClientHello{AppVersion: "test", UserMachine: "user@machine"}); err != nil {
			e.t.Fatal(err)
		}
	}
	h := mustWait[control.Hello](e.t, c, nil)
	if h.Role != want {
		e.t.Fatalf("role = %s, want %s", h.Role, want)
	}
	return c, h
}

func (e *env) helper(name string) *fake.Conn {
	e.t.Helper()
	c, _ := e.connect(e.addHelper(name), control.RoleHelper)
	return c
}

func (e *env) client(name string) (*fake.Conn, ssh.Signer) {
	e.t.Helper()
	k := e.addClient(name)
	c, _ := e.connect(k, control.RoleClient)
	clientFPs.Store(c, fp(k))
	return c, k
}

func mustWait[T any](t *testing.T, c *fake.Conn, ok func(*T) bool) *T {
	t.Helper()
	v, err := fake.WaitFor(c, wait, ok)
	if err != nil {
		var zero T
		t.Fatalf("waiting for %T: %v", &zero, err)
	}
	return v
}

// waitClosed drains c until the relay closes it and returns the Error
// message it sent, if any.
func waitClosed(t *testing.T, c *fake.Conn) string {
	t.Helper()
	msg := ""
	deadline := time.Now().Add(wait)
	for {
		m, err := c.Next(time.Until(deadline))
		if errors.Is(err, fake.ErrClosed) {
			return msg
		}
		if err != nil {
			t.Fatalf("connection not closed: %v", err)
		}
		if e, ok := m.(*control.Error); ok {
			msg = e.Message
		}
	}
}

// assertOpen checks that c stays open for a short while.
func assertOpen(t *testing.T, c *fake.Conn) {
	t.Helper()
	if _, _, err := c.Client.SendRequest(server.KeepaliveRequest, true, nil); err != nil {
		t.Fatalf("connection closed: %v", err)
	}
}

func fp(k ssh.Signer) string { return auth.Fingerprint(k.PublicKey()) }

func keyLine(k ssh.Signer, comment string) string { return fake.KeyLine(k.PublicKey(), comment) }

// presenceWhere waits for a Presence that satisfies ok.
func presenceWhere(t *testing.T, h *fake.Conn, ok func(*control.Presence) bool) *control.Presence {
	t.Helper()
	return mustWait(t, h, ok)
}

// clientInfo returns the client with the fingerprint, or an empty entry.
func clientInfo(p *control.Presence, fingerprint string) *control.ClientInfo {
	for i := range p.Clients {
		if p.Clients[i].Fingerprint == fingerprint {
			return &p.Clients[i]
		}
	}
	return &control.ClientInfo{}
}

func openReason(err error) ssh.RejectionReason {
	var oe *ssh.OpenChannelError
	if errors.As(err, &oe) {
		return oe.Reason
	}
	return 0
}

func readAll(t *testing.T, r io.Reader, n int) string {
	t.Helper()
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(buf)
}

var uniqN atomic.Int64

func uniq(prefix string) string { return fmt.Sprintf("%s%d", prefix, uniqN.Add(1)) }

func writeFile(p, s string) error { return os.WriteFile(p, []byte(s), 0o600) }

// proxy forwards one TCP connection and can freeze it, so the peer looks
// dead to the relay while the socket stays open.
type proxy struct {
	ln     net.Listener
	frozen atomic.Bool
	done   chan struct{}
}

func newProxy(t *testing.T, target string) *proxy {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &proxy{ln: ln, done: make(chan struct{})}
	t.Cleanup(func() { close(p.done); ln.Close() })
	go func() {
		for {
			a, err := ln.Accept()
			if err != nil {
				return
			}
			b, err := net.Dial("tcp", target)
			if err != nil {
				a.Close()
				return
			}
			go p.pipe(a, b)
			go p.pipe(b, a)
		}
	}()
	return p
}

func (p *proxy) addr() string { return p.ln.Addr().String() }

func (p *proxy) freeze() { p.frozen.Store(true) }

func (p *proxy) pipe(dst, src net.Conn) {
	defer dst.Close()
	defer src.Close()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if p.frozen.Load() {
			<-p.done
			return
		}
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
