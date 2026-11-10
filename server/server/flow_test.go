package server_test

import (
	"bytes"
	"crypto/rand"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/server/fake"
	"github.com/helpbuoy/helpbuoy/server/server"
	"github.com/helpbuoy/helpbuoy/server/store"
)

// computerFPs maps computer connections made by env.computer to their fingerprints.
var computerFPs sync.Map

func computerFP(c *fake.Conn) string {
	v, _ := computerFPs.Load(c)
	s, _ := v.(string)
	return s
}

// connect sends Connect from techie h for the computer with fingerprint and
// returns the server's error ("" on success).
func connect(t *testing.T, h *fake.Conn, seq uint32, fingerprint, id string) string {
	t.Helper()
	if err := h.Send(&control.Connect{Seq: seq, Fingerprint: fingerprint, RequestID: id}); err != nil {
		t.Fatal(err)
	}
	return mustWait[control.Reply](t, h, func(r *control.Reply) bool { return r.Seq == seq }).Error
}

// connectTo connects techie h to computer c and waits for SessionReady.
func connectTo(t *testing.T, h, c *fake.Conn, seq uint32) string {
	t.Helper()
	id := control.NewRequestID()
	if msg := connect(t, h, seq, computerFP(c), id); msg != "" {
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

func acceptSession(t *testing.T, h *fake.Conn) (ssh.Channel, control.TechiesessionData) {
	t.Helper()
	select {
	case nch := <-h.Sessions:
		d, err := control.UnmarshalTechiesessionData(nch.ExtraData())
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
		t.Fatal("no session channel at the techie")
	}
	return nil, control.TechiesessionData{}
}

// session runs a request through to an open, spliced session.
type session struct {
	id        string
	cch       ssh.Channel
	hch       ssh.Channel
	hdata     control.TechiesessionData
	ready     *control.SessionReady
	computer  *fake.Conn
	techie    *fake.Conn
	computerK ssh.Signer
}

func startSession(t *testing.T, h *fake.Conn, c *fake.Conn, seq uint32) *session {
	t.Helper()
	s := &session{computer: c, techie: h}
	s.id = control.NewRequestID()
	if msg := connect(t, h, seq, computerFP(c), s.id); msg != "" {
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
	hk := e.addTechie("alice")
	h, _ := e.connect(hk, control.RoleTechie)
	c, ck := e.computer("anna@PC")
	s := startSession(t, h, c, 1)

	if s.ready.RequestID != s.id || s.ready.TechieName != "alice" ||
		s.ready.TechieKey != keyLine(hk, "") {
		t.Errorf("session ready %+v", s.ready)
	}
	if s.hdata.RequestID != s.id || s.hdata.ComputerName != "anna@PC" || s.hdata.ComputerKey != keyLine(ck, "anna@PC") ||
		s.hdata.UserMachine != "user@machine" {
		t.Errorf("techie session data %+v", s.hdata)
	}
	p := presenceWhere(t, h, func(p *control.Presence) bool { return computerInfo(p, fp(ck)).InSessionWith == "alice" })
	if ri := computerInfo(p, fp(ck)).Request; ri == nil || ri.State != control.RequestTaken || ri.TakenBy != "alice" {
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
		t.Fatalf("computer->techie: %v", err)
	}
	wg.Wait()
	s.hch.Write([]byte("input"))
	if got := readAll(t, s.cch, 5); got != "input" {
		t.Errorf("techie->computer %q", got)
	}

	// Half-close: computer EOF reaches the techie, which can still answer.
	s.cch.CloseWrite()
	if n, err := s.hch.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatalf("techie read after computer EOF: %d %v", n, err)
	}
	s.hch.Write([]byte("bye"))
	if got := readAll(t, s.cch, 3); got != "bye" {
		t.Errorf("after half-close %q", got)
	}

	// The techie closes: the computer side closes too and the request ends.
	s.hch.Close()
	if _, err := io.ReadAll(s.cch); err != nil {
		t.Errorf("computer read: %v", err)
	}
	if _, err := s.cch.Write([]byte("x")); err == nil {
		t.Error("computer channel still writable after techie closed")
	}
	if st := mustWait[control.RequestStateMsg](t, c, func(m *control.RequestStateMsg) bool { return m.State != control.RequestTaken }); st.RequestID != s.id || st.State != control.RequestEnded {
		t.Errorf("end state %+v", st)
	}
	presenceWhere(t, h, func(p *control.Presence) bool {
		ci := computerInfo(p, fp(ck))
		return ci.Request == nil && ci.InSessionWith == ""
	})

	// Next request, and this time the computer closes first.
	s = startSession(t, h, c, 2)
	s.cch.Write([]byte("ping"))
	if got := readAll(t, s.hch, 4); got != "ping" {
		t.Errorf("second session %q", got)
	}
	s.cch.Close()
	if _, err := io.ReadAll(s.hch); err != nil {
		t.Errorf("techie read: %v", err)
	}
	mustWait[control.RequestStateMsg](t, c, func(m *control.RequestStateMsg) bool { return m.State == control.RequestEnded })
}

// TestFirstTechieWins: many techies connect to one computer at once; exactly
// one gets it, the others are told the computer is busy.
func TestFirstTechieWins(t *testing.T) {
	e := newEnv(t, nil)
	const n = 12
	techies := make([]*fake.Conn, n)
	for i := range techies {
		techies[i] = e.techie(uniq("h"))
	}
	c, _ := e.computer("anna@PC")
	for round := range 3 {
		ids := make([]string, n)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i, h := range techies {
			ids[i] = control.NewRequestID()
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				h.Send(&control.Connect{Seq: uint32(100 + round), Fingerprint: computerFP(c), RequestID: ids[i]})
			}()
		}
		close(start)
		wg.Wait()
		winner := -1
		for i, h := range techies {
			rep, err := fake.WaitFor(h, wait, func(r *control.Reply) bool { return r.Seq == uint32(100+round) })
			if err != nil {
				t.Fatal(err)
			}
			if rep.Error == "" {
				if winner >= 0 {
					t.Fatalf("round %d: techies %d and %d both won", round, winner, i)
				}
				winner = i
			} else if rep.Error != "computer is busy" {
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

func TestOneTechiePerComputer(t *testing.T) {
	e := newEnv(t, nil)
	h1 := e.techie("alice")
	h2 := e.techie("bob")
	c, _ := e.computer("anna@PC")
	s := startSession(t, h1, c, 1)

	// The second techie can't connect while the session runs.
	if msg := connect(t, h2, 1, computerFP(c), control.NewRequestID()); msg != "computer is busy" {
		t.Errorf("second techie: %q", msg)
	}
	// Nor reuse the running request's id.
	if msg := connect(t, h2, 2, computerFP(c), s.id); msg != "bad request id" {
		t.Errorf("reused id: %q", msg)
	}
	// Nor open a second session channel for the same request.
	if _, err := openSession(c, s.id); openReason(err) != ssh.Prohibited {
		t.Errorf("second session channel: %v", err)
	}
	select {
	case <-h2.Sessions:
		t.Fatal("second techie got a session channel")
	case <-h1.Sessions:
		t.Fatal("first techie got a second session channel")
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
	h := e.techie("alice")
	a, _ := e.computer("anna@PC")
	b, _ := e.computer("bert@PC")

	// No techie connected yet: no session.
	if _, err := openSession(a, control.NewRequestID()); openReason(err) != ssh.Prohibited {
		t.Errorf("session without a techie: %v", err)
	}
	id := connectTo(t, h, a, 1)
	// Another computer can't use it.
	if _, err := openSession(b, id); openReason(err) != ssh.Prohibited {
		t.Errorf("session for someone else's request: %v", err)
	}
	// A techie can't cause a session channel by itself.
	select {
	case <-h.Sessions:
		t.Fatal("techie got a session without the computer opening one")
	case <-time.After(50 * time.Millisecond):
	}
	// The right computer can.
	done := make(chan error, 1)
	go func() { _, err := openSession(a, id); done <- err }()
	acceptSession(t, h)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSessionStartTimeout(t *testing.T) {
	e := newEnv(t, nil)
	h := e.techie("alice")
	c, _ := e.computer("anna@PC")
	id := connectTo(t, h, c, 1)
	e.clock.Advance(time.Minute)
	if st := mustWait[control.RequestStateMsg](t, c, func(m *control.RequestStateMsg) bool { return m.State != control.RequestTaken }); st.State != control.RequestEnded {
		t.Errorf("state %+v", st)
	}
	if _, err := openSession(c, id); openReason(err) != ssh.Prohibited {
		t.Errorf("late session: %v", err)
	}
}

func TestTechieRefusesSession(t *testing.T) {
	e := newEnv(t, nil)
	h := e.techie("alice")
	c, _ := e.computer("anna@PC")
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

func TestTechiesessionCap(t *testing.T) {
	e := newEnv(t, func(c *server.Config) { c.MaxSessionsPerTechie = 2 })
	h := e.techie("alice")
	var fps []string
	for range 3 {
		c, _ := e.computer(uniq("u@pc"))
		fps = append(fps, computerFP(c))
	}
	if connect(t, h, 1, fps[0], control.NewRequestID()) != "" || connect(t, h, 2, fps[1], control.NewRequestID()) != "" {
		t.Fatal("first two refused")
	}
	if msg := connect(t, h, 3, fps[2], control.NewRequestID()); msg == "" {
		t.Fatal("third session accepted")
	}
	// Another techie can still connect.
	if msg := connect(t, e.techie("bob"), 1, fps[2], control.NewRequestID()); msg != "" {
		t.Errorf("other techie: %q", msg)
	}
}

// TestMaxSessions: with max_sessions in state.json the server refuses a
// Connect, from any techie, while that many requests are taken, and accepts
// one again once a session ended.
func TestMaxSessions(t *testing.T) {
	e := newEnv(t, nil)
	e.update(func(st *store.State) error { return st.SetMaxSessions(1) })
	h := e.techie("alice")
	h2 := e.techie("bob")
	c1, _ := e.computer("anna@PC")
	c2, _ := e.computer("bert@PC")
	s := startSession(t, h, c1, 1)
	for _, tc := range []*fake.Conn{h, h2} {
		if msg := connect(t, tc, 2, computerFP(c2), control.NewRequestID()); !strings.Contains(msg, "one session at a time") {
			t.Errorf("second session: %q", msg)
		}
	}
	s.cch.Close()
	mustWait[control.RequestStateMsg](t, c1, func(m *control.RequestStateMsg) bool { return m.State == control.RequestEnded })
	if msg := connect(t, h2, 3, computerFP(c2), control.NewRequestID()); msg != "" {
		t.Errorf("after the first ended: %q", msg)
	}
	// A request waiting for the user counts too.
	if msg := connect(t, h, 4, computerFP(c1), control.NewRequestID()); msg == "" {
		t.Error("connect while a request waits for its user")
	}
	// Lifting the limit takes effect at once.
	e.update(func(st *store.State) error { return st.SetMaxSessions(0) })
	if msg := connect(t, h, 5, computerFP(c1), control.NewRequestID()); msg != "" {
		t.Errorf("no limit: %q", msg)
	}
}

func TestDisconnectEndsSession(t *testing.T) {
	e := newEnv(t, nil)
	h := e.techie("alice")
	h2 := e.techie("bob")
	c, ck := e.computer("anna@PC")
	s := startSession(t, h, c, 1)
	// The techie's whole connection goes away.
	h.Close()
	if _, err := io.ReadAll(s.cch); err != nil {
		t.Errorf("computer read: %v", err)
	}
	mustWait[control.RequestStateMsg](t, c, func(m *control.RequestStateMsg) bool { return m.State == control.RequestEnded })

	// The computer goes away mid-session: the techie's channel closes.
	s = startSession(t, h2, c, 1)
	c.Close()
	if _, err := io.ReadAll(s.hch); err != nil {
		t.Errorf("techie read: %v", err)
	}
	presenceWhere(t, h2, func(p *control.Presence) bool {
		ci := computerInfo(p, fp(ck))
		return !ci.Online && ci.Request == nil
	})
}

func TestRemovedTechiesessionsClose(t *testing.T) {
	e := newEnv(t, nil)
	h := e.techie("alice")
	c, _ := e.computer("anna@PC")
	s := startSession(t, h, c, 1)
	e.update(func(st *store.State) error { return st.RemoveTechie("alice") })
	if _, err := io.ReadAll(s.cch); err != nil {
		t.Errorf("computer read: %v", err)
	}
	waitClosed(t, h)
	mustWait[control.RequestStateMsg](t, c, func(m *control.RequestStateMsg) bool { return m.State == control.RequestEnded })
}

func TestTechieComputerHelloIgnored(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(e.addTechie("alice"))
	c.Send(&control.ComputerHello{UserMachine: "x@y"})
	if h := mustWait[control.Hello](t, c, nil); h.Role != control.RoleTechie {
		t.Fatalf("hello %+v", h)
	}
	assertOpen(t, c)
}

// TestTechieCancelsRequest: the techie who connected stops waiting; the
// computer is told and another techie's cancel is ignored.
func TestTechieCancelsRequest(t *testing.T) {
	e := newEnv(t, nil)
	h := e.techie("alice")
	bob := e.techie("bob")
	c, ck := e.computer("anna@PC")

	id := connectTo(t, h, c, 1)
	bob.Send(&control.CancelRequest{RequestID: id})
	assertOpen(t, bob)
	presenceWhere(t, h, func(p *control.Presence) bool {
		r := computerInfo(p, fp(ck)).Request
		return r != nil && r.ID == id
	})
	h.Send(&control.CancelRequest{RequestID: id})
	if st := mustWait[control.RequestStateMsg](t, c, nil); st.RequestID != id || st.State != control.RequestCancelled {
		t.Errorf("techie cancel %+v", st)
	}
	presenceWhere(t, h, func(p *control.Presence) bool { return computerInfo(p, fp(ck)).Request == nil })
	if _, err := openSession(c, id); openReason(err) != ssh.Prohibited {
		t.Errorf("session after techie cancel: %v", err)
	}
	assertOpen(t, h)
}

// TestCancelTakenRequest: the computer cancels after SessionReady (the user
// refused the techie), before and while the session channel is being opened.
func TestCancelTakenRequest(t *testing.T) {
	e := newEnv(t, nil)
	h := e.techie("alice")
	c, ck := e.computer("anna@PC")

	id := connectTo(t, h, c, 1)
	c.Send(&control.CancelRequest{RequestID: id})
	if st := mustWait[control.RequestStateMsg](t, c, nil); st.RequestID != id || st.State != control.RequestCancelled {
		t.Errorf("cancel taken %+v", st)
	}
	presenceWhere(t, h, func(p *control.Presence) bool { return computerInfo(p, fp(ck)).Request == nil })
	if _, err := openSession(c, id); openReason(err) != ssh.Prohibited {
		t.Errorf("session after cancel: %v", err)
	}

	// Cancel while the server is opening the techie's channel: the techie
	// holds the open, the computer cancels, then the techie accepts. The
	// accepted channel is closed by the server.
	id = connectTo(t, h, c, 2)
	done := make(chan error, 1)
	go func() { _, err := openSession(c, id); done <- err }()
	var nch ssh.NewChannel
	select {
	case nch = <-h.Sessions:
	case <-time.After(wait):
		t.Fatal("no session at techie")
	}
	c.Send(&control.CancelRequest{RequestID: id})
	mustWait[control.RequestStateMsg](t, c, func(m *control.RequestStateMsg) bool { return m.State == control.RequestCancelled })
	ch, reqs, err := nch.Accept()
	if err != nil {
		t.Fatal(err)
	}
	go ssh.DiscardRequests(reqs)
	if _, err := io.ReadAll(ch); err != nil {
		t.Errorf("techie channel: %v", err)
	}
	if err := <-done; openReason(err) != ssh.Prohibited {
		t.Errorf("computer session after cancel: %v", err)
	}
	presenceWhere(t, h, func(p *control.Presence) bool { return computerInfo(p, fp(ck)).Request == nil })
	connectTo(t, h, c, 3) // a fresh connect works
}

// TestConnectFlow: a techie connects to a computer; the user refuses once,
// then allows.
func TestConnectFlow(t *testing.T) {
	e := newEnv(t, nil)
	hk := e.addTechie("alice")
	h, _ := e.connect(hk, control.RoleTechie)
	bob := e.techie("bob")
	c, ck := e.computer("anna@PC")
	offline := e.addComputer("oma@PC")

	if msg := connect(t, h, 1, fp(offline), control.NewRequestID()); msg != "computer is not online" {
		t.Errorf("offline computer: %q", msg)
	}
	if msg := connect(t, h, 2, "SHA256:nobody", control.NewRequestID()); msg != "computer is not online" {
		t.Errorf("unknown computer: %q", msg)
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
	if sr.RequestID != id || sr.TechieName != "alice" || sr.TechieKey != keyLine(hk, "") {
		t.Errorf("session ready %+v", sr)
	}
	p := presenceWhere(t, h, func(p *control.Presence) bool { return computerInfo(p, fp(ck)).Request != nil })
	if ri := computerInfo(p, fp(ck)).Request; ri.ID != id || ri.State != control.RequestTaken || ri.TakenBy != "alice" {
		t.Errorf("request info %+v", ri)
	}
	// One techie at a time.
	if msg := connect(t, bob, 1, fp(ck), control.NewRequestID()); msg != "computer is busy" {
		t.Errorf("second techie: %q", msg)
	}
	c.Send(&control.CancelRequest{RequestID: id})
	mustWait[control.RequestStateMsg](t, c, func(m *control.RequestStateMsg) bool { return m.State == control.RequestCancelled })
	presenceWhere(t, h, func(p *control.Presence) bool { return computerInfo(p, fp(ck)).Request == nil })

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
	if hd.RequestID != id || hd.ComputerName != "anna@PC" {
		t.Errorf("techie data %+v", hd)
	}
	cch.Write([]byte("hi"))
	if got := readAll(t, hch, 2); got != "hi" {
		t.Errorf("got %q", got)
	}
	presenceWhere(t, h, func(p *control.Presence) bool { return computerInfo(p, fp(ck)).InSessionWith == "alice" })
}

// TestConnectNoAnswer: a request from Connect ends when the user does not
// answer within the session start timeout.
func TestConnectNoAnswer(t *testing.T) {
	e := newEnv(t, nil)
	h := e.techie("alice")
	c, ck := e.computer("anna@PC")
	id := control.NewRequestID()
	if msg := connect(t, h, 1, fp(ck), id); msg != "" {
		t.Fatal(msg)
	}
	mustWait[control.SessionReady](t, c, nil)
	e.clock.Advance(time.Minute)
	if st := mustWait[control.RequestStateMsg](t, c, nil); st.RequestID != id || st.State != control.RequestEnded {
		t.Errorf("state %+v", st)
	}
	presenceWhere(t, h, func(p *control.Presence) bool { return computerInfo(p, fp(ck)).Request == nil })
}
