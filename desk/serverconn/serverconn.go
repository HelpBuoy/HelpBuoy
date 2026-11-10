// Package serverconn keeps the desk app's one SSH connection to the server:
// login with the app key, TOFU check of the server host key, the sv-control
// channel (Hello, Presence, commands and their Replies), incoming sv-session
// channels, keepalive, and reconnecting with backoff.
//
// A Conn runs in its own goroutines. The UI reads Events once per frame and
// sends commands, whose results arrive on a channel per command.
package serverconn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/desk/config"
	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/proto/i18n"
)

// Status is the state of the server connection as shown in the UI.
type Status int

const (
	StatusConnecting      Status = iota // dialing, or waiting to retry after a lost connection
	StatusOnline                        // logged in as a techie
	StatusAuthFailed                    // the server does not know this key as a techie
	StatusUnreachable                   // nothing answered, or it is not an helpbuoy-server
	StatusIdentityChanged               // the server's host key differs from the pinned one
	StatusReplaced                      // a newer connection with this key took over
	StatusClosed                        // Close was called
)

func (s Status) String() string {
	switch s {
	case StatusConnecting:
		return "connecting"
	case StatusOnline:
		return "online"
	case StatusAuthFailed:
		return "auth failed"
	case StatusUnreachable:
		return "unreachable"
	case StatusIdentityChanged:
		return "server identity changed"
	case StatusReplaced:
		return "replaced"
	case StatusClosed:
		return "closed"
	}
	return fmt.Sprintf("Status(%d)", int(s))
}

// Errors reported by the connection and by commands.
var (
	ErrNotTechie    = errors.New(i18n.Trans("this key is not a techie on the server"))
	ErrNotConnected = errors.New(i18n.Trans("not connected to the server"))
	ErrTimeout      = errors.New(i18n.Trans("the server did not answer"))
	ErrReplaced     = errors.New(i18n.Trans("this key connected to the server from another desk app"))
)

// Options configure a Conn.
type Options struct {
	Addr   string     // host:port to dial
	Signer ssh.Signer // the techie's app key
	// HostKey is the pinned server host key as an authorized_keys line, or ""
	// to pin the key of the first successful connection.
	HostKey string

	KeepAlive        time.Duration // default 15 s; the connection is dropped after 3 missed
	HandshakeTimeout time.Duration // default 10 s, for TCP connect, SSH handshake and Hello
	CommandTimeout   time.Duration // default 30 s, for the Reply to a command
	MinBackoff       time.Duration // default 1 s
	MaxBackoff       time.Duration // default 60 s

	// Dial overrides the TCP dialer, for tests.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (o *Options) defaults() {
	set := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	set(&o.KeepAlive, 15*time.Second)
	set(&o.HandshakeTimeout, 10*time.Second)
	set(&o.CommandTimeout, 30*time.Second)
	set(&o.MinBackoff, time.Second)
	set(&o.MaxBackoff, 60*time.Second)
	if o.Dial == nil {
		o.Dial = (&net.Dialer{}).DialContext
	}
}

// Event is one of StatusEvent, HostKeyPinned, PresenceEvent, ServerInfoEvent,
// SessionEvent.
type Event any

// StatusEvent reports a change of the connection status.
type StatusEvent struct {
	Status  Status
	Err     error          // why the connection is not online, if known
	Hello   *control.Hello // set when online
	HostKey ssh.PublicKey  // the server's host key, once known
}

// HostKeyPinned reports that the server host key was seen for the first time
// and is now pinned; the app must store it.
type HostKeyPinned struct {
	Key ssh.PublicKey
}

// PresenceEvent carries a full Presence snapshot.
type PresenceEvent struct {
	Presence *control.Presence
}

// ServerInfoEvent carries who runs the server.
type ServerInfoEvent struct {
	Info *control.ServerInfo
}

// SessionEvent hands over an accepted sv-session channel. The receiver owns
// Channel and must close it, also when it rejects the session.
type SessionEvent struct {
	Data    control.TechiesessionData
	Channel ssh.Channel
}

// Conn is the connection to one server.
type Conn struct {
	opts   Options
	events chan Event
	ctx    context.Context
	cancel context.CancelFunc
	kick   chan struct{}
	done   chan struct{}
	rtt    atomic.Int64

	mu      sync.Mutex
	cc      *control.Conn
	seq     uint32
	pending map[uint32]chan error

	pinned ssh.PublicKey // used by the run goroutine only
}

