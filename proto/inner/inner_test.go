package inner

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type serveResult struct {
	s   *ServerSession
	err error
}

type dialResult struct {
	c   *ClientSession
	err error
}

// setup runs Serve and Dial over a buffered net.Pipe pair with the given keys.
func setup(t *testing.T, hostKey, helperKey ssh.Signer, wantHelper, wantClient ssh.PublicKey) (serveResult, dialResult) {
	t.Helper()
	a, b := pipe()
	sch := make(chan serveResult, 1)
	go func() {
		s, err := Serve(a, hostKey, wantHelper)
		sch <- serveResult{s, err}
	}()
	c, err := Dial(b, helperKey, wantClient)
	dr := dialResult{c, err}
	var sr serveResult
	select {
	case sr = <-sch:
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return")
	}
	t.Cleanup(func() {
		if sr.s != nil {
			sr.s.Close()
		}
		if dr.c != nil {
			dr.c.Close()
		}
		a.Close()
		b.Close()
	})
	return sr, dr
}

func roundTrip(t *testing.T, name string, w io.Writer, r io.Reader) {
	t.Helper()
	msg := []byte("hello over " + name)
	errc := make(chan error, 1)
	go func() {
		_, err := w.Write(msg)
		errc <- err
	}()
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(r, got); err != nil {
		t.Fatalf("%s: read: %v", name, err)
	}
	if err := <-errc; err != nil {
		t.Fatalf("%s: write: %v", name, err)
	}
	if string(got) != string(msg) {
		t.Fatalf("%s: got %q, want %q", name, got, msg)
	}
}

func TestHandshakeAndData(t *testing.T) {
	host, helper := newSigner(t), newSigner(t)
	sr, dr := setup(t, host, helper, helper.PublicKey(), host.PublicKey())
	if sr.err != nil || dr.err != nil {
		t.Fatalf("serve: %v, dial: %v", sr.err, dr.err)
	}
	s, c := sr.s, dr.c

	roundTrip(t, "screen client->helper", s.Screen(), c.Screen())
	roundTrip(t, "screen helper->client", c.Screen(), s.Screen())
	roundTrip(t, "input helper->client", c.Input(), s.Input())
	roundTrip(t, "input client->helper", s.Input(), c.Input())

	// CloseWrite gives EOF on the other side, the reverse direction still works.
	if err := c.Input().CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Input().Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatalf("after CloseWrite: n=%d err=%v, want EOF", n, err)
	}
	roundTrip(t, "input after CloseWrite", s.Input(), c.Input())

	deadline := time.Now().Add(5 * time.Second)
	for c.RTT() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no keepalive RTT measured")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDialWrongClientKey(t *testing.T) {
	host, helper, other := newSigner(t), newSigner(t), newSigner(t)
	sr, dr := setup(t, host, helper, helper.PublicKey(), other.PublicKey())
	if !errors.Is(dr.err, ErrWrongKey) {
		t.Fatalf("dial err = %v, want ErrWrongKey", dr.err)
	}
	if sr.err == nil {
		t.Fatal("serve succeeded with a client that refused it")
	}
}

func TestServeRejectsOtherHelper(t *testing.T) {
	host, helper, other := newSigner(t), newSigner(t), newSigner(t)
	sr, dr := setup(t, host, other, helper.PublicKey(), host.PublicKey())
	if !errors.Is(sr.err, ErrWrongKey) {
		t.Fatalf("serve err = %v, want ErrWrongKey", sr.err)
	}
	if dr.err == nil {
		t.Fatal("dial succeeded with an unknown helper key")
	}
}

func TestExtraChannelsRejected(t *testing.T) {
	host, helper := newSigner(t), newSigner(t)
	a, b := pipe()
	defer a.Close()
	defer b.Close()
	sch := make(chan serveResult, 1)
	go func() {
		s, err := Serve(a, host, helper.PublicKey())
		sch <- serveResult{s, err}
	}()
	cc, chans, reqs, err := ssh.NewClientConn(b, "inner", &ssh.ClientConfig{
		User:            "helpbuoy",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(helper)},
		HostKeyCallback: ssh.FixedHostKey(host.PublicKey()),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	go ssh.DiscardRequests(reqs)
	go func() {
		for nc := range chans {
			nc.Reject(ssh.Prohibited, "")
		}
	}()

	mustReject := func(typ string) {
		t.Helper()
		ch, _, err := cc.OpenChannel(typ, nil)
		if err == nil {
			ch.Close()
			t.Fatalf("channel %q was accepted", typ)
		}
		var oce *ssh.OpenChannelError
		if !errors.As(err, &oce) {
			t.Fatalf("channel %q: unexpected error %v", typ, err)
		}
	}
	mustReject("session")
	mustReject("direct-tcpip")
	mustReject("x11")

	scr, _, err := cc.OpenChannel(ChannelScreen, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Channel requests are rejected.
	if ok, err := scr.SendRequest("shell", true, nil); ok || err != nil {
		t.Fatalf("channel request: ok=%v err=%v", ok, err)
	}
	mustReject(ChannelScreen)
	if _, _, err := cc.OpenChannel(ChannelInput, nil); err != nil {
		t.Fatal(err)
	}
	sr := <-sch
	if sr.err != nil {
		t.Fatal(sr.err)
	}
	defer sr.s.Close()
	mustReject(ChannelScreen)
	mustReject(ChannelInput)
	mustReject("session")

	// Global requests: only keepalive is answered positively.
	if ok, _, err := cc.SendRequest(keepaliveRequest, true, nil); !ok || err != nil {
		t.Fatalf("keepalive: ok=%v err=%v", ok, err)
	}
	if ok, _, err := cc.SendRequest("tcpip-forward", true, nil); ok || err != nil {
		t.Fatalf("tcpip-forward: ok=%v err=%v", ok, err)
	}
}

func waitDone(t *testing.T, name string, wait func() error) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: Wait did not return", name)
	}
}

