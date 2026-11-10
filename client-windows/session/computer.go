// Package session keeps the computer's connection to the server: SSH with the
// app key, the sv-control channel, help requests, and the session channel
// with the inner SSH server once a techie opened the request (PLAN.md 1, 3.3,
// 3.5). It has no GUI dependencies; the window gets a State snapshot after
// every change through Handler.
package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/client-windows/identity"
	"github.com/helpbuoy/helpbuoy/proto/control"
	pid "github.com/helpbuoy/helpbuoy/proto/identity"
)

// Phase is what the computer is doing, as far as the window cares.
type Phase int

// Phases, derived from the connection, request and session state.
const (
	PhaseStopped    Phase = iota // not started, closed, or given up (see State.Err)
	PhaseConnecting              // dialing, or waiting before the next attempt
	PhasePending                 // connected, key not registered yet
	PhaseReady                   // registered and connected, waiting for a techie
	PhaseAsking                  // a techie wants to connect; the user is asked, then the session starts
	PhaseInSession               // the session runs
)

func (p Phase) String() string {
	return [...]string{"stopped", "connecting", "pending", "ready", "asking", "in-session"}[p]
}

// Techie describes the techie of the current or last session.
type Techie struct {
	Name        string
	Fingerprint string
}

// State is a snapshot of the computer for the window.
type State struct {
	Phase Phase
	// Err is the last connection error. It is cleared once connected.
	Err error
	// ServerHostKey is the server host key line (pinned, or seen on this
	// first connection), ServerFingerprint its fingerprint.
	ServerHostKey     string
	ServerFingerprint string
	// RequestID of the techie's connect being asked about or running.
	RequestID string
	// Techie of the running session, or of the last one.
	Techie Techie
}

// Handler receives events from the computer. Its methods are called from the
// computer's goroutines and must return quickly, except ConfirmTechie, which
// may block while the user decides.
type Handler interface {
	// StateChanged reports a new snapshot. Snapshots arrive in order.
	StateChanged(State)
	// Registered reports that the server knows this key as a computer, and
	// the server host key line to pin from now on.
	Registered(serverHostKey string)
	// TechiePinned reports a techie key to remember for name.
	TechiePinned(name, keyLine string)
	// ServerInfo reports who runs the server, after connecting and whenever
	// a techie changes it.
	ServerInfo(info control.ServerInfo)
	// ApprovedServers reports the servers this server approved for this
	// computer (PLAN.md 12), after connecting as a registered computer and
	// whenever the list changes.
	ApprovedServers(servers []control.ApprovedServer)
	// ListingRefused reports that a techie refused to approve a server.
	ListingRefused(s control.ApprovedServer)
	// ConfirmTechie asks the user whether techie h may take control. It is
	// called for every session, before the session channel is opened.
	// oldFingerprint is set when the techie called h.Name showed a different
	// key last time ("unreadable" if the pinned key can't be parsed). ctx
	// ends when the question is moot (the server ended the request, or the
	// connection was lost); the implementation should then stop asking.
	ConfirmTechie(ctx context.Context, h Techie, oldFingerprint string) bool
}

// Runner runs one session on the computer's sv-session channel: the inner SSH
// server and the screen and input streams. It returns when the session ends
// or ctx is cancelled (local Esc, window closed, server connection lost).
type Runner interface {
	Run(ctx context.Context, ch io.ReadWriteCloser, techieKey ssh.PublicKey) error
}

