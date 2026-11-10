package serverconn

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/desk/config"
	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/proto/transport"
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

// fakeServer is a minimal helpbuoy-server: it knows one techie key, sends Hello and
// Presence, answers commands, and opens a session channel after Connect.
type fakeServer struct {
	t        *testing.T
	ln       net.Listener
	hostKey  ssh.Signer
	techie   ssh.PublicKey
	presence control.Presence

	mu       sync.Mutex
	received []any
	conns    []ssh.Conn
	ctrls    []*control.Conn
	logins   int
	payload  []byte // written on each session channel
}

func newFakeServer(t *testing.T, techie ssh.PublicKey) *fakeServer {
	ln, err := transport.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &fakeServer{
		t: t, ln: ln, hostKey: newSigner(t), techie: techie,
		payload: []byte("inner ssh bytes"),
		presence: control.Presence{Computers: []control.ComputerInfo{{
			Fingerprint: "SHA256:abc", Name: "anna@ANNA-PC", Online: true,
		}}},
	}
	t.Cleanup(func() { ln.Close(); r.dropAll() })
	go r.serve()
	return r
}

func (r *fakeServer) addr() string { return r.ln.Addr().String() }

func (r *fakeServer) dropAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.conns {
		c.Close()
	}
	r.conns = nil
}

// kickAll sends an Error with reason on every techie connection and closes
// it, like the server's Kick.
func (r *fakeServer) kickAll(reason string) {
	r.mu.Lock()
	ctrls := r.ctrls
	r.ctrls = nil
	r.mu.Unlock()
	for _, cc := range ctrls {
		cc.Send(&control.Error{Message: reason})
	}
	r.dropAll()
}

func (r *fakeServer) got() []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]any(nil), r.received...)
}

func (r *fakeServer) serve() {
	for {
		nc, err := r.ln.Accept()
		if err != nil {
			return
		}
		go r.handle(nc)
	}
}

func (r *fakeServer) handle(nc net.Conn) {
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			role := string(control.RolePending)
			if bytes.Equal(key.Marshal(), r.techie.Marshal()) {
				role = string(control.RoleTechie)
			}
			return &ssh.Permissions{Extensions: map[string]string{"role": role}}, nil
		},
	}
	cfg.AddHostKey(r.hostKey)
	sc, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		nc.Close()
		return
	}
	r.mu.Lock()
	r.conns = append(r.conns, sc)
	r.logins++
	r.mu.Unlock()
	go func() {
		for req := range reqs {
			if req.WantReply {
				req.Reply(req.Type == "keepalive@openssh.com", nil)
			}
		}
	}()
	role := control.Role(sc.Permissions.Extensions["role"])
	for nch := range chans {
		if nch.ChannelType() != control.ChannelControl {
			nch.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, creqs, err := nch.Accept()
		if err != nil {
			return
		}
		go ssh.DiscardRequests(creqs)
		go r.control(sc, ch, role)
	}
}

func (r *fakeServer) control(sc *ssh.ServerConn, ch ssh.Channel, role control.Role) {
	cc := control.NewConn(ch)
	if role == control.RoleTechie {
		// Before Hello, so kickAll reaches every connection that is online.
		r.mu.Lock()
		r.ctrls = append(r.ctrls, cc)
		r.mu.Unlock()
	}
	cc.Send(&control.Hello{Role: role, ServerVersion: "test", ServerTime: time.Now(), Name: "alice"})
	if role != control.RoleTechie {
		return
	}
	cc.Send(&r.presence)
	for {
		m, err := cc.Receive()
		if err != nil {
			return
		}
		r.mu.Lock()
		r.received = append(r.received, m)
		r.mu.Unlock()
		switch m := m.(type) {
		case *control.RegisterComputer:
			cc.Send(&control.Reply{Seq: m.Seq})
		case *control.UnregisterComputer:
			cc.Send(&control.Reply{Seq: m.Seq, Error: "unknown computer"})
		case *control.RenameComputer:
			if m.Fingerprint != "SHA256:a" || m.Name != "Anna laptop" {
				cc.Send(&control.Reply{Seq: m.Seq, Error: "bad rename"})
				continue
			}
			cc.Send(&control.Reply{Seq: m.Seq})
		case *control.SetServerInfo:
			cc.Send(&control.Reply{Seq: m.Seq})
		case *control.DecideListing:
			if m.Computer != "SHA256:a" || m.Address != "other.example.com:5959" || m.Fingerprint != "SHA256:o" || !m.Allow {
				cc.Send(&control.Reply{Seq: m.Seq, Error: "bad decision"})
				continue
			}
			cc.Send(&control.Reply{Seq: m.Seq})
		case *control.Connect:
			if m.Fingerprint != "SHA256:a" {
				cc.Send(&control.Reply{Seq: m.Seq, Error: "computer is not online"})
				continue
			}
			cc.Send(&control.Reply{Seq: m.Seq})
			data := control.MarshalTechiesessionData(control.TechiesessionData{
				RequestID: m.RequestID, ComputerName: "anna@ANNA-PC",
				ComputerKey: "ssh-ed25519 AAAA anna@ANNA-PC", UserMachine: "anna@ANNA-PC",
			})
			go func() {
				sch, sreqs, err := sc.OpenChannel(control.ChannelSession, data)
				if err != nil {
					r.t.Errorf("open session channel: %v", err)
					return
				}
				go ssh.DiscardRequests(sreqs)
				sch.Write(r.payload)
				sch.CloseWrite()
			}()
		}
	}
}