func TestCloseEndsWait(t *testing.T) {
	host, helper := newSigner(t), newSigner(t)

	sr, dr := setup(t, host, helper, helper.PublicKey(), host.PublicKey())
	if sr.err != nil || dr.err != nil {
		t.Fatalf("serve: %v, dial: %v", sr.err, dr.err)
	}
	dr.c.Close()
	waitDone(t, "server", sr.s.Wait)
	waitDone(t, "client", dr.c.Wait)

	sr, dr = setup(t, host, helper, helper.PublicKey(), host.PublicKey())
	if sr.err != nil || dr.err != nil {
		t.Fatalf("serve: %v, dial: %v", sr.err, dr.err)
	}
	sr.s.Close()
	waitDone(t, "client", dr.c.Wait)
	waitDone(t, "server", sr.s.Wait)
}

func TestServeTimeout(t *testing.T) {
	old := setupTimeout
	setupTimeout = 200 * time.Millisecond
	defer func() { setupTimeout = old }()

	host, helper := newSigner(t), newSigner(t)
	a, b := pipe()
	defer b.Close()
	// Drain whatever the server writes, never answer.
	go io.Copy(io.Discard, b)
	_, err := Serve(a, host, helper.PublicKey())
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

func TestKeepaliveClosesDeadPeer(t *testing.T) {
	oldI, oldM := keepaliveInterval, keepaliveMaxMiss
	keepaliveInterval, keepaliveMaxMiss = 20*time.Millisecond, 3
	defer func() { keepaliveInterval, keepaliveMaxMiss = oldI, oldM }()

	host, helper := newSigner(t), newSigner(t)
	a, b := pipe()
	stall := &stallConn{Conn: a, stall: make(chan struct{}), release: make(chan struct{})}
	defer close(stall.release)
	sch := make(chan serveResult, 1)
	go func() {
		s, err := Serve(stall, host, helper.PublicKey())
		sch <- serveResult{s, err}
	}()
	c, err := Dial(b, helper, host.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	sr := <-sch
	if sr.err != nil {
		t.Fatal(sr.err)
	}
	// The server stops reading; keepalives go unanswered.
	close(stall.stall)
	waitDone(t, "client", c.Wait)
	a.Close()
	b.Close()
}

// stallConn blocks all reads once stall is closed.
type stallConn struct {
	net.Conn
	stall   chan struct{}
	release chan struct{}
}

func (s *stallConn) Read(p []byte) (int, error) {
	select {
	case <-s.stall:
		<-s.release
		return 0, io.EOF
	default:
	}
	return s.Conn.Read(p)
}

// pipe returns a net.Pipe pair whose writes are buffered, like an SSH channel
// with a large window. A bare net.Pipe deadlocks the SSH version exchange,
// where both sides write before they read.
func pipe() (*bufConn, *bufConn) {
	a, b := net.Pipe()
	return newBufConn(a), newBufConn(b)
}

type bufConn struct {
	net.Conn
	q      chan []byte
	closed chan struct{}
	once   chan struct{}
}

func newBufConn(c net.Conn) *bufConn {
	bc := &bufConn{Conn: c, q: make(chan []byte, 4096), closed: make(chan struct{}), once: make(chan struct{}, 1)}
	bc.once <- struct{}{}
	go func() {
		for {
			select {
			case p := <-bc.q:
				if _, err := c.Write(p); err != nil {
					return
				}
			case <-bc.closed:
				return
			}
		}
	}()
	return bc
}

func (bc *bufConn) Write(p []byte) (int, error) {
	select {
	case <-bc.closed:
		return 0, io.ErrClosedPipe
	case bc.q <- append([]byte(nil), p...):
		return len(p), nil
	}
}

func (bc *bufConn) Close() error {
	select {
	case <-bc.once:
		close(bc.closed)
	default:
	}
	return bc.Conn.Close()
}