// Options configure a Computer. Addr, Signer and Handler are required.
type Options struct {
	Addr        string     // server host:port, already normalized
	Signer      ssh.Signer // the app key
	AppVersion  string
	UserMachine string // current user@machine for ComputerHello

	// ServerHostKey is the pinned server host key line. Without it,
	// ServerFingerprint pins the host key by its fingerprint (a server
	// approved by the primary server, PLAN.md 12); without either the host
	// key is trusted on first use.
	ServerHostKey     string
	ServerFingerprint string
	// Listing, if set, is sent to the server after every connection as a
	// registered computer: a request to approve another server.
	Listing *control.RequestListing
	// KnownTechies maps techie names to pinned key lines.
	KnownTechies map[string]string
	// Retry keeps retrying when the very first attempt fails. Without it the
	// computer gives up after a failed first attempt, so the first-run window
	// can show the error; after one successful connection it always retries.
	Retry bool

	Handler Handler
	// Runner runs sessions; nil refuses every session.
	Runner Runner
	// EscHook, if set, is called when a session starts. It must arrange for
	// onEsc to be called when the user presses Esc on the local keyboard and
	// return a function that removes that hook again.
	EscHook func(onEsc func()) (stop func())
	// ParseKey parses a techie key line; default proto/identity.ParseLine.
	ParseKey func(line string) (ssh.PublicKey, error)

	DialTimeout time.Duration // TCP connect, SSH handshake and Hello; default 10 s
	KeepAlive   time.Duration // default 15 s; the connection is dropped after 3 missed
	MinBackoff  time.Duration // default 1 s
	MaxBackoff  time.Duration // default 30 s
	Logger      *slog.Logger
}

// Computer is one server connection with automatic reconnects.
type Computer struct {
	opts   Options
	log    *slog.Logger
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	notifyMu sync.Mutex // serializes Handler.StateChanged calls

	mu          sync.Mutex
	started     bool
	stopped     bool
	err         error
	hostKey     ssh.PublicKey
	techies     map[string]string
	conn        *serverConn // nil while disconnected
	pending     bool
	reqID       string             // request of the SessionReady being handled, "" once it ended
	reqEnded    context.CancelFunc // cancels the ConfirmTechie context
	sessionBusy bool               // SessionReady accepted, until the session ended
	inSession   bool
	techie      Techie
	endSession  context.CancelFunc
}

// serverConn is one established server connection.
type serverConn struct {
	ssh    *ssh.Client
	ctl    *control.Conn
	ctx    context.Context // cancelled when the connection ends
	sendq  chan any
	cancel context.CancelFunc
}

// send queues a control message. It never blocks; a full queue means the
// server stopped reading, so the connection is dropped.
func (rc *serverConn) send(m any) {
	select {
	case rc.sendq <- m:
	default:
		rc.ssh.Close()
	}
}

// New returns a computer; call Start to connect.
func New(opts Options) *Computer {
	if opts.DialTimeout == 0 {
		opts.DialTimeout = 10 * time.Second
	}
	if opts.KeepAlive == 0 {
		opts.KeepAlive = 15 * time.Second
	}
	if opts.MinBackoff == 0 {
		opts.MinBackoff = time.Second
	}
	if opts.MaxBackoff == 0 {
		opts.MaxBackoff = 30 * time.Second
	}
	if opts.ParseKey == nil {
		opts.ParseKey = func(line string) (ssh.PublicKey, error) {
			k, _, err := pid.ParseLine(line)
			return k, err
		}
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	c := &Computer{
		opts:    opts,
		log:     log.With("server", opts.Addr),
		done:    make(chan struct{}),
		techies: make(map[string]string),
	}
	for k, v := range opts.KnownTechies {
		c.techies[k] = v
	}
	if opts.ServerHostKey != "" {
		if k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(opts.ServerHostKey)); err == nil {
			c.hostKey = k
		} else {
			c.log.Warn("pinned server host key is unreadable", "err", err)
			c.err = fmt.Errorf("%w: the pinned key in the configuration is unreadable", ErrHostKeyChanged)
		}
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	return c
}

// Start connects in the background. It does nothing on a second call.
func (c *Computer) Start() {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return
	}
	c.started = true
	broken := c.err != nil
	c.mu.Unlock()
	if broken {
		c.giveUp(c.err)
		close(c.done)
		return
	}
	go c.run()
}

// Close ends any session and disconnects. It does not wait; use Done.
func (c *Computer) Close() {
	c.cancel()
	c.mu.Lock()
	if !c.started {
		c.started = true
		close(c.done)
	}
	c.mu.Unlock()
}

// Done is closed when the computer stopped for good.
func (c *Computer) Done() <-chan struct{} { return c.done }

