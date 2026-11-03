package server_test

import (
	"bytes"
	"crypto/rand"
	"io"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/help-me-win/proto/control"
	"github.com/mevdschee/help-me-win/relay/fake"
	"github.com/mevdschee/help-me-win/relay/server"
	"github.com/mevdschee/help-me-win/relay/store"
)

// clientFPs maps client connections made by env.client to their fingerprints.
var clientFPs sync.Map

func clientFP(c *fake.Conn) string {
	v, _ := clientFPs.Load(c)
	s, _ := v.(string)
	return s
}

// connect sends Connect from helper h for the client with fingerprint and
// returns the relay's error ("" on success).
func connect(t *testing.T, h *fake.Conn, seq uint32, fingerprint, id string) string {
	t.Helper()
	if err := h.Send(&control.Connect{Seq: seq, Fingerprint: fingerprint, RequestID: id}); err != nil {
		t.Fatal(err)
	}
	return mustWait[control.Reply](t, h, func(r *control.Reply) bool { return r.Seq == seq }).Error
}

// connectTo connects helper h to client c and waits for SessionReady.
func connectTo(t *testing.T, h, c *fake.Conn, seq uint32) string {
	t.Helper()
	id := control.NewRequestID()
	if msg := connect(t, h, seq, clientFP(c), id); msg != "" {
		t.Fatalf("connect: %s", msg)
	}
	if sr := mustWait[control.SessionReady](t, c, nil); sr.RequestID != id {
		t.Fatalf("session ready for %s, want %s", sr.RequestID, id)
	}
	return id
}

func openSession(c *fake.Conn, id string) (ssh.Channel, error) {
	ch, reqs, err := c.Client.OpenChannel(control.ChannelSession,
		control.MarshalSessionChannelData(control.SessionChannelData{RequestID: id}))
	if err != nil {
		return nil, err
	}
	go ssh.DiscardRequests(reqs)
	return ch, nil
}

func acceptSession(t *testing.T, h *fake.Conn) (ssh.Channel, control.HelperSessionData) {
	t.Helper()
	select {
	case nch := <-h.Sessions:
		d, err := control.UnmarshalHelperSessionData(nch.ExtraData())
		if err != nil {
			t.Fatal(err)
		}
		ch, reqs, err := nch.Accept()
		if err != nil {
			t.Fatal(err)
		}
		go ssh.DiscardRequests(reqs)
		return ch, d
	case <-time.After(wait):
		t.Fatal("no session channel at the helper")
	}
	return nil, control.HelperSessionData{}
}

// session runs a request through to an open, spliced session.
type session struct {
	id      string
	cch     ssh.Channel
	hch     ssh.Channel
	hdata   control.HelperSessionData
	ready   *control.SessionReady
	client  *fake.Conn
	helper  *fake.Conn
	clientK ssh.Signer
}

func startSession(t *testing.T, h *fake.Conn, c *fake.Conn, seq uint32) *session {
	t.Helper()
	s := &session{client: c, helper: h}
	s.id = control.NewRequestID()
	if msg := connect(t, h, seq, clientFP(c), s.id); msg != "" {
		t.Fatalf("connect: %s", msg)
	}
	s.ready = mustWait[control.SessionReady](t, c, nil)
	type res struct {
		ch  ssh.Channel
		err error
	}
	done := make(chan res, 1)
	go func() {
		ch, err := openSession(c, s.id)
		done <- res{ch, err}
	}()
	s.hch, s.hdata = acceptSession(t, h)
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	s.cch = r.ch
	return s
}

func ptr[T any](v T) *T { return &v }