// waitFor reads events until one satisfies f.
func waitFor[T any](t *testing.T, c *Conn, f func(T) bool) T {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-c.Events():
			if v, ok := ev.(T); ok && f(v) {
				return v
			}
		case <-deadline:
			var zero T
			t.Fatalf("timed out waiting for %T", zero)
			return zero
		}
	}
}

func waitStatus(t *testing.T, c *Conn, want Status) StatusEvent {
	t.Helper()
	return waitFor(t, c, func(e StatusEvent) bool { return e.Status == want })
}

func waitResult(t *testing.T, res <-chan error) error {
	t.Helper()
	select {
	case err := <-res:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("no command result")
		return nil
	}
}

func fastOptions(addr string, s ssh.Signer) Options {
	return Options{
		Addr: addr, Signer: s,
		KeepAlive: 50 * time.Millisecond, HandshakeTimeout: 3 * time.Second,
		CommandTimeout: 3 * time.Second, MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond,
	}
}

func TestOnlinePresenceCommandsAndSession(t *testing.T) {
	key := newSigner(t)
	r := newFakeServer(t, key.PublicKey())
	c := Start(fastOptions(r.addr(), key))
	defer c.Close()

	pin := waitFor(t, c, func(HostKeyPinned) bool { return true })
	if !bytes.Equal(pin.Key.Marshal(), r.hostKey.PublicKey().Marshal()) {
		t.Fatal("pinned the wrong host key")
	}
	st := waitStatus(t, c, StatusOnline)
	if st.Hello == nil || st.Hello.Name != "alice" {
		t.Fatalf("hello = %+v", st.Hello)
	}
	p := waitFor(t, c, func(PresenceEvent) bool { return true })
	if len(p.Presence.Computers) != 1 || !p.Presence.Computers[0].Online {
		t.Fatalf("presence = %+v", p.Presence)
	}

	if err := waitResult(t, c.RegisterComputer("ssh-ed25519 AAAA bob@pc", "", "")); err != nil {
		t.Errorf("register: %v", err)
	}
	if err := waitResult(t, c.UnregisterComputer("SHA256:x")); err == nil || err.Error() != "unknown computer" {
		t.Errorf("unregister error = %v", err)
	}
	if err := waitResult(t, c.RenameComputer("SHA256:a", "Anna laptop")); err != nil {
		t.Errorf("rename: %v", err)
	}
	info := control.ServerInfo{Company: "Example Inc", Phone: "+1 234 567 890", Description: "Weekdays"}
	if err := waitResult(t, c.SetServerInfo(info)); err != nil {
		t.Errorf("set server info: %v", err)
	}
	req := control.ListingRequest{Address: "other.example.com:5959", Fingerprint: "SHA256:o"}
	if err := waitResult(t, c.DecideListing("SHA256:a", req, true)); err != nil {
		t.Errorf("decide listing: %v", err)
	}
	if err := waitResult(t, c.Connect("SHA256:nope", "r0")); err == nil {
		t.Error("connect to an offline computer succeeded")
	}
	if err := waitResult(t, c.Connect("SHA256:a", "r1")); err != nil {
		t.Fatalf("connect: %v", err)
	}
	sess := waitFor(t, c, func(SessionEvent) bool { return true })
	defer sess.Channel.Close()
	if sess.Data.RequestID != "r1" || sess.Data.ComputerName != "anna@ANNA-PC" {
		t.Errorf("session data = %+v", sess.Data)
	}
	b, err := io.ReadAll(sess.Channel)
	if err != nil || !bytes.Equal(b, r.payload) {
		t.Errorf("session channel read %q, %v", b, err)
	}

	got := r.got()
	seqs := map[uint32]bool{}
	for _, m := range got {
		var s uint32
		switch m := m.(type) {
		case *control.RegisterComputer:
			s = m.Seq
			if m.PublicKey != "ssh-ed25519 AAAA bob@pc" {
				t.Errorf("register key = %q", m.PublicKey)
			}
		case *control.UnregisterComputer:
			s = m.Seq
		case *control.RenameComputer:
			s = m.Seq
		case *control.SetServerInfo:
			s = m.Seq
			if m.Company != "Example Inc" || m.Phone != "+1 234 567 890" || m.Description != "Weekdays" {
				t.Errorf("set server info = %+v", m)
			}
		case *control.DecideListing:
			s = m.Seq
		case *control.Connect:
			s = m.Seq
		}
		if seqs[s] {
			t.Errorf("seq %d reused", s)
		}
		seqs[s] = true
	}
	if len(got) != 7 {
		t.Errorf("server received %d commands", len(got))
	}

	// Keepalives are answered, so an RTT shows up.
	time.Sleep(200 * time.Millisecond)
	if c.RTT() <= 0 {
		t.Error("no keepalive RTT")
	}
}