// Start begins connecting to the server in the background.
func Start(opts Options) *Conn {
	opts.defaults()
	ctx, cancel := context.WithCancel(context.Background())
	c := &Conn{
		opts:    opts,
		events:  make(chan Event, 64),
		ctx:     ctx,
		cancel:  cancel,
		kick:    make(chan struct{}, 1),
		done:    make(chan struct{}),
		pending: map[uint32]chan error{},
	}
	if opts.HostKey != "" {
		if k, err := config.ParseHostKey(opts.HostKey); err == nil {
			c.pinned = k
		}
	}
	go c.run()
	return c
}

// Events returns the event stream. It is never closed; drain it every frame.
func (c *Conn) Events() <-chan Event { return c.events }

// RTT returns the round trip time of the last keepalive, or 0.
func (c *Conn) RTT() time.Duration { return time.Duration(c.rtt.Load()) }

// Reconnect makes a waiting connection retry now.
func (c *Conn) Reconnect() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// Close ends the connection, all its session channels with it, and waits for
// the goroutines to stop.
func (c *Conn) Close() {
	c.cancel()
	<-c.done
}

// RegisterTechie adds another techie key under name.
func (c *Conn) RegisterTechie(line, name string) <-chan error {
	return c.command(func(seq uint32) any {
		return &control.RegisterTechie{Seq: seq, PublicKey: line, Name: name}
	})
}

// UnregisterTechie removes the techie with the given name.
func (c *Conn) UnregisterTechie(name string) <-chan error {
	return c.command(func(seq uint32) any {
		return &control.UnregisterTechie{Seq: seq, Name: name}
	})
}

// RegisterComputer registers a computer key line; name overrides the comment.
func (c *Conn) RegisterComputer(line, name string) <-chan error {
	return c.command(func(seq uint32) any {
		return &control.RegisterComputer{Seq: seq, PublicKey: line, Name: name}
	})
}

// RenameComputer changes the name techies see for the computer with the given
// fingerprint.
func (c *Conn) RenameComputer(fingerprint, name string) <-chan error {
	return c.command(func(seq uint32) any {
		return &control.RenameComputer{Seq: seq, Fingerprint: fingerprint, Name: name}
	})
}

// UnregisterComputer removes the computer with the given fingerprint.
func (c *Conn) UnregisterComputer(fingerprint string) <-chan error {
	return c.command(func(seq uint32) any {
		return &control.UnregisterComputer{Seq: seq, Fingerprint: fingerprint}
	})
}

// SetServerInfo replaces who runs the server: its address, company name,
// phone number and description.
func (c *Conn) SetServerInfo(info control.ServerInfo) <-chan error {
	return c.command(func(seq uint32) any {
		return &control.SetServerInfo{Seq: seq, Address: info.Address, Company: info.Company, Phone: info.Phone, Description: info.Description}
	})
}

// DecideListing allows or refuses the open listing request req of the
// computer with the given fingerprint (PLAN.md 12.3).
func (c *Conn) DecideListing(computer string, req control.ListingRequest, allow bool) <-chan error {
	return c.command(func(seq uint32) any {
		return &control.DecideListing{Seq: seq, Computer: computer, Address: req.Address, Fingerprint: req.Fingerprint, Allow: allow}
	})
}

// Connect asks the computer with the given fingerprint to allow a session.
// requestID comes from control.NewRequestID; the session channel that
// follows when the user allows it carries the same id.
func (c *Conn) Connect(fingerprint, requestID string) <-chan error {
	return c.command(func(seq uint32) any {
		return &control.Connect{Seq: seq, Fingerprint: fingerprint, RequestID: requestID}
	})
}

// CancelRequest stops waiting for the user to allow the Connect with
// requestID. The server sends no reply.
func (c *Conn) CancelRequest(requestID string) {
	c.mu.Lock()
	cc := c.cc
	c.mu.Unlock()
	if cc != nil {
		cc.Send(&control.CancelRequest{RequestID: requestID})
	}
}