// State returns the current snapshot.
func (c *Computer) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stateLocked()
}

func (c *Computer) stateLocked() State {
	s := State{
		Err:       c.err,
		RequestID: c.reqID,
		Techie:    c.techie,
	}
	if c.hostKey != nil {
		s.ServerHostKey = identity.KeyLine(c.hostKey, "")
		s.ServerFingerprint = ssh.FingerprintSHA256(c.hostKey)
	}
	switch {
	case c.stopped:
		s.Phase = PhaseStopped
	case c.inSession:
		s.Phase = PhaseInSession
	case c.conn == nil:
		s.Phase = PhaseConnecting
	case c.pending:
		s.Phase = PhasePending
	case c.sessionBusy:
		s.Phase = PhaseAsking
	default:
		s.Phase = PhaseReady
	}
	return s
}

// notify sends the current snapshot to the handler. Call without c.mu held.
func (c *Computer) notify() {
	c.notifyMu.Lock()
	defer c.notifyMu.Unlock()
	c.opts.Handler.StateChanged(c.State())
}

// EndSession ends the running session, if any.
func (c *Computer) EndSession() {
	c.mu.Lock()
	end := c.endSession
	c.mu.Unlock()
	if end != nil {
		end()
	}
}

func (c *Computer) giveUp(err error) {
	c.mu.Lock()
	c.stopped = true
	c.err = err
	c.mu.Unlock()
	c.notify()
}

func (c *Computer) run() {
	defer close(c.done)
	backoff := c.opts.MinBackoff
	reachedOnce := false
	c.notify()
	for {
		reached, err := c.connectOnce()
		if c.ctx.Err() != nil {
			c.giveUp(nil)
			return
		}
		if reached {
			reachedOnce = true
			backoff = c.opts.MinBackoff
		}
		c.log.Info("server connection ended", "err", err, "connected", reached)
		switch k := Classify(err); {
		case k == KindHostKeyChanged, k == KindTechieKey,
			!reachedOnce && !c.opts.Retry:
			c.giveUp(err)
			return
		}
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		c.notify()

		// Full jitter in [backoff/2, backoff].
		d := backoff/2 + rand.N(backoff/2+1)
		select {
		case <-time.After(d):
		case <-c.ctx.Done():
			c.giveUp(nil)
			return
		}
		backoff = min(backoff*2, c.opts.MaxBackoff)
	}
}

// hostKeyCallback pins the server host key: the configured one, or the first
// one seen by this computer (kept in memory until Registered persists it).
func (c *Computer) hostKeyCallback(_ string, _ net.Addr, key ssh.PublicKey) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hostKey == nil {
		if fp := c.opts.ServerFingerprint; fp != "" && ssh.FingerprintSHA256(key) != fp {
			e := &HostKeyChangedError{
				OldFingerprint: fp,
				NewFingerprint: ssh.FingerprintSHA256(key),
				NewKeyLine:     identity.KeyLine(key, ""),
			}
			c.log.Error("server host key is not the approved one", "approved", fp, "got", e.NewFingerprint)
			return e
		}
		c.hostKey = key
		return nil
	}
	if !sameKey(c.hostKey, key) {
		e := &HostKeyChangedError{
			OldFingerprint: ssh.FingerprintSHA256(c.hostKey),
			NewFingerprint: ssh.FingerprintSHA256(key),
			NewKeyLine:     identity.KeyLine(key, ""),
		}
		c.log.Error("server host key changed", "pinned", e.OldFingerprint, "got", e.NewFingerprint)
		return e
	}
	return nil
}

func sameKey(a, b ssh.PublicKey) bool {
	return a.Type() == b.Type() && bytes.Equal(a.Marshal(), b.Marshal())
}

// hostKeyAlgorithms asks for the pinned key's algorithm, so that a server
// with several host keys always shows the pinned one.
func hostKeyAlgorithms(k ssh.PublicKey) []string {
	if k == nil {
		return nil
	}
	if k.Type() == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{k.Type()}
}

