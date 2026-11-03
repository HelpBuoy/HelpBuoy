package session

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/helpbuoy/client-windows/identity"
	"github.com/mevdschee/helpbuoy/client-windows/internal/fakeserver"
	"github.com/mevdschee/helpbuoy/proto/control"
)

// testHandler records what the computer reports.
type testHandler struct {
	states chan State

	mu         sync.Mutex
	registered []string // server host keys
	pinned     map[string]string
	refuse     bool
	hold       bool // ConfirmTechie waits until its context ends
	asked      []asked
}

// asked is one ConfirmTechie call.
type asked struct {
	techie Techie
	oldFP  string
}

func newHandler() *testHandler {
	return &testHandler{states: make(chan State, 256), pinned: map[string]string{}}
}

func (h *testHandler) StateChanged(s State) { h.states <- s }
func (h *testHandler) Registered(hostKey string) {
	h.mu.Lock()
	h.registered = append(h.registered, hostKey)
	h.mu.Unlock()
}
func (h *testHandler) TechiePinned(name, line string) {
	h.mu.Lock()
	h.pinned[name] = line
	h.mu.Unlock()
}
func (h *testHandler) ServerInfo(title, instruction string) {}
func (h *testHandler) ConfirmTechie(ctx context.Context, techie Techie, oldFP string) bool {
	h.mu.Lock()
	h.asked = append(h.asked, asked{techie, oldFP})
	hold, refuse := h.hold, h.refuse
	h.mu.Unlock()
	if hold {
		<-ctx.Done()
		return true // answered too late
	}
	return !refuse
}

// waitPhase waits for a snapshot with phase p.
func (h *testHandler) waitPhase(t *testing.T, p Phase) State {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case s := <-h.states:
			if s.Phase == p {
				return s
			}
		case <-timeout:
			t.Fatalf("timed out waiting for phase %v", p)
		}
	}
}

// fakeRunner records sessions and runs until ctx ends.
type fakeRunner struct {
	started chan ssh.PublicKey
	ended   chan error
}

func newRunner() *fakeRunner {
	return &fakeRunner{started: make(chan ssh.PublicKey, 4), ended: make(chan error, 4)}
}

func (r *fakeRunner) Run(ctx context.Context, ch io.ReadWriteCloser, techieKey ssh.PublicKey) error {
	r.started <- techieKey
	done := make(chan struct{})
	go func() {
		io.Copy(io.Discard, ch) // the techie side closing ends the session too
		close(done)
	}()
	select {
	case <-ctx.Done():
	case <-done:
	}
	r.ended <- ctx.Err()
	return nil
}

func newKey(t *testing.T, comment string) (ssh.Signer, string) {
	t.Helper()
	k, err := identity.Ephemeral(comment)
	if err != nil {
		t.Fatal(err)
	}
	return k.Signer, k.Line
}

func parseKey(line string) (ssh.PublicKey, error) {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	return k, err
}

type fixture struct {
	server   *fakeserver.Server
	h        *testHandler
	runner   *fakeRunner
	signer   ssh.Signer
	fp       string
	computer *Computer
	escFire  chan func()
	escStop  chan struct{}
}

func setup(t *testing.T, mod func(*Options)) *fixture {
	t.Helper()
	server, err := fakeserver.Start("", fakeserver.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	signer, _ := newKey(t, "anna@PC")
	f := &fixture{
		server:  server,
		h:       newHandler(),
		runner:  newRunner(),
		signer:  signer,
		fp:      ssh.FingerprintSHA256(signer.PublicKey()),
		escFire: make(chan func(), 4),
		escStop: make(chan struct{}, 4),
	}
	opts := Options{
		Addr:        server.Addr(),
		Signer:      signer,
		AppVersion:  "test",
		UserMachine: "anna@PC",
		Handler:     f.h,
		Runner:      f.runner,
		ParseKey:    parseKey,
		EscHook: func(onEsc func()) func() {
			f.escFire <- onEsc
			return func() { f.escStop <- struct{}{} }
		},
		DialTimeout: 2 * time.Second,
		MinBackoff:  20 * time.Millisecond,
		MaxBackoff:  50 * time.Millisecond,
	}
	if mod != nil {
		mod(&opts)
	}
	f.computer = New(opts)
	t.Cleanup(func() {
		f.computer.Close()
		select {
		case <-f.computer.Done():
		case <-time.After(5 * time.Second):
			t.Error("computer did not stop")
		}
	})
	return f
}

// recv waits for a control message of the given type from the computer.
func recv[T any](t *testing.T, r *fakeserver.Server) T {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case m := <-r.Received():
			if v, ok := m.Msg.(T); ok {
				return v
			}
		case <-timeout:
			var zero T
			t.Fatalf("timed out waiting for %T", zero)
			return zero
		}
	}
}