// command sends a message with the next Seq. The returned channel receives
// exactly one value: nil, the server's error, ErrNotConnected or ErrTimeout.
func (c *Conn) command(build func(seq uint32) any) <-chan error {
	res := make(chan error, 1)
	c.mu.Lock()
	cc := c.cc
	if cc == nil {
		c.mu.Unlock()
		res <- ErrNotConnected
		return res
	}
	c.seq++
	seq := c.seq
	c.pending[seq] = res
	c.mu.Unlock()

	if err := cc.Send(build(seq)); err != nil {
		c.resolve(seq, ErrNotConnected)
		return res
	}
	time.AfterFunc(c.opts.CommandTimeout, func() { c.resolve(seq, ErrTimeout) })
	return res
}

// resolve delivers the result of command seq once.
func (c *Conn) resolve(seq uint32, err error) {
	c.mu.Lock()
	res, ok := c.pending[seq]
	delete(c.pending, seq)
	c.mu.Unlock()
	if ok {
		res <- err
	}
}

// emit delivers an event unless the Conn is closing.
func (c *Conn) emit(ev Event) bool {
	select {
	case c.events <- ev:
		return true
	case <-c.ctx.Done():
		return false
	}
}

func (c *Conn) run() {
	defer close(c.done)
	backoff := c.opts.MinBackoff
	for {
		c.emit(StatusEvent{Status: StatusConnecting, HostKey: c.pinned})
		start := time.Now()
		online, err := c.connect()
		if c.ctx.Err() != nil {
			c.closed()
			return
		}
		st := classify(online, err)
		c.emit(StatusEvent{Status: st, Err: err, HostKey: c.pinned})
		if online && time.Since(start) > 30*time.Second {
			backoff = c.opts.MinBackoff
		}
		wait := backoff
		if st == StatusIdentityChanged || st == StatusReplaced {
			// Only Reconnect retries: a hard error, or another desk app
			// with this key, which retrying would only push off again.
			wait = 365 * 24 * time.Hour
		}
		select {
		case <-c.ctx.Done():
			c.closed()
			return
		case <-c.kick:
			backoff = c.opts.MinBackoff
		case <-time.After(wait):
			backoff = min(backoff*2, c.opts.MaxBackoff)
		}
	}
}

// closed reports StatusClosed if there is room; nobody may be reading anymore.
func (c *Conn) closed() {
	select {
	case c.events <- StatusEvent{Status: StatusClosed}:
	default:
	}
}

// classify maps the end of a connection attempt to the status shown while
// waiting for the next one.
func classify(online bool, err error) Status {
	switch {
	case errors.Is(err, config.ErrServerIdentityChanged):
		return StatusIdentityChanged
	case errors.Is(err, ErrNotTechie):
		return StatusAuthFailed
	case errors.Is(err, ErrReplaced):
		return StatusReplaced
	case online:
		return StatusConnecting // was online; retrying
	case err != nil && strings.Contains(err.Error(), "unable to authenticate"):
		return StatusAuthFailed
	}
	return StatusUnreachable
}

