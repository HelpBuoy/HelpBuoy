// Package session keeps the client's connection to the relay: SSH with the
// app key, the sv-control channel, help requests, and the session channel
// with the inner SSH server once a helper opened the request (PLAN.md 1, 3.3,
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

	"github.com/mevdschee/helpbuoy/client-windows/identity"
	"github.com/mevdschee/helpbuoy/proto/control"
	pid "github.com/mevdschee/helpbuoy/proto/identity"
)

// Phase is what the client is doing, as far as the window cares.
type Phase int

// Phases, derived from the connection, request and session state.
const (
	PhaseStopped    Phase = iota // not started, closed, or given up (see State.Err)
	PhaseConnecting              // dialing, or waiting before the next attempt
	PhasePending                 // connected, key not registered yet
	PhaseReady                   // registered and connected, waiting for a helper
	PhaseAsking                  // a helper wants to connect; the user is asked, then the session starts
	PhaseInSession               // the session runs
)

func (p Phase) String() string {
	return [...]string{"stopped", "connecting", "pending", "ready", "asking", "in-session"}[p]
}

// Helper describes the helper of the current or last session.
type Helper struct {
	Name        string
	Fingerprint string
}

// State is a snapshot of the client for the window.
type State struct {
	Phase Phase
	// Err is the last connection error. It is cleared once connected.
	Err error
	// RelayHostKey is the relay host key line (pinned, or seen on this
	// first connection), RelayFingerprint its fingerprint.
	RelayHostKey     string
	RelayFingerprint string
	// RequestID of the helper's connect being asked about or running.
	RequestID string
	// Helper of the running session, or of the last one.
	Helper Helper
}

// Handler receives events from the client. Its methods are called from the
// client's goroutines; StateChanged, Registered and HelperPinned must return
// quickly. ConfirmHelper may block while the user decides.
type Handler interface {
	// StateChanged reports a new snapshot. Snapshots arrive in order.
	StateChanged(State)
	// Registered reports that the relay knows this key as a client, and
	// the relay host key line to pin from now on.
	Registered(relayHostKey string)
	// HelperPinned reports a helper key to remember for name.
	HelperPinned(name, keyLine string)
	// ConfirmHelper asks the user whether helper h may take control. It is
	// called for every session, before the session channel is opened.
	// oldFingerprint is set when the helper called h.Name showed a different
	// key last time ("unreadable" if the pinned key can't be parsed). ctx
	// ends when the question is moot (the relay ended the request, or the
	// connection was lost); the implementation should then stop asking.
	ConfirmHelper(ctx context.Context, h Helper, oldFingerprint string) bool
}

// Runner runs one session on the client's sv-session channel: the inner SSH
// server and the screen and input streams. It returns when the session ends
// or ctx is cancelled (local Esc, window closed, relay connection lost).
type Runner interface {
	Run(ctx context.Context, ch io.ReadWriteCloser, helperKey ssh.PublicKey) error
}

// Options configure a Client. Addr, Signer and Handler are required.
type Options struct {
	Addr        string     // relay host:port, already normalized
	Signer      ssh.Signer // the app key
	AppVersion  string
	UserMachine string // current user@machine for ClientHello

	// RelayHostKey is the pinned relay host key line; empty means trust on
	// first use (the first-run window).
	RelayHostKey string
	// KnownHelpers maps helper names to pinned key lines.
	KnownHelpers map[string]string
	// Retry keeps retrying when the very first attempt fails. Without it the
	// client gives up after a failed first attempt, so the first-run window
	// can show the error; after one successful connection it always retries.
	Retry bool

	Handler Handler
	// Runner runs sessions; nil refuses every session.
	Runner Runner
	// EscHook, if set, is called when a session starts. It must arrange for
	// onEsc to be called when the user presses Esc on the local keyboard and
	// return a function that removes that hook again.
	EscHook func(onEsc func()) (stop func())
	// ParseKey parses a helper key line; default proto/identity.ParseLine.
	ParseKey func(line string) (ssh.PublicKey, error)

	DialTimeout time.Duration // TCP connect, SSH handshake and Hello; default 10 s
	KeepAlive   time.Duration // default 15 s; the connection is dropped after 3 missed
	MinBackoff  time.Duration // default 1 s
	MaxBackoff  time.Duration // default 30 s
	Logger      *slog.Logger
}

// Client is one relay connection with automatic reconnects.
type Client struct {
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
	helpers     map[string]string
	conn        *relayConn // nil while disconnected
	pending     bool
	reqID       string             // request of the SessionReady being handled, "" once it ended
	reqEnded    context.CancelFunc // cancels the ConfirmHelper context
	sessionBusy bool               // SessionReady accepted, until the session ended
	inSession   bool
	helper      Helper
	endSession  context.CancelFunc
}