func TestFirstRunRegistration(t *testing.T) {
	f := setup(t, nil)
	f.computer.Start()
	s := f.h.waitPhase(t, PhasePending)
	wantFP := ssh.FingerprintSHA256(f.server.HostKey())
	if s.ServerFingerprint != wantFP {
		t.Fatalf("pending state %+v", s)
	}
	f.server.Register(f.fp)
	f.h.waitPhase(t, PhaseReady)
	f.h.mu.Lock()
	reg := f.h.registered
	f.h.mu.Unlock()
	want := identity.KeyLine(f.server.HostKey(), "")
	if len(reg) != 1 || reg[0] != want {
		t.Fatalf("registered %q, want %q", reg, want)
	}
}

func TestAlreadyRegistered(t *testing.T) {
	f := setup(t, nil)
	f.server.Register(f.fp)
	f.computer.Start()
	f.h.waitPhase(t, PhaseReady)
	f.h.mu.Lock()
	defer f.h.mu.Unlock()
	if len(f.h.registered) != 1 {
		t.Fatalf("registered %q", f.h.registered)
	}
}

func TestHostKeyChanged(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	other, _ := ssh.NewSignerFromKey(priv)
	f := setup(t, func(o *Options) {
		o.ServerHostKey = identity.KeyLine(other.PublicKey(), "")
		o.Retry = true
	})
	f.computer.Start()
	s := f.h.waitPhase(t, PhaseStopped)
	if Classify(s.Err) != KindHostKeyChanged {
		t.Fatalf("err %v", s.Err)
	}
	if s.ServerFingerprint != ssh.FingerprintSHA256(other.PublicKey()) {
		t.Fatal("state shows the new key as the server key")
	}
	var hk *HostKeyChangedError
	if !errors.As(s.Err, &hk) || hk.OldFingerprint != ssh.FingerprintSHA256(other.PublicKey()) ||
		hk.NewFingerprint != ssh.FingerprintSHA256(f.server.HostKey()) || hk.NewKeyLine != identity.KeyLine(f.server.HostKey(), "") {
		t.Fatalf("change %+v", hk)
	}
}

func TestPinnedHostKeyAccepted(t *testing.T) {
	var serverKey string
	f := setup(t, nil)
	serverKey = identity.KeyLine(f.server.HostKey(), "")
	f.computer = New(Options{
		Addr: f.server.Addr(), Signer: f.signer, Handler: f.h, ParseKey: parseKey,
		ServerHostKey: serverKey,
	})
	f.computer.Start()
	f.h.waitPhase(t, PhasePending)
}

func TestConnectErrors(t *testing.T) {
	// Refused: a port that was just closed.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	refused := ln.Addr().String()
	ln.Close()

	// Not a server: something that speaks another protocol.
	http, _ := net.Listen("tcp", "127.0.0.1:0")
	defer http.Close()
	go func() {
		for {
			c, err := http.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
			c.Close()
		}
	}()

	// Timeout: accepts and never says anything.
	silent, _ := net.Listen("tcp", "127.0.0.1:0")
	defer silent.Close()
	var held []net.Conn
	var hmu sync.Mutex
	go func() {
		for {
			c, err := silent.Accept()
			if err != nil {
				return
			}
			hmu.Lock()
			held = append(held, c)
			hmu.Unlock()
		}
	}()
	defer func() {
		hmu.Lock()
		for _, c := range held {
			c.Close()
		}
		hmu.Unlock()
	}()

	cases := []struct {
		addr string
		want Kind
	}{
		{refused, KindRefused},
		{http.Addr().String(), KindNotServer},
		{silent.Addr().String(), KindTimeout},
		{"no-such-host.invalid:5959", KindNameNotFound},
	}
	for _, tc := range cases {
		h := newHandler()
		signer, _ := newKey(t, "x")
		c := New(Options{Addr: tc.addr, Signer: signer, Handler: h, DialTimeout: 500 * time.Millisecond})
		c.Start()
		s := h.waitPhase(t, PhaseStopped)
		if got := Classify(s.Err); got != tc.want {
			t.Errorf("%s: kind %v (%v), want %v", tc.addr, got, s.Err, tc.want)
		}
		if Describe(s.Err) == "" {
			t.Errorf("%s: empty description", tc.addr)
		}
		c.Close()
	}
}