func TestReconnectAfterDrop(t *testing.T) {
	key := newSigner(t)
	r := newFakeServer(t, key.PublicKey())
	c := Start(fastOptions(r.addr(), key))
	defer c.Close()
	waitStatus(t, c, StatusOnline)
	r.dropAll()
	waitStatus(t, c, StatusConnecting)
	waitStatus(t, c, StatusOnline)
	r.mu.Lock()
	n := r.logins
	r.mu.Unlock()
	if n < 2 {
		t.Errorf("logins = %d", n)
	}
}

// TestReplaced checks that a connection pushed off by another desk app with
// the same key says so and waits for Reconnect instead of pushing back.
func TestReplaced(t *testing.T) {
	key := newSigner(t)
	r := newFakeServer(t, key.PublicKey())
	c := Start(fastOptions(r.addr(), key))
	defer c.Close()
	waitStatus(t, c, StatusOnline)
	r.kickAll(control.ReasonReplaced)
	st := waitStatus(t, c, StatusReplaced)
	if !errors.Is(st.Err, ErrReplaced) {
		t.Errorf("err = %v", st.Err)
	}
	time.Sleep(500 * time.Millisecond) // several backoffs
	r.mu.Lock()
	n := r.logins
	r.mu.Unlock()
	if n != 1 {
		t.Fatalf("logins = %d after being replaced, want 1 (no retry)", n)
	}
	c.Reconnect()
	waitStatus(t, c, StatusOnline)
}

// TestOtherKickRetries checks that other kick reasons still reconnect.
func TestOtherKickRetries(t *testing.T) {
	key := newSigner(t)
	r := newFakeServer(t, key.PublicKey())
	c := Start(fastOptions(r.addr(), key))
	defer c.Close()
	waitStatus(t, c, StatusOnline)
	r.kickAll("bad control message: test")
	waitStatus(t, c, StatusConnecting)
	waitStatus(t, c, StatusOnline)
}

func TestNotTechie(t *testing.T) {
	r := newFakeServer(t, newSigner(t).PublicKey())
	c := Start(fastOptions(r.addr(), newSigner(t)))
	defer c.Close()
	st := waitStatus(t, c, StatusAuthFailed)
	if !errors.Is(st.Err, ErrNotTechie) {
		t.Errorf("err = %v", st.Err)
	}
	if err := waitResult(t, c.Connect("SHA256:a", "r1")); !errors.Is(err, ErrNotConnected) {
		t.Errorf("command while offline: %v", err)
	}
}

func TestIdentityChanged(t *testing.T) {
	key := newSigner(t)
	r := newFakeServer(t, key.PublicKey())
	opts := fastOptions(r.addr(), key)
	opts.HostKey = config.HostKeyLine(newSigner(t).PublicKey())
	c := Start(opts)
	defer c.Close()
	st := waitStatus(t, c, StatusIdentityChanged)
	if !errors.Is(st.Err, config.ErrServerIdentityChanged) {
		t.Errorf("err = %v", st.Err)
	}
	// A hard error: no automatic retries.
	time.Sleep(300 * time.Millisecond)
	r.mu.Lock()
	n := r.logins
	r.mu.Unlock()
	if n != 0 {
		t.Errorf("logged in %d times despite the mismatch", n)
	}
}

func TestPinnedKeyAccepted(t *testing.T) {
	key := newSigner(t)
	r := newFakeServer(t, key.PublicKey())
	opts := fastOptions(r.addr(), key)
	opts.HostKey = config.HostKeyLine(r.hostKey.PublicKey())
	c := Start(opts)
	defer c.Close()
	for {
		select {
		case ev := <-c.Events():
			switch ev := ev.(type) {
			case HostKeyPinned:
				t.Fatal("already pinned key pinned again")
			case StatusEvent:
				if ev.Status == StatusOnline {
					return
				}
			}
		case <-time.After(10 * time.Second):
			t.Fatal("not online")
		}
	}
}

func TestUnreachable(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	c := Start(fastOptions(addr, newSigner(t)))
	defer c.Close()
	waitStatus(t, c, StatusUnreachable)
}

func TestNotAnSSHServer(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			nc.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
			nc.Close()
		}
	}()
	c := Start(fastOptions(ln.Addr().String(), newSigner(t)))
	defer c.Close()
	waitStatus(t, c, StatusUnreachable)
}

func TestClassify(t *testing.T) {
	if classify(false, errors.New("ssh: handshake failed: ssh: unable to authenticate")) != StatusAuthFailed {
		t.Error("auth error")
	}
	if classify(true, io.EOF) != StatusConnecting {
		t.Error("lost connection")
	}
	if classify(false, io.EOF) != StatusUnreachable {
		t.Error("failed dial")
	}
}