// relayConn is one established relay connection.
type relayConn struct {
	ssh    *ssh.Client
	ctl    *control.Conn
	ctx    context.Context // cancelled when the connection ends
	sendq  chan any
	cancel context.CancelFunc
}

// send queues a control message. It never blocks; a full queue means the
// relay stopped reading, so the connection is dropped.
func (rc *relayConn) send(m any) {
	select {
	case rc.sendq <- m:
	default:
		rc.ssh.Close()
	}
}

// New returns a client; call Start to connect.
func New(opts Options) *Client {
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
	c := &Client{
		opts:    opts,
		log:     log.With("relay", opts.Addr),
		done:    make(chan struct{}),
		helpers: make(map[string]string),
	}
	for k, v := range opts.KnownHelpers {
		c.helpers[k] = v
	}
	if opts.RelayHostKey != "" {
		if k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(opts.RelayHostKey)); err == nil {
			c.hostKey = k
		} else {
			c.log.Warn("pinned relay host key is unreadable", "err", err)
			c.err = fmt.Errorf("%w: the pinned key in the configuration is unreadable", ErrHostKeyChanged)
		}
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	return c
}

// Start connects in the background. It does nothing on a second call.
func (c *Client) Start() {
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
func (c *Client) Close() {
	c.cancel()
	c.mu.Lock()
	if !c.started {
		c.started = true
		close(c.done)
	}
	c.mu.Unlock()
}

// Done is closed when the client stopped for good.
func (c *Client) Done() <-chan struct{} { return c.done }

// State returns the current snapshot.
func (c *Client) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stateLocked()
}