func TestRefusedKey(t *testing.T) {
	f := setup(t, nil)
	f.server.RefuseAll(true)
	f.computer.Start()
	s := f.h.waitPhase(t, PhaseStopped)
	if Classify(s.Err) != KindRefusedKey {
		t.Fatalf("err %v", s.Err)
	}
}

func startSession(t *testing.T, f *fixture, techieLine string) fakeserver.Session {
	t.Helper()
	f.server.Register(f.fp)
	f.computer.Start()
	f.h.waitPhase(t, PhaseReady)
	if _, err := f.server.Connect(f.fp, "alice", techieLine); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-f.server.Sessions():
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("no session channel")
	}
	return fakeserver.Session{}
}

func TestSessionAndEsc(t *testing.T) {
	f := setup(t, nil)
	techie, techieLine := newKey(t, "alice")
	sess := startSession(t, f, techieLine)
	if sess.Data.RequestID == "" || sess.Fingerprint != f.fp {
		t.Fatalf("session %+v", sess)
	}
	got := <-f.runner.started
	if !sameKey(got, techie.PublicKey()) {
		t.Fatal("runner got another techie key")
	}
	s := f.h.waitPhase(t, PhaseInSession)
	if s.Techie.Name != "alice" || s.Techie.Fingerprint != ssh.FingerprintSHA256(techie.PublicKey()) {
		t.Fatalf("techie %+v", s.Techie)
	}
	f.h.mu.Lock()
	pinned := f.h.pinned["alice"]
	f.h.mu.Unlock()
	if pinned != identity.KeyLine(techie.PublicKey(), "") {
		t.Fatalf("pinned %q", pinned)
	}
	// Another connect while the session runs is ignored.
	f.server.Connect(f.fp, "bob", techieLine)
	select {
	case <-f.server.Sessions():
		t.Fatal("second session during a session")
	case <-time.After(100 * time.Millisecond):
	}

	// Local Esc ends the session and removes the hook.
	onEsc := <-f.escFire
	onEsc()
	if err := <-f.runner.ended; err == nil {
		t.Fatal("session ended without cancel")
	}
	<-f.escStop
	s = f.h.waitPhase(t, PhaseReady)
	if s.Techie.Name != "alice" {
		t.Fatal("last techie forgotten")
	}

	// The techie can connect again.
	f.server.Connect(f.fp, "alice", techieLine)
	sess = <-f.server.Sessions()
	<-f.runner.started
	f.h.waitPhase(t, PhaseInSession)
	// The techie ending it (closing its channel) ends it too.
	sess.Channel.Close()
	<-f.runner.ended
	<-f.escStop
	f.h.waitPhase(t, PhaseReady)
	f.h.mu.Lock()
	defer f.h.mu.Unlock()
	if len(f.h.asked) != 2 {
		t.Fatalf("asked %d times for 2 sessions", len(f.h.asked))
	}
	fp := ssh.FingerprintSHA256(techie.PublicKey())
	for _, a := range f.h.asked {
		if a.techie.Name != "alice" || a.techie.Fingerprint != fp {
			t.Fatalf("asked about %+v", a.techie)
		}
		if a.oldFP != "" {
			t.Fatalf("warned about an unchanged techie key: %q", a.oldFP)
		}
	}
}

func TestTechieRefused(t *testing.T) {
	f := setup(t, nil)
	f.h.refuse = true
	_, techieLine := newKey(t, "alice")
	f.server.Register(f.fp)
	f.computer.Start()
	f.h.waitPhase(t, PhaseReady)
	id, _ := f.server.Connect(f.fp, "alice", techieLine)
	if c := recv[*control.CancelRequest](t, f.server); c.RequestID != id {
		t.Fatalf("cancelled %q, want %q", c.RequestID, id)
	}
	select {
	case <-f.server.Sessions():
		t.Fatal("session opened despite refusal")
	case <-f.runner.started:
		t.Fatal("runner started despite refusal")
	case <-time.After(100 * time.Millisecond):
	}
	f.h.waitPhase(t, PhaseReady)
	f.h.mu.Lock()
	defer f.h.mu.Unlock()
	if len(f.h.asked) != 1 || f.h.asked[0].oldFP != "" || f.h.pinned["alice"] != "" {
		t.Fatalf("asked %+v, pinned %q", f.h.asked, f.h.pinned["alice"])
	}
}