func TestSessionSplice(t *testing.T) {
	e := newEnv(t, nil)
	hk := e.addHelper("alice")
	h, _ := e.connect(hk, control.RoleHelper)
	c, ck := e.client("anna@PC")
	s := startSession(t, h, c, 1)

	if s.ready.RequestID != s.id || s.ready.HelperName != "alice" || s.ready.HelperDisplayName != "Display alice" ||
		s.ready.HelperKey != keyLine(hk, "") {
		t.Errorf("session ready %+v", s.ready)
	}
	if s.hdata.RequestID != s.id || s.hdata.ClientName != "anna@PC" || s.hdata.ClientKey != keyLine(ck, "anna@PC") ||
		s.hdata.UserMachine != "user@machine" {
		t.Errorf("helper session data %+v", s.hdata)
	}
	p := presenceWhere(t, h, func(p *control.Presence) bool { return clientInfo(p, fp(ck)).InSessionWith == "alice" })
	if ri := clientInfo(p, fp(ck)).Request; ri == nil || ri.State != control.RequestTaken || ri.TakenBy != "alice" {
		t.Errorf("request in session %+v", ri)
	}

	// Bytes flow both ways, also in bulk.
	big := make([]byte, 3<<20)
	rand.Read(big)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.cch.Write(big)
	}()
	got := make([]byte, len(big))
	if _, err := io.ReadFull(s.hch, got); err != nil || !bytes.Equal(got, big) {
		t.Fatalf("client->helper: %v", err)
	}
	wg.Wait()
	s.hch.Write([]byte("input"))
	if got := readAll(t, s.cch, 5); got != "input" {
		t.Errorf("helper->client %q", got)
	}

	// Half-close: client EOF reaches the helper, which can still answer.
	s.cch.CloseWrite()
	if n, err := s.hch.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatalf("helper read after client EOF: %d %v", n, err)
	}
	s.hch.Write([]byte("bye"))
	if got := readAll(t, s.cch, 3); got != "bye" {
		t.Errorf("after half-close %q", got)
	}

	// The helper closes: the client side closes too and the request ends.
	s.hch.Close()
	if _, err := io.ReadAll(s.cch); err != nil {
		t.Errorf("client read: %v", err)
	}
	if _, err := s.cch.Write([]byte("x")); err == nil {
		t.Error("client channel still writable after helper closed")
	}
	if st := mustWait[control.RequestStateMsg](t, c, func(m *control.RequestStateMsg) bool { return m.State != control.RequestTaken }); st.RequestID != s.id || st.State != control.RequestEnded {
		t.Errorf("end state %+v", st)
	}
	presenceWhere(t, h, func(p *control.Presence) bool {
		ci := clientInfo(p, fp(ck))
		return ci.Request == nil && ci.InSessionWith == ""
	})

	// Next request, and this time the client closes first.
	s = startSession(t, h, c, 2)
	s.cch.Write([]byte("ping"))
	if got := readAll(t, s.hch, 4); got != "ping" {
		t.Errorf("second session %q", got)
	}
	s.cch.Close()
	if _, err := io.ReadAll(s.hch); err != nil {
		t.Errorf("helper read: %v", err)
	}
	mustWait[control.RequestStateMsg](t, c, func(m *control.RequestStateMsg) bool { return m.State == control.RequestEnded })
}

// TestFirstHelperWins: many helpers connect to one client at once; exactly
// one gets it, the others are told the client is busy.
func TestFirstHelperWins(t *testing.T) {
	e := newEnv(t, nil)
	const n = 12
	helpers := make([]*fake.Conn, n)
	for i := range helpers {
		helpers[i] = e.helper(uniq("h"))
	}
	c, _ := e.client("anna@PC")
	for round := range 3 {
		ids := make([]string, n)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i, h := range helpers {
			ids[i] = control.NewRequestID()
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				h.Send(&control.Connect{Seq: uint32(100 + round), Fingerprint: clientFP(c), RequestID: ids[i]})
			}()
		}
		close(start)
		wg.Wait()
		winner := -1
		for i, h := range helpers {
			rep, err := fake.WaitFor(h, wait, func(r *control.Reply) bool { return r.Seq == uint32(100+round) })
			if err != nil {
				t.Fatal(err)
			}
			if rep.Error == "" {
				if winner >= 0 {
					t.Fatalf("round %d: helpers %d and %d both won", round, winner, i)
				}
				winner = i
			} else if rep.Error != "client is busy" {
				t.Errorf("loser error %q", rep.Error)
			}
		}
		if winner < 0 {
			t.Fatalf("round %d: nobody won", round)
		}
		if ready := mustWait[control.SessionReady](t, c, nil); ready.RequestID != ids[winner] {
			t.Errorf("ready for %s", ready.RequestID)
		}
		// The user refuses; the next round starts fresh.
		c.Send(&control.CancelRequest{RequestID: ids[winner]})
		mustWait[control.RequestStateMsg](t, c, func(m *control.RequestStateMsg) bool { return m.State == control.RequestCancelled })
	}
}

