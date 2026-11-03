package session

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/help-me-win/client-windows/identity"
	"github.com/mevdschee/help-me-win/client-windows/internal/fakerelay"
	"github.com/mevdschee/help-me-win/proto/control"
)

// testHandler records what the client reports.
type testHandler struct {
	states chan State

	mu         sync.Mutex
	registered []string // name|hostkey
	pinned     map[string]string
	confirm    bool
	asked      int
}

func newHandler() *testHandler {
	return &testHandler{states: make(chan State, 256), pinned: map[string]string{}}
}

func (h *testHandler) StateChanged(s State) { h.states <- s }
func (h *testHandler) Registered(name, hostKey string) {
	h.mu.Lock()
	h.registered = append(h.registered, name+"|"+hostKey)
	h.mu.Unlock()
}
func (h *testHandler) HelperPinned(name, line string) {
	h.mu.Lock()
	h.pinned[name] = line
	h.mu.Unlock()
}
func (h *testHandler) ConfirmHelperKeyChange(name, oldFP, newFP string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.asked++
	return h.confirm
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

func (r *fakeRunner) Run(ctx context.Context, ch io.ReadWriteCloser, helperKey ssh.PublicKey) error {
	r.started <- helperKey
	done := make(chan struct{})
	go func() {
		io.Copy(io.Discard, ch) // the helper side closing ends the session too
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
	relay   *fakerelay.Relay
	h       *testHandler
	runner  *fakeRunner
	signer  ssh.Signer
	fp      string
	client  *Client
	escFire chan func()
	escStop chan struct{}
}

func setup(t *testing.T, mod func(*Options)) *fixture {
	t.Helper()
	relay, err := fakerelay.Start("", fakerelay.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { relay.Close() })
	signer, _ := newKey(t, "anna@PC")
	f := &fixture{
		relay:   relay,
		h:       newHandler(),
		runner:  newRunner(),
		signer:  signer,
		fp:      ssh.FingerprintSHA256(signer.PublicKey()),
		escFire: make(chan func(), 4),
		escStop: make(chan struct{}, 4),
	}
	opts := Options{
		Addr:        relay.Addr(),
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
	f.client = New(opts)
	t.Cleanup(func() {
		f.client.Close()
		select {
		case <-f.client.Done():
		case <-time.After(5 * time.Second):
			t.Error("client did not stop")
		}
	})
	return f
}

// recv waits for a control message of the given type from the client.
func recv[T any](t *testing.T, r *fakerelay.Relay) T {
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
	f.client.Start()
	s := f.h.waitPhase(t, PhasePending)
	wantFP := ssh.FingerprintSHA256(f.relay.HostKey())
	if s.RelayFingerprint != wantFP || s.Name != "" {
		t.Fatalf("pending state %+v", s)
	}
	if f.client.RequestHelp() {
		t.Fatal("pending client could request help")
	}

	f.relay.Register(f.fp, "anna@PC")
	s = f.h.waitPhase(t, PhaseReady)
	if s.Name != "anna@PC" {
		t.Fatalf("name %q", s.Name)
	}
	f.h.mu.Lock()
	reg := f.h.registered
	f.h.mu.Unlock()
	want := "anna@PC|" + identity.KeyLine(f.relay.HostKey(), "")
	if len(reg) != 1 || reg[0] != want {
		t.Fatalf("registered %q, want %q", reg, want)
	}
}

func TestAlreadyRegistered(t *testing.T) {
	f := setup(t, nil)
	f.relay.Register(f.fp, "anna@PC")
	f.client.Start()
	s := f.h.waitPhase(t, PhaseReady)
	if s.Name != "anna@PC" {
		t.Fatal(s)
	}
}

func TestHostKeyChanged(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	other, _ := ssh.NewSignerFromKey(priv)
	f := setup(t, func(o *Options) {
		o.RelayHostKey = identity.KeyLine(other.PublicKey(), "")
		o.Retry = true
	})
	f.client.Start()
	s := f.h.waitPhase(t, PhaseStopped)
	if Classify(s.Err) != KindHostKeyChanged {
		t.Fatalf("err %v", s.Err)
	}
	if s.RelayFingerprint != ssh.FingerprintSHA256(other.PublicKey()) {
		t.Fatal("state shows the new key as the relay key")
	}
}

func TestPinnedHostKeyAccepted(t *testing.T) {
	var relayKey string
	f := setup(t, nil)
	relayKey = identity.KeyLine(f.relay.HostKey(), "")
	f.client = New(Options{
		Addr: f.relay.Addr(), Signer: f.signer, Handler: f.h, ParseKey: parseKey,
		RelayHostKey: relayKey,
	})
	f.client.Start()
	f.h.waitPhase(t, PhasePending)
}

func TestConnectErrors(t *testing.T) {
	// Refused: a port that was just closed.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	refused := ln.Addr().String()
	ln.Close()

	// Not a relay: something that speaks another protocol.
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
		{http.Addr().String(), KindNotRelay},
		{silent.Addr().String(), KindTimeout},
		{"no-such-host.invalid:5938", KindNameNotFound},
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
	f.relay.RefuseAll(true)
	f.client.Start()
	s := f.h.waitPhase(t, PhaseStopped)
	if Classify(s.Err) != KindRefusedKey {
		t.Fatalf("err %v", s.Err)
	}
}

func TestRequestAndCancel(t *testing.T) {
	f := setup(t, nil)
	f.relay.Register(f.fp, "anna@PC")
	f.client.Start()
	f.h.waitPhase(t, PhaseReady)

	if !f.client.RequestHelp() {
		t.Fatal("RequestHelp refused")
	}
	recv[*control.RequestHelp](t, f.relay)
	if f.client.RequestHelp() {
		t.Fatal("second RequestHelp accepted")
	}
	// Wait until the relay's "open" arrived and the id is known.
	var s State
	for s.RequestID == "" {
		s = f.h.waitPhase(t, PhaseRequested)
	}
	f.client.CancelRequest()
	c := recv[*control.CancelRequest](t, f.relay)
	if c.RequestID != s.RequestID {
		t.Fatalf("cancelled %q, want %q", c.RequestID, s.RequestID)
	}
	f.h.waitPhase(t, PhaseReady)
}

func TestCancelBeforeOpen(t *testing.T) {
	f := setup(t, nil)
	f.relay.Register(f.fp, "anna@PC")
	f.client.Start()
	f.h.waitPhase(t, PhaseReady)
	f.client.RequestHelp()
	f.client.CancelRequest() // before the relay's "open" can have arrived
	c := recv[*control.CancelRequest](t, f.relay)
	if c.RequestID == "" {
		t.Fatal("empty request id")
	}
	if st := f.client.State(); st.Phase != PhaseReady {
		t.Fatalf("phase %v", st.Phase)
	}
}

func startSession(t *testing.T, f *fixture, helperLine string) fakerelay.Session {
	t.Helper()
	f.relay.Register(f.fp, "anna@PC")
	f.client.Start()
	f.h.waitPhase(t, PhaseReady)
	f.client.RequestHelp()
	recv[*control.RequestHelp](t, f.relay)
	for f.client.State().RequestID == "" {
		f.h.waitPhase(t, PhaseRequested)
	}
	if err := f.relay.Take(f.fp, "alice", helperLine); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-f.relay.Sessions():
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("no session channel")
	}
	return fakerelay.Session{}
}

func TestSessionAndEsc(t *testing.T) {
	f := setup(t, nil)
	helper, helperLine := newKey(t, "alice")
	sess := startSession(t, f, helperLine)
	if sess.Data.RequestID == "" || sess.Fingerprint != f.fp {
		t.Fatalf("session %+v", sess)
	}
	got := <-f.runner.started
	if !sameKey(got, helper.PublicKey()) {
		t.Fatal("runner got another helper key")
	}
	s := f.h.waitPhase(t, PhaseInSession)
	if s.Helper.Name != "alice" || s.Helper.Fingerprint != ssh.FingerprintSHA256(helper.PublicKey()) {
		t.Fatalf("helper %+v", s.Helper)
	}
	f.h.mu.Lock()
	pinned := f.h.pinned["alice"]
	f.h.mu.Unlock()
	if pinned != identity.KeyLine(helper.PublicKey(), "") {
		t.Fatalf("pinned %q", pinned)
	}
	if f.client.RequestHelp() {
		t.Fatal("RequestHelp during a session")
	}

	// Local Esc ends the session and removes the hook.
	onEsc := <-f.escFire
	onEsc()
	if err := <-f.runner.ended; err == nil {
		t.Fatal("session ended without cancel")
	}
	<-f.escStop
	s = f.h.waitPhase(t, PhaseReady)
	if s.Helper.Name != "alice" {
		t.Fatal("last helper forgotten")
	}

	// A new request gives a new session.
	f.client.RequestHelp()
	recv[*control.RequestHelp](t, f.relay)
	for f.client.State().RequestID == "" {
		f.h.waitPhase(t, PhaseRequested)
	}
	f.relay.Take(f.fp, "alice", helperLine)
	sess = <-f.relay.Sessions()
	<-f.runner.started
	f.h.waitPhase(t, PhaseInSession)
	// The helper ending it (closing its channel) ends it too.
	sess.Channel.Close()
	<-f.runner.ended
	<-f.escStop
	f.h.waitPhase(t, PhaseReady)
	f.h.mu.Lock()
	asked := f.h.asked
	f.h.mu.Unlock()
	if asked != 0 {
		t.Fatal("asked about an unchanged helper key")
	}
}

func TestConnectionLossEndsSession(t *testing.T) {
	f := setup(t, nil)
	_, helperLine := newKey(t, "alice")
	startSession(t, f, helperLine)
	<-f.runner.started
	f.h.waitPhase(t, PhaseInSession)
	f.relay.Kick(f.fp)
	<-f.runner.ended
	f.h.waitPhase(t, PhaseConnecting)
	f.h.waitPhase(t, PhaseReady) // reconnected
}

func TestHelperKeyChangeRefused(t *testing.T) {
	_, oldLine := newKey(t, "alice")
	f := setup(t, func(o *Options) { o.KnownHelpers = map[string]string{"alice": oldLine} })
	_, newLine := newKey(t, "alice")
	f.relay.Register(f.fp, "anna@PC")
	f.client.Start()
	f.h.waitPhase(t, PhaseReady)
	f.client.RequestHelp()
	recv[*control.RequestHelp](t, f.relay)
	for f.client.State().RequestID == "" {
		f.h.waitPhase(t, PhaseRequested)
	}
	f.relay.Take(f.fp, "alice", newLine)
	recv[*control.CancelRequest](t, f.relay)
	select {
	case <-f.relay.Sessions():
		t.Fatal("session opened despite refusal")
	case <-f.runner.started:
		t.Fatal("runner started despite refusal")
	case <-time.After(100 * time.Millisecond):
	}
	f.h.mu.Lock()
	defer f.h.mu.Unlock()
	if f.h.asked != 1 || f.h.pinned["alice"] != "" {
		t.Fatalf("asked %d, pinned %q", f.h.asked, f.h.pinned["alice"])
	}
}

func TestHelperKeyChangeAccepted(t *testing.T) {
	_, oldLine := newKey(t, "alice")
	f := setup(t, func(o *Options) { o.KnownHelpers = map[string]string{"alice": oldLine} })
	f.h.confirm = true
	helper, newLine := newKey(t, "alice")
	startSession(t, f, newLine)
	<-f.runner.started
	f.h.mu.Lock()
	defer f.h.mu.Unlock()
	if f.h.asked != 1 || f.h.pinned["alice"] != identity.KeyLine(helper.PublicKey(), "") {
		t.Fatalf("asked %d, pinned %q", f.h.asked, f.h.pinned["alice"])
	}
}

func TestReconnect(t *testing.T) {
	f := setup(t, func(o *Options) { o.Retry = true })
	f.relay.Register(f.fp, "anna@PC")
	f.client.Start()
	f.h.waitPhase(t, PhaseReady)
	f.relay.Kick(f.fp)
	f.h.waitPhase(t, PhaseConnecting)
	f.h.waitPhase(t, PhaseReady)
}

func TestRetryWhenRelayDown(t *testing.T) {
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
	relay, err := fakerelay.Start(addr, fakerelay.Options{})
	if err != nil {
		t.Skip("port reuse not possible:", err)
	}
	defer relay.Close()
	h.waitPhase(t, PhasePending)
}

func TestRelayErrorMessage(t *testing.T) {
	f := setup(t, nil)
	f.client.Start()
	f.h.waitPhase(t, PhasePending)
	f.relay.Send(f.fp, control.Error{Message: "replaced by a newer connection"})
	f.relay.Kick(f.fp)
	s := f.h.waitPhase(t, PhaseConnecting)
	for s.Err == nil {
		s = f.h.waitPhase(t, PhaseConnecting)
	}
	if Classify(s.Err) != KindRelay {
		t.Fatalf("err %v", s.Err)
	}
}