// connectOnce runs one server connection until it ends. reached reports
// whether the server answered with Hello.
func (c *Computer) connectOnce() (reached bool, err error) {
	dctx, dcancel := context.WithTimeout(c.ctx, c.opts.DialTimeout)
	var d net.Dialer
	tcp, err := d.DialContext(dctx, "tcp", c.opts.Addr)
	dcancel()
	if err != nil {
		return false, err
	}
	defer tcp.Close()
	stop := context.AfterFunc(c.ctx, func() { tcp.Close() })
	defer stop()
	if tc, ok := tcp.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	tcp.SetDeadline(time.Now().Add(c.opts.DialTimeout))

	c.mu.Lock()
	algos := hostKeyAlgorithms(c.hostKey)
	c.mu.Unlock()
	cfg := &ssh.ClientConfig{
		User:              control.SSHUser,
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(c.opts.Signer)},
		HostKeyCallback:   c.hostKeyCallback,
		HostKeyAlgorithms: algos,
		ClientVersion:     "SSH-2.0-helpbuoy-client",
		Timeout:           c.opts.DialTimeout,
	}
	sc, chans, reqs, err := ssh.NewClientConn(tcp, c.opts.Addr, cfg)
	if err != nil {
		return false, handshakeError(err)
	}
	computer := ssh.NewClient(sc, chans, reqs) // rejects channels the server opens
	defer computer.Close()

	ch, chReqs, err := computer.OpenChannel(control.ChannelControl, nil)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrNotServer, err)
	}
	go ssh.DiscardRequests(chReqs)
	ctl := control.NewConn(ch)
	if err := ctl.Send(control.ComputerHello{AppVersion: c.opts.AppVersion, UserMachine: c.opts.UserMachine}); err != nil {
		return false, err
	}
	m, err := ctl.Receive()
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return false, fmt.Errorf("%w: closed before Hello", ErrNotServer)
		}
		return false, err
	}
	var hello *control.Hello
	switch m := m.(type) {
	case *control.Hello:
		hello = m
	case *control.Error:
		return false, &ServerError{Message: m.Message}
	default:
		return false, fmt.Errorf("%w: first message is %T", ErrNotServer, m)
	}
	tcp.SetDeadline(time.Time{})
	c.log.Info("connected to server", "role", hello.Role, "server_version", hello.ServerVersion)

	cctx, ccancel := context.WithCancel(c.ctx)
	defer ccancel()
	rc := &serverConn{ssh: computer, ctl: ctl, ctx: cctx, cancel: ccancel, sendq: make(chan any, 16)}
	go c.sender(rc)
	go c.keepAlive(rc)
	context.AfterFunc(cctx, func() { computer.Close() })

	if err := c.connected(rc, hello); err != nil {
		return true, err
	}
	defer c.disconnected()
	for {
		m, err := ctl.Receive()
		if err != nil {
			c.mu.Lock()
			if c.err != nil && Classify(c.err) == KindServer {
				err = c.err // the server said why
			}
			c.mu.Unlock()
			return true, err
		}
		c.dispatch(rc, m)
	}
}

func handshakeError(err error) error {
	switch {
	case errors.Is(err, ErrHostKeyChanged):
		return err
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, net.ErrClosed):
		return err
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return err
	}
	if bytes.Contains([]byte(err.Error()), []byte("unable to authenticate")) {
		return fmt.Errorf("%w: %v", ErrRefusedKey, err)
	}
	return fmt.Errorf("%w: %v", ErrNotServer, err)
}

func (c *Computer) sender(rc *serverConn) {
	for {
		select {
		case m := <-rc.sendq:
			if err := rc.ctl.Send(m); err != nil {
				rc.cancel()
				return
			}
		case <-rc.ctx.Done():
			return
		}
	}
}

// keepAlive sends a global request every KeepAlive and drops the connection
// when three intervals pass without an answer.
func (c *Computer) keepAlive(rc *serverConn) {
	t := time.NewTicker(c.opts.KeepAlive)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-rc.ctx.Done():
			return
		}
		answered := make(chan error, 1)
		go func() {
			_, _, err := rc.ssh.SendRequest("keepalive@openssh.com", true, nil)
			answered <- err
		}()
		select {
		case err := <-answered:
			if err != nil {
				rc.cancel()
				return
			}
		case <-time.After(3 * c.opts.KeepAlive):
			c.log.Warn("server stopped answering keepalives")
			rc.cancel()
			return
		case <-rc.ctx.Done():
			return
		}
	}
}