func TestOneHelperPerClient(t *testing.T) {
	e := newEnv(t, nil)
	h1 := e.helper("alice")
	h2 := e.helper("bob")
	c, _ := e.client("anna@PC")
	s := startSession(t, h1, c, 1)

	// The second helper can't connect while the session runs.
	if msg := connect(t, h2, 1, clientFP(c), control.NewRequestID()); msg != "client is busy" {
		t.Errorf("second helper: %q", msg)
	}
	// Nor reuse the running request's id.
	if msg := connect(t, h2, 2, clientFP(c), s.id); msg != "bad request id" {
		t.Errorf("reused id: %q", msg)
	}
	// Nor open a second session channel for the same request.
	if _, err := openSession(c, s.id); openReason(err) != ssh.Prohibited {
		t.Errorf("second session channel: %v", err)
	}
	select {
	case <-h2.Sessions:
		t.Fatal("second helper got a session channel")
	case <-h1.Sessions:
		t.Fatal("first helper got a second session channel")
	case <-time.After(50 * time.Millisecond):
	}
	// The session still works.
	s.cch.Write([]byte("ok"))
	if got := readAll(t, s.hch, 2); got != "ok" {
		t.Errorf("session %q", got)
	}
}

func TestSessionOnlyForOwnTakenRequest(t *testing.T) {
	e := newEnv(t, nil)
	h := e.helper("alice")
	a, _ := e.client("anna@PC")
	b, _ := e.client("bert@PC")

	// No helper connected yet: no session.
	if _, err := openSession(a, control.NewRequestID()); openReason(err) != ssh.Prohibited {
		t.Errorf("session without a helper: %v", err)
	}
	id := connectTo(t, h, a, 1)
	// Another client can't use it.
	if _, err := openSession(b, id); openReason(err) != ssh.Prohibited {
		t.Errorf("session for someone else's request: %v", err)
	}
	// A helper can't cause a session channel by itself.
	select {
	case <-h.Sessions:
		t.Fatal("helper got a session without the client opening one")
	case <-time.After(50 * time.Millisecond):
	}
	// The right client can.
	done := make(chan error, 1)
	go func() { _, err := openSession(a, id); done <- err }()
	acceptSession(t, h)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSessionStartTimeout(t *testing.T) {
	e := newEnv(t, nil)
	h := e.helper("alice")
	c, _ := e.client("anna@PC")
	id := connectTo(t, h, c, 1)
	e.clock.Advance(time.Minute)
	if st := mustWait[control.RequestStateMsg](t, c, func(m *control.RequestStateMsg) bool { return m.State != control.RequestTaken }); st.State != control.RequestEnded {
		t.Errorf("state %+v", st)
	}
	if _, err := openSession(c, id); openReason(err) != ssh.Prohibited {
		t.Errorf("late session: %v", err)
	}
}

func TestHelperRefusesSession(t *testing.T) {
	e := newEnv(t, nil)
	h := e.helper("alice")
	c, _ := e.client("anna@PC")
	id := connectTo(t, h, c, 1)
	go func() {
		nch := <-h.Sessions
		nch.Reject(ssh.Prohibited, "no")
	}()
	if _, err := openSession(c, id); openReason(err) != ssh.ConnectionFailed {
		t.Errorf("open: %v", err)
	}
	mustWait[control.RequestStateMsg](t, c, func(m *control.RequestStateMsg) bool { return m.State == control.RequestEnded })
}

func TestHelperSessionCap(t *testing.T) {
	e := newEnv(t, func(c *server.Config) { c.MaxSessionsPerHelper = 2 })
	h := e.helper("alice")
	var fps []string
	for range 3 {
		c, _ := e.client(uniq("u@pc"))
		fps = append(fps, clientFP(c))
	}
	if connect(t, h, 1, fps[0], control.NewRequestID()) != "" || connect(t, h, 2, fps[1], control.NewRequestID()) != "" {
		t.Fatal("first two refused")
	}
	if msg := connect(t, h, 3, fps[2], control.NewRequestID()); msg == "" {
		t.Fatal("third session accepted")
	}
	// Another helper can still connect.
	if msg := connect(t, e.helper("bob"), 1, fps[2], control.NewRequestID()); msg != "" {
		t.Errorf("other helper: %q", msg)
	}
}

func TestDisconnectEndsSession(t *testing.T) {
	e := newEnv(t, nil)
	h := e.helper("alice")
	h2 := e.helper("bob")
	c, ck := e.client("anna@PC")
	s := startSession(t, h, c, 1)
	// The helper's whole connection goes away.
	h.Close()
	if _, err := io.ReadAll(s.cch); err != nil {
		t.Errorf("client read: %v", err)
	}
	mustWait[control.RequestStateMsg](t, c, func(m *control.RequestStateMsg) bool { return m.State == control.RequestEnded })

	// The client goes away mid-session: the helper's channel closes.
	s = startSession(t, h2, c, 1)
	c.Close()
	if _, err := io.ReadAll(s.hch); err != nil {
		t.Errorf("helper read: %v", err)
	}
	presenceWhere(t, h2, func(p *control.Presence) bool {
		ci := clientInfo(p, fp(ck))
		return !ci.Online && ci.Request == nil
	})
}

func TestRemovedHelperSessionsClose(t *testing.T) {
	e := newEnv(t, nil)
	h := e.helper("alice")
	c, _ := e.client("anna@PC")
	s := startSession(t, h, c, 1)
	e.update(func(st *store.State) error { return st.RemoveHelper("alice") })
	if _, err := io.ReadAll(s.cch); err != nil {
		t.Errorf("client read: %v", err)
	}
	waitClosed(t, h)
	mustWait[control.RequestStateMsg](t, c, func(m *control.RequestStateMsg) bool { return m.State == control.RequestEnded })
}

func TestHelperClientHelloIgnored(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(e.addHelper("alice"))
	c.Send(&control.ClientHello{UserMachine: "x@y"})
	if h := mustWait[control.Hello](t, c, nil); h.Role != control.RoleHelper {
		t.Fatalf("hello %+v", h)
	}
	assertOpen(t, c)
}

// TestCancelTakenRequest: the client cancels after SessionReady (the user
// refused the helper), before and while the session channel is being opened.
func TestCancelTakenRequest(t *testing.T) {
	e := newEnv(t, nil)
	h := e.helper("alice")
	c, ck := e.client("anna@PC")

	id := connectTo(t, h, c, 1)
	c.Send(&control.CancelRequest{RequestID: id})
	if st := mustWait[control.RequestStateMsg](t, c, nil); st.RequestID != id || st.State != control.RequestCancelled {
		t.Errorf("cancel taken %+v", st)
	}
	presenceWhere(t, h, func(p *control.Presence) bool { return clientInfo(p, fp(ck)).Request == nil })
	if _, err := openSession(c, id); openReason(err) != ssh.Prohibited {
		t.Errorf("session after cancel: %v", err)
	}

	// Cancel while the relay is opening the helper's channel: the helper
	// holds the open, the client cancels, then the helper accepts. The
	// accepted channel is closed by the relay.
	id = connectTo(t, h, c, 2)
	done := make(chan error, 1)
	go func() { _, err := openSession(c, id); done <- err }()
	var nch ssh.NewChannel
	select {
	case nch = <-h.Sessions:
	case <-time.After(wait):
		t.Fatal("no session at helper")
	}
	c.Send(&control.CancelRequest{RequestID: id})
	mustWait[control.RequestStateMsg](t, c, func(m *control.RequestStateMsg) bool { return m.State == control.RequestCancelled })
	ch, reqs, err := nch.Accept()
	if err != nil {
		t.Fatal(err)
	}
	go ssh.DiscardRequests(reqs)
	if _, err := io.ReadAll(ch); err != nil {
		t.Errorf("helper channel: %v", err)
	}
	if err := <-done; openReason(err) != ssh.Prohibited {
		t.Errorf("client session after cancel: %v", err)
	}
	presenceWhere(t, h, func(p *control.Presence) bool { return clientInfo(p, fp(ck)).Request == nil })
	connectTo(t, h, c, 3) // a fresh connect works
}

// TestConnectFlow: a helper connects to a client; the user refuses once,
// then allows.
func TestConnectFlow(t *testing.T) {
	e := newEnv(t, nil)
	hk := e.addHelper("alice")
	h, _ := e.connect(hk, control.RoleHelper)
	bob := e.helper("bob")
	c, ck := e.client("anna@PC")
	offline := e.addClient("oma@PC")

	if msg := connect(t, h, 1, fp(offline), control.NewRequestID()); msg != "client is not online" {
		t.Errorf("offline client: %q", msg)
	}
	if msg := connect(t, h, 2, "SHA256:nobody", control.NewRequestID()); msg != "client is not online" {
		t.Errorf("unknown client: %q", msg)
	}
	if msg := connect(t, h, 3, fp(ck), "not-hex"); msg != "bad request id" {
		t.Errorf("bad id: %q", msg)
	}

	// Refused by the user.
	id := control.NewRequestID()
	if msg := connect(t, h, 4, fp(ck), id); msg != "" {
		t.Fatal(msg)
	}
	sr := mustWait[control.SessionReady](t, c, nil)
	if sr.RequestID != id || sr.HelperName != "alice" || sr.HelperKey != keyLine(hk, "") {
		t.Errorf("session ready %+v", sr)
	}
	p := presenceWhere(t, h, func(p *control.Presence) bool { return clientInfo(p, fp(ck)).Request != nil })
	if ri := clientInfo(p, fp(ck)).Request; ri.ID != id || ri.State != control.RequestTaken || ri.TakenBy != "alice" {
		t.Errorf("request info %+v", ri)
	}
	// One helper at a time.
	if msg := connect(t, bob, 1, fp(ck), control.NewRequestID()); msg != "client is busy" {
		t.Errorf("second helper: %q", msg)
	}
	c.Send(&control.CancelRequest{RequestID: id})
	mustWait[control.RequestStateMsg](t, c, func(m *control.RequestStateMsg) bool { return m.State == control.RequestCancelled })
	presenceWhere(t, h, func(p *control.Presence) bool { return clientInfo(p, fp(ck)).Request == nil })

	// Allowed: the session is spliced.
	id = control.NewRequestID()
	if msg := connect(t, h, 6, fp(ck), id); msg != "" {
		t.Fatal(msg)
	}
	mustWait[control.SessionReady](t, c, nil)
	done := make(chan error, 1)
	var cch ssh.Channel
	go func() {
		var err error
		cch, err = openSession(c, id)
		done <- err
	}()
	hch, hd := acceptSession(t, h)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if hd.RequestID != id || hd.ClientName != "anna@PC" {
		t.Errorf("helper data %+v", hd)
	}
	cch.Write([]byte("hi"))
	if got := readAll(t, hch, 2); got != "hi" {
		t.Errorf("got %q", got)
	}
	presenceWhere(t, h, func(p *control.Presence) bool { return clientInfo(p, fp(ck)).InSessionWith == "alice" })
}

// TestConnectNoAnswer: a request from Connect ends when the user does not
// answer within the session start timeout.
func TestConnectNoAnswer(t *testing.T) {
	e := newEnv(t, nil)
	h := e.helper("alice")
	c, ck := e.client("anna@PC")
	id := control.NewRequestID()
	if msg := connect(t, h, 1, fp(ck), id); msg != "" {
		t.Fatal(msg)
	}
	mustWait[control.SessionReady](t, c, nil)
	e.clock.Advance(time.Minute)
	if st := mustWait[control.RequestStateMsg](t, c, nil); st.RequestID != id || st.State != control.RequestEnded {
		t.Errorf("state %+v", st)
	}
	presenceWhere(t, h, func(p *control.Presence) bool { return clientInfo(p, fp(ck)).Request == nil })
}