func (c *Client) stateLocked() State {
	s := State{
		Err:       c.err,
		RequestID: c.reqID,
		Helper:    c.helper,
	}
	if c.hostKey != nil {
		s.RelayHostKey = identity.KeyLine(c.hostKey, "")
		s.RelayFingerprint = ssh.FingerprintSHA256(c.hostKey)
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
func (c *Client) notify() {
	c.notifyMu.Lock()
	defer c.notifyMu.Unlock()
	c.opts.Handler.StateChanged(c.State())
}

// EndSession ends the running session, if any.
func (c *Client) EndSession() {
	c.mu.Lock()
	end := c.endSession
	c.mu.Unlock()
	if end != nil {
		end()
	}
}

func (c *Client) giveUp(err error) {
	c.mu.Lock()
	c.stopped = true
	c.err = err
	c.mu.Unlock()
	c.notify()
}

func (c *Client) run() {
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
		c.log.Info("relay connection ended", "err", err, "connected", reached)
		switch k := Classify(err); {
		case k == KindHostKeyChanged, k == KindHelperKey,
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

// hostKeyCallback pins the relay host key: the configured one, or the first
// one seen by this client (kept in memory until Registered persists it).
func (c *Client) hostKeyCallback(_ string, _ net.Addr, key ssh.PublicKey) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hostKey == nil {
		c.hostKey = key
		return nil
	}
	if !sameKey(c.hostKey, key) {
		e := &HostKeyChangedError{
			OldFingerprint: ssh.FingerprintSHA256(c.hostKey),
			NewFingerprint: ssh.FingerprintSHA256(key),
			NewKeyLine:     identity.KeyLine(key, ""),
		}
		c.log.Error("relay host key changed", "pinned", e.OldFingerprint, "got", e.NewFingerprint)
		return e
	}
	return nil
}

func sameKey(a, b ssh.PublicKey) bool {
	return a.Type() == b.Type() && bytes.Equal(a.Marshal(), b.Marshal())
}

// hostKeyAlgorithms asks for the pinned key's algorithm, so that a relay
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

// connectOnce runs one relay connection until it ends. reached reports
// whether the relay answered with Hello.
func (c *Client) connectOnce() (reached bool, err error) {
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
	client := ssh.NewClient(sc, chans, reqs) // rejects channels the relay opens
	defer client.Close()

	ch, chReqs, err := client.OpenChannel(control.ChannelControl, nil)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrNotRelay, err)
	}
	go ssh.DiscardRequests(chReqs)
	ctl := control.NewConn(ch)
	if err := ctl.Send(control.ClientHello{AppVersion: c.opts.AppVersion, UserMachine: c.opts.UserMachine}); err != nil {
		return false, err
	}
	m, err := ctl.Receive()
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return false, fmt.Errorf("%w: closed before Hello", ErrNotRelay)
		}
		return false, err
	}
	var hello *control.Hello
	switch m := m.(type) {
	case *control.Hello:
		hello = m
	case *control.Error:
		return false, &RelayError{Message: m.Message}
	default:
		return false, fmt.Errorf("%w: first message is %T", ErrNotRelay, m)
	}
	tcp.SetDeadline(time.Time{})
	c.log.Info("connected to relay", "role", hello.Role, "relay_version", hello.RelayVersion)

	cctx, ccancel := context.WithCancel(c.ctx)
	defer ccancel()
	rc := &relayConn{ssh: client, ctl: ctl, ctx: cctx, cancel: ccancel, sendq: make(chan any, 16)}
	go c.sender(rc)
	go c.keepAlive(rc)
	context.AfterFunc(cctx, func() { client.Close() })

	if err := c.connected(rc, hello); err != nil {
		return true, err
	}
	defer c.disconnected()
	for {
		m, err := ctl.Receive()
		if err != nil {
			c.mu.Lock()
			if c.err != nil && Classify(c.err) == KindRelay {
				err = c.err // the relay said why
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
	return fmt.Errorf("%w: %v", ErrNotRelay, err)
}

func (c *Client) sender(rc *relayConn) {
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
func (c *Client) keepAlive(rc *relayConn) {
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
			c.log.Warn("relay stopped answering keepalives")
			rc.cancel()
			return
		case <-rc.ctx.Done():
			return
		}
	}
}

func (c *Client) connected(rc *relayConn, h *control.Hello) error {
	var registered bool
	c.mu.Lock()
	switch h.Role {
	case control.RoleHelper:
		c.mu.Unlock()
		return ErrHelperKey
	case control.RolePending:
		c.pending = true
	case control.RoleClient:
		c.pending = false
		registered = true
	default:
		c.mu.Unlock()
		return fmt.Errorf("%w: unknown role %q", ErrNotRelay, h.Role)
	}
	c.conn = rc
	c.err = nil
	hostKey := identity.KeyLine(c.hostKey, "")
	c.mu.Unlock()
	if registered {
		c.opts.Handler.Registered(hostKey)
	}
	c.notify()
	return nil
}

func (c *Client) disconnected() {
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

func (c *Client) dispatch(rc *relayConn, m any) {
	switch m := m.(type) {
	case *control.Registered:
		c.mu.Lock()
		c.pending = false
		hostKey := identity.KeyLine(c.hostKey, "")
		c.mu.Unlock()
		c.log.Info("registered")
		c.opts.Handler.Registered(hostKey)

	case *control.RequestStateMsg:
		c.mu.Lock()
		if m.RequestID != "" && m.RequestID == c.reqID {
			c.log.Info("request ended by the relay", "request", m.RequestID, "state", m.State)
			c.endRequestLocked()
		}
		c.mu.Unlock()

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
		c.log.Warn("relay error", "message", m.Message)
		c.mu.Lock()
		c.err = &RelayError{Message: m.Message}
		c.mu.Unlock()
		return // the relay closes the connection next

	default:
		c.log.Debug("ignored control message", "type", fmt.Sprintf("%T", m))
		return
	}
	c.notify()
}

// endRequestLocked forgets the current request and stops asking about it.
func (c *Client) endRequestLocked() {
	c.reqID = ""
	if c.reqEnded != nil {
		c.reqEnded()
		c.reqEnded = nil
	}
}

// runSession asks the user about the helper and runs the session for one
// SessionReady. ask ends when the request ends.
func (c *Client) runSession(rc *relayConn, sr control.SessionReady, ask context.Context) {
	log := c.log.With("request", sr.RequestID, "helper", sr.HelperName)
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
	key, err := c.opts.ParseKey(sr.HelperKey)
	if err != nil {
		abort("unusable helper key", err)
		return
	}
	fp := ssh.FingerprintSHA256(key)
	line := identity.KeyLine(key, "")

	c.mu.Lock()
	old := c.helpers[sr.HelperName]
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
			log.Warn("helper key changed", "old", oldFP, "new", fp)
			pin = true
		}
	}
	helper := Helper{Name: sr.HelperName, Fingerprint: fp}
	if !c.opts.Handler.ConfirmHelper(ask, helper, oldFP) {
		abort("user refused the helper", nil)
		return
	}
	if ask.Err() != nil {
		abort("the request ended while the user was asked", nil)
		return
	}
	if pin {
		c.mu.Lock()
		c.helpers[sr.HelperName] = line
		c.mu.Unlock()
		c.opts.Handler.HelperPinned(sr.HelperName, line)
	}

	ctx, cancel := context.WithCancel(rc.ctx)
	defer cancel()
	ch, reqs, err := rc.ssh.OpenChannel(control.ChannelSession,
		control.MarshalSessionChannelData(control.SessionChannelData{RequestID: sr.RequestID}))
	if err != nil {
		abort("relay refused the session channel", err)
		return
	}
	go ssh.DiscardRequests(reqs)

	c.mu.Lock()
	c.inSession = true
	c.helper = helper
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