// TestRequestEndsWhileAsking: the server ends the request (the user did not
// answer in time); the question is withdrawn and no session is opened.
func TestRequestEndsWhileAsking(t *testing.T) {
	f := setup(t, nil)
	f.h.hold = true
	_, techieLine := newKey(t, "alice")
	f.server.Register(f.fp)
	f.computer.Start()
	f.h.waitPhase(t, PhaseReady)
	id, _ := f.server.Connect(f.fp, "alice", techieLine)
	s := f.h.waitPhase(t, PhaseAsking)
	if s.RequestID != id {
		t.Fatalf("asking about %q, want %q", s.RequestID, id)
	}
	f.server.Send(f.fp, control.RequestStateMsg{RequestID: id, State: control.RequestEnded})
	f.h.waitPhase(t, PhaseReady)
	select {
	case <-f.server.Sessions():
		t.Fatal("session opened after the request ended")
	case <-f.runner.started:
		t.Fatal("runner started after the request ended")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestConnectionLossEndsSession(t *testing.T) {
	f := setup(t, nil)
	_, techieLine := newKey(t, "alice")
	startSession(t, f, techieLine)
	<-f.runner.started
	f.h.waitPhase(t, PhaseInSession)
	f.server.Kick(f.fp)
	<-f.runner.ended
	f.h.waitPhase(t, PhaseConnecting)
	f.h.waitPhase(t, PhaseReady) // reconnected
}

func TestTechieKeyChangeRefused(t *testing.T) {
	_, oldLine := newKey(t, "alice")
	f := setup(t, func(o *Options) { o.KnownTechies = map[string]string{"alice": oldLine} })
	f.h.refuse = true
	_, newLine := newKey(t, "alice")
	f.server.Register(f.fp)
	f.computer.Start()
	f.h.waitPhase(t, PhaseReady)
	f.server.Connect(f.fp, "alice", newLine)
	recv[*control.CancelRequest](t, f.server)
	select {
	case <-f.server.Sessions():
		t.Fatal("session opened despite refusal")
	case <-f.runner.started:
		t.Fatal("runner started despite refusal")
	case <-time.After(100 * time.Millisecond):
	}
	f.h.mu.Lock()
	defer f.h.mu.Unlock()
	if len(f.h.asked) != 1 || f.h.asked[0].oldFP == "" || f.h.pinned["alice"] != "" {
		t.Fatalf("asked %+v, pinned %q", f.h.asked, f.h.pinned["alice"])
	}
}

func TestTechieKeyChangeAccepted(t *testing.T) {
	_, oldLine := newKey(t, "alice")
	f := setup(t, func(o *Options) { o.KnownTechies = map[string]string{"alice": oldLine} })
	techie, newLine := newKey(t, "alice")
	startSession(t, f, newLine)
	<-f.runner.started
	f.h.mu.Lock()
	defer f.h.mu.Unlock()
	want := ssh.FingerprintSHA256(oldKey(t, oldLine))
	if len(f.h.asked) != 1 || f.h.asked[0].oldFP != want || f.h.pinned["alice"] != identity.KeyLine(techie.PublicKey(), "") {
		t.Fatalf("asked %+v, pinned %q", f.h.asked, f.h.pinned["alice"])
	}
}

func oldKey(t *testing.T, line string) ssh.PublicKey {
	t.Helper()
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestReconnect(t *testing.T) {
	f := setup(t, func(o *Options) { o.Retry = true })
	f.server.Register(f.fp)
	f.computer.Start()
	f.h.waitPhase(t, PhaseReady)
	f.server.Kick(f.fp)
	f.h.waitPhase(t, PhaseConnecting)
	f.h.waitPhase(t, PhaseReady)
}

func TestRetryWhenServerDown(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	h := newHandler()
	signer, _ := newKey(t, "x")
	c := New(Options{Addr: addr, Signer: signer, Handler: h, Retry: true,
		MinBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond})
	c.Start()
	defer c.Close()
	// Several failed attempts, but it keeps trying.
	for i := 0; i < 3; i++ {
		s := h.waitPhase(t, PhaseConnecting)
		_ = s
	}
	server, err := fakeserver.Start(addr, fakeserver.Options{})
	if err != nil {
		t.Skip("port reuse not possible:", err)
	}
	defer server.Close()
	h.waitPhase(t, PhasePending)
}

func TestServerErrorMessage(t *testing.T) {
	f := setup(t, nil)
	f.computer.Start()
	f.h.waitPhase(t, PhasePending)
	f.server.Send(f.fp, control.Error{Message: "replaced by a newer connection"})
	f.server.Kick(f.fp)
	s := f.h.waitPhase(t, PhaseConnecting)
	for s.Err == nil {
		s = f.h.waitPhase(t, PhaseConnecting)
	}
	if Classify(s.Err) != KindServer {
		t.Fatalf("err %v", s.Err)
	}
}