func (c *Computer) connected(rc *serverConn, h *control.Hello) error {
	var registered bool
	c.mu.Lock()
	switch h.Role {
	case control.RoleTechie:
		c.mu.Unlock()
		return ErrTechieKey
	case control.RolePending:
		c.pending = true
	case control.RoleComputer:
		c.pending = false
		registered = true
	default:
		c.mu.Unlock()
		return fmt.Errorf("%w: unknown role %q", ErrNotServer, h.Role)
	}
	c.conn = rc
	c.err = nil
	hostKey := identity.KeyLine(c.hostKey, "")
	c.mu.Unlock()
	if registered {
		c.opts.Handler.Registered(hostKey)
		c.sendListing(rc)
	}
	c.notify()
	return nil
}

func (c *Computer) disconnected() {
	c.mu.Lock()
	c.conn = nil
	c.endRequestLocked()
	end := c.endSession
	c.mu.Unlock()
	if end != nil {
		end()
	}
	c.notify()
}

func (c *Computer) dispatch(rc *serverConn, m any) {
	switch m := m.(type) {
	case *control.Registered:
		c.mu.Lock()
		c.pending = false
		hostKey := identity.KeyLine(c.hostKey, "")
		c.mu.Unlock()
		c.log.Info("registered")
		c.opts.Handler.Registered(hostKey)
		c.sendListing(rc)

	case *control.RequestStateMsg:
		c.mu.Lock()
		if m.RequestID != "" && m.RequestID == c.reqID {
			c.log.Info("request ended by the server", "request", m.RequestID, "state", m.State)
			c.endRequestLocked()
		}
		c.mu.Unlock()

	case *control.ServerInfo:
		c.opts.Handler.ServerInfo(*m)
		return

	case *control.ApprovedServers:
		c.opts.Handler.ApprovedServers(m.Servers)
		return

	case *control.ListingRefused:
		c.log.Info("listing refused", "address", m.Address, "fingerprint", m.Fingerprint)
		c.opts.Handler.ListingRefused(control.ApprovedServer{Address: m.Address, Fingerprint: m.Fingerprint})
		return

	case *control.SessionReady:
		c.mu.Lock()
		busy := c.sessionBusy
		var ask context.Context
		if !busy {
			c.sessionBusy = true
			c.reqID = m.RequestID
			ask, c.reqEnded = context.WithCancel(rc.ctx)
		}
		c.mu.Unlock()
		if busy {
			c.log.Warn("SessionReady while a session runs; ignored", "request", m.RequestID)
			return
		}
		go c.runSession(rc, *m, ask)

	case *control.Error:
		c.log.Warn("server error", "message", m.Message)
		c.mu.Lock()
		c.err = &ServerError{Message: m.Message}
		c.mu.Unlock()
		return // the server closes the connection next

	default:
		c.log.Debug("ignored control message", "type", fmt.Sprintf("%T", m))
		return
	}
	c.notify()
}

// endRequestLocked forgets the current request and stops asking about it.
func (c *Computer) endRequestLocked() {
	c.reqID = ""
	if c.reqEnded != nil {
		c.reqEnded()
		c.reqEnded = nil
	}
}