// connect runs one connection until it ends. online reports whether it got
// as far as a techie Hello.
func (c *Conn) connect() (online bool, err error) {
	ctx, cancel := context.WithTimeout(c.ctx, c.opts.HandshakeTimeout)
	nc, err := c.opts.Dial(ctx, "tcp", c.opts.Addr)
	cancel()
	if err != nil {
		return false, err
	}
	if tc, ok := nc.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}

	var seen ssh.PublicKey
	var mismatch bool
	cfg := &ssh.ClientConfig{
		User: control.SSHUser,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(c.opts.Signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			seen = key
			if c.pinned == nil {
				return nil
			}
			if err := config.CheckPinned(config.HostKeyLine(c.pinned), key); err != nil {
				mismatch = true
				return err
			}
			return nil
		},
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		Timeout:           c.opts.HandshakeTimeout,
	}
	nc.SetDeadline(time.Now().Add(c.opts.HandshakeTimeout))
	stop := context.AfterFunc(c.ctx, func() { nc.Close() })
	defer stop()
	sc, chans, reqs, err := ssh.NewClientConn(nc, c.opts.Addr, cfg)
	if err != nil {
		nc.Close()
		if mismatch {
			return false, config.ErrServerIdentityChanged
		}
		return false, err
	}
	nc.SetDeadline(time.Time{})
	defer sc.Close()
	stop()
	stop = context.AfterFunc(c.ctx, func() { sc.Close() })
	defer stop()

	if c.pinned == nil && seen != nil {
		c.pinned = seen
		c.emit(HostKeyPinned{Key: seen})
	}

	go ssh.DiscardRequests(reqs)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.acceptSessions(chans)
	}()
	defer func() {
		sc.Close() // ends acceptSessions
		wg.Wait()
	}()

	ch, chReqs, err := sc.OpenChannel(control.ChannelControl, nil)
	if err != nil {
		return false, fmt.Errorf("open control channel: %w", err)
	}
	go ssh.DiscardRequests(chReqs)
	cc := control.NewConn(ch)

	timer := time.AfterFunc(c.opts.HandshakeTimeout, func() { sc.Close() })
	first, err := cc.Receive()
	timer.Stop()
	if err != nil {
		return false, fmt.Errorf("waiting for the server's hello: %w", err)
	}
	var hello *control.Hello
	switch m := first.(type) {
	case *control.Hello:
		hello = m
	case *control.Error:
		return false, serverError(m)
	default:
		return false, fmt.Errorf("server sent %T before hello", first)
	}
	if hello.Role != control.RoleTechie {
		return false, ErrNotTechie
	}

	c.mu.Lock()
	c.cc = cc
	c.mu.Unlock()
	defer c.disconnect()

	c.emit(StatusEvent{Status: StatusOnline, Hello: hello, HostKey: c.pinned})

	kaDone := make(chan struct{})
	defer close(kaDone)
	go c.keepalive(sc, kaDone)

	for {
		m, err := cc.Receive()
		if err != nil {
			if c.ctx.Err() != nil {
				return true, c.ctx.Err()
			}
			return true, fmt.Errorf("connection to the server lost: %w", err)
		}
		switch m := m.(type) {
		case *control.Presence:
			c.emit(PresenceEvent{Presence: m})
		case *control.ServerInfo:
			c.emit(ServerInfoEvent{Info: m})
		case *control.Reply:
			var rerr error
			if m.Error != "" {
				rerr = errors.New(m.Error)
			}
			c.resolve(m.Seq, rerr)
		case *control.Error:
			return true, serverError(m)
		}
	}
}

// serverError is the error for an Error message from the server.
func serverError(m *control.Error) error {
	if m.Message == control.ReasonReplaced {
		return ErrReplaced
	}
	return fmt.Errorf("server: %s", m.Message)
}

// disconnect forgets the control conn and fails the outstanding commands.
func (c *Conn) disconnect() {
	c.mu.Lock()
	c.cc = nil
	p := c.pending
	c.pending = map[uint32]chan error{}
	c.mu.Unlock()
	for _, res := range p {
		res <- ErrNotConnected
	}
}

// acceptSessions accepts the server's sv-session channels and rejects every
// other channel type.
func (c *Conn) acceptSessions(chans <-chan ssh.NewChannel) {
	for nc := range chans {
		if nc.ChannelType() != control.ChannelSession {
			nc.Reject(ssh.UnknownChannelType, "unsupported channel type")
			continue
		}
		d, err := control.UnmarshalTechiesessionData(nc.ExtraData())
		if err != nil {
			nc.Reject(ssh.Prohibited, "bad session data")
			continue
		}
		ch, reqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go ssh.DiscardRequests(reqs)
		if !c.emit(SessionEvent{Data: d, Channel: ch}) {
			ch.Close()
		}
	}
}

// keepalive sends a global request every KeepAlive interval and closes the
// connection when no answer arrives within three intervals.
func (c *Conn) keepalive(sc ssh.Conn, done <-chan struct{}) {
	t := time.NewTicker(c.opts.KeepAlive)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		start := time.Now()
		res := make(chan error, 1)
		go func() {
			_, _, err := sc.SendRequest("keepalive@openssh.com", true, nil)
			res <- err
		}()
		select {
		case <-done:
			return
		case err := <-res:
			if err != nil {
				sc.Close()
				return
			}
			c.rtt.Store(int64(time.Since(start)))
		case <-time.After(3 * c.opts.KeepAlive):
			sc.Close()
			return
		}
	}
}