// runSession asks the user about the techie and runs the session for one
// SessionReady. ask ends when the request ends.
func (c *Computer) runSession(rc *serverConn, sr control.SessionReady, ask context.Context) {
	log := c.log.With("request", sr.RequestID, "techie", sr.TechieName)
	abort := func(why string, err error) {
		log.Warn("session not started: "+why, "err", err)
		rc.send(control.CancelRequest{RequestID: sr.RequestID})
		c.mu.Lock()
		c.sessionBusy = false
		c.endRequestLocked()
		c.mu.Unlock()
		c.notify()
	}
	if c.opts.Runner == nil {
		abort("no session runner", nil)
		return
	}
	key, err := c.opts.ParseKey(sr.TechieKey)
	if err != nil {
		abort("unusable techie key", err)
		return
	}
	fp := ssh.FingerprintSHA256(key)
	line := identity.KeyLine(key, "")

	c.mu.Lock()
	old := c.techies[sr.TechieName]
	c.mu.Unlock()
	pin := old == ""
	oldFP := ""
	if old != "" {
		oldKey, _, _, _, perr := ssh.ParseAuthorizedKey([]byte(old))
		if perr != nil || !sameKey(oldKey, key) {
			oldFP = "unreadable"
			if perr == nil {
				oldFP = ssh.FingerprintSHA256(oldKey)
			}
			log.Warn("techie key changed", "old", oldFP, "new", fp)
			pin = true
		}
	}
	techie := Techie{Name: sr.TechieName, Fingerprint: fp}
	if !c.opts.Handler.ConfirmTechie(ask, techie, oldFP) {
		abort("user refused the techie", nil)
		return
	}
	if ask.Err() != nil {
		abort("the request ended while the user was asked", nil)
		return
	}
	if pin {
		c.mu.Lock()
		c.techies[sr.TechieName] = line
		c.mu.Unlock()
		c.opts.Handler.TechiePinned(sr.TechieName, line)
	}

	ctx, cancel := context.WithCancel(rc.ctx)
	defer cancel()
	ch, reqs, err := rc.ssh.OpenChannel(control.ChannelSession,
		control.MarshalSessionChannelData(control.SessionChannelData{RequestID: sr.RequestID}))
	if err != nil {
		abort("server refused the session channel", err)
		return
	}
	go ssh.DiscardRequests(reqs)

	c.mu.Lock()
	c.inSession = true
	c.techie = techie
	c.endSession = cancel
	c.mu.Unlock()
	c.notify()
	log.Info("session started", "fingerprint", fp)

	stopHook := func() {}
	if c.opts.EscHook != nil {
		stopHook = c.opts.EscHook(func() {
			log.Info("local Esc")
			cancel()
		})
	}
	err = c.opts.Runner.Run(ctx, ch, key)
	stopHook()
	ch.Close()
	log.Info("session ended", "err", err)

	c.mu.Lock()
	c.inSession = false
	c.sessionBusy = false
	c.endSession = nil
	c.endRequestLocked()
	c.mu.Unlock()
	c.notify()
}

// sendListing sends the listing request of Options.Listing, if any.
func (c *Computer) sendListing(rc *serverConn) {
	if l := c.opts.Listing; l != nil {
		c.log.Info("asking the server to approve another server", "address", l.Address, "fingerprint", l.Fingerprint)
		rc.send(*l)
	}
}

// ErrProbed ends the handshake of ProbeHostKey once the host key is known.
var errProbed = errors.New("host key seen")

// ProbeHostKey connects to the server at addr only far enough to see its
// host key (PLAN.md 12.3): it ends the SSH handshake right after the server
// showed its key, before authenticating, so nothing else happens with that
// server.
func ProbeHostKey(ctx context.Context, addr string, timeout time.Duration) (ssh.PublicKey, error) {
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var d net.Dialer
	tcp, err := d.DialContext(dctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer tcp.Close()
	stop := context.AfterFunc(dctx, func() { tcp.Close() })
	defer stop()
	tcp.SetDeadline(time.Now().Add(timeout))
	var key ssh.PublicKey
	cfg := &ssh.ClientConfig{
		User: control.SSHUser,
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
			key = k
			return errProbed
		},
		ClientVersion: "SSH-2.0-helpbuoy-client",
		Timeout:       timeout,
	}
	_, _, _, err = ssh.NewClientConn(tcp, addr, cfg)
	if key != nil {
		return key, nil
	}
	if dctx.Err() != nil && ctx.Err() == nil {
		return nil, context.DeadlineExceeded
	}
	if err == nil {
		err = ErrNotServer
	}
	return nil, handshakeError(err)
}
