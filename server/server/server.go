// Package server is the helpbuoy-server SSH server. It ties the host key, the state
// store, key-based roles, the registry, the limits and the splice together:
// it accepts TCP connections, runs the SSH handshake, assigns each proven key
// its role, serves the sv-control channel and pairs sv-session channels.
//
// Everything is denied by default: only the global request
// keepalive@openssh.com is answered, only the channel types from
// proto/control are accepted, and only by the roles allowed to open them.
package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/proto/wire"
	"github.com/helpbuoy/helpbuoy/server/auth"
	"github.com/helpbuoy/helpbuoy/server/hostkey"
	"github.com/helpbuoy/helpbuoy/server/limits"
	"github.com/helpbuoy/helpbuoy/server/registry"
	"github.com/helpbuoy/helpbuoy/server/salt"
	"github.com/helpbuoy/helpbuoy/server/splice"
	"github.com/helpbuoy/helpbuoy/server/store"
)

// KeepaliveRequest is the only global request the server answers, and the one
// it sends to check that a peer is alive.
const KeepaliveRequest = "keepalive@openssh.com"

// Config configures a Server. Zero values take the PLAN 3.4 defaults.
type Config struct {
	StateDir string       // holds hostkey_ed25519, state.json
	Version  string       // reported in Hello
	Log      *slog.Logger // JSON log; nil discards
	Now      func() time.Time
	Limits   limits.Config

	HandshakeTimeout   time.Duration // 10 s; also the time allowed to open the control channel
	KeepaliveInterval  time.Duration // 15 s
	KeepaliveMissed    int           // 3
	SessionOpenTimeout time.Duration // time for a techie to accept the session channel, 30 s
	PresenceInterval   time.Duration // minimum time between Presence snapshots to one techie, 100 ms

	SessionStartTimeout  time.Duration // 1 min
	PendingIdle          time.Duration // 24 h
	MaxSessionsPerTechie int           // 20

	ReloadInterval time.Duration // state.json poll, 1 s
	SweepInterval  time.Duration // expiry checks, 1 s
	LastSeenFlush  time.Duration // last-seen persistence, 5 min
}

func (c *Config) fill() {
	setD := func(v *time.Duration, d time.Duration) {
		if *v == 0 {
			*v = d
		}
	}
	setD(&c.HandshakeTimeout, 10*time.Second)
	setD(&c.KeepaliveInterval, 15*time.Second)
	setD(&c.SessionOpenTimeout, 30*time.Second)
	setD(&c.PresenceInterval, 100*time.Millisecond)
	setD(&c.ReloadInterval, time.Second)
	setD(&c.SweepInterval, time.Second)
	setD(&c.LastSeenFlush, 5*time.Minute)
	if c.KeepaliveMissed == 0 {
		c.KeepaliveMissed = 3
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Limits.Now == nil {
		c.Limits.Now = c.Now
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	if c.Version == "" {
		c.Version = "dev"
	}
}

// Server is a running server.
type Server struct {
	cfg    Config
	log    *slog.Logger
	signer ssh.Signer
	sshCfg *ssh.ServerConfig
	store  *store.Store
	reg    *registry.Registry
	lim    *limits.Limiter

	applyMu sync.Mutex // serializes state.json writes and reloads
	lastRaw []byte
	flushed map[string]time.Time
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	mu      sync.Mutex
	lns     map[net.Listener]bool
	conns   map[*conn]bool
	closed  bool
	once    sync.Once
}

// New loads (or creates) the host key and state in cfg.StateDir and returns
// a server ready to Serve.
func New(cfg Config) (*Server, error) {
	cfg.fill()
	st, err := store.Open(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	// A new host key or salt made by the admin commands takes effect now,
	// at the start (PLAN.md 12.4, 12.7).
	if ok, err := hostkey.Apply(cfg.StateDir); err != nil {
		return nil, err
	} else if ok {
		cfg.Log.Warn("new host key in use; computers will warn that the server identity changed")
	}
	signer, err := hostkey.LoadOrCreate(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	if ok, err := salt.Apply(cfg.StateDir); err != nil {
		return nil, err
	} else if ok {
		cfg.Log.Info("new verification salt in use")
	}
	vsalt, err := salt.LoadOrCreate(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	idx, raw, err := st.Load()
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:     cfg,
		log:     cfg.Log,
		signer:  signer,
		store:   st,
		lim:     limits.New(cfg.Limits),
		lastRaw: raw,
		flushed: map[string]time.Time{},
		lns:     map[net.Listener]bool{},
		conns:   map[*conn]bool{},
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.reg = registry.New(registry.Config{
		Now:                  cfg.Now,
		SessionStartTimeout:  cfg.SessionStartTimeout,
		PendingIdle:          cfg.PendingIdle,
		MaxSessionsPerTechie: cfg.MaxSessionsPerTechie,
		Version:              cfg.Version,
		Salt:                 hex.EncodeToString(vsalt),
		Mutate:               s.mutate,
		Log:                  cfg.Log,
	}, idx)
	s.sshCfg = &ssh.ServerConfig{
		PublicKeyCallback: auth.PublicKeyCallback,
		ServerVersion:     "SSH-2.0-helpbuoyserver_" + cfg.Version,
		MaxAuthTries:      6,
	}
	s.sshCfg.AddHostKey(signer)
	s.log.Info("server started", "version", cfg.Version, "state", cfg.StateDir,
		"hostkey", auth.Fingerprint(signer.PublicKey()),
		"techies", len(idx.State.Techies), "computers", len(idx.State.Computers))
	s.loop(cfg.ReloadInterval, s.reload)
	s.loop(cfg.SweepInterval, func() { s.reg.Sweep(); s.lim.Sweep() })
	s.loop(cfg.LastSeenFlush, s.flushLastSeen)
	return s, nil
}

// HostKey returns the server's host public key.
func (s *Server) HostKey() ssh.PublicKey { return s.signer.PublicKey() }

// Registry returns the live registry, for tests and diagnostics.
func (s *Server) Registry() *registry.Registry { return s.reg }

// Limiter returns the limiter, for tests and diagnostics.
func (s *Server) Limiter() *limits.Limiter { return s.lim }

func (s *Server) loop(every time.Duration, fn func()) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-t.C:
				fn()
			}
		}
	}()
}

// mutate writes a state change and installs it; see registry.Config.Mutate.
func (s *Server) mutate(fn func(*store.State) error) error {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	idx, raw, err := s.store.Update(fn)
	if err != nil {
		return err
	}
	s.lastRaw = raw
	s.reg.SetIndex(idx)
	return nil
}

// reload picks up changes the admin CLI made to state.json.
func (s *Server) reload() {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	raw, err := s.store.ReadRaw()
	if err != nil {
		s.log.Error("state read failed", "err", err.Error())
		return
	}
	if bytes.Equal(raw, s.lastRaw) {
		return
	}
	s.lastRaw = raw
	idx, err := store.Parse(raw)
	if err != nil {
		s.log.Error("state reload failed, keeping the previous state", "err", err.Error())
		return
	}
	s.log.Info("state reloaded", "techies", len(idx.State.Techies), "computers", len(idx.State.Computers))
	s.reg.SetIndex(idx)
}

// Reload checks state.json for changes now instead of at the next poll.
func (s *Server) Reload() { s.reload() }

func (s *Server) flushLastSeen() {
	seen := s.reg.LastSeen()
	changed := map[string]time.Time{}
	for fp, t := range seen {
		if !s.flushed[fp].Equal(t) {
			changed[fp] = t
		}
	}
	if len(changed) == 0 {
		return
	}
	err := s.mutate(func(st *store.State) error {
		st.SetLastSeen(changed)
		return nil
	})
	if err != nil {
		s.log.Error("last-seen flush failed", "err", err.Error())
		return
	}
	for fp, t := range changed {
		s.flushed[fp] = t
	}
}

// ErrServerClosed is returned by Serve after Close.
var ErrServerClosed = errors.New("server closed")

// Serve accepts connections on l until Close.
func (s *Server) Serve(l net.Listener) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		l.Close()
		return ErrServerClosed
	}
	s.lns[l] = true
	s.mu.Unlock()
	s.log.Info("listening", "addr", l.Addr().String())
	var delay time.Duration
	for {
		nc, err := l.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return ErrServerClosed
			}
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			// Typically too many open files: wait a little and go on.
			delay = min(max(2*delay, 5*time.Millisecond), time.Second)
			s.log.Error("accept failed", "err", err.Error())
			time.Sleep(delay)
			continue
		}
		delay = 0
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(nc)
		}()
	}
}

// Close stops listening, closes every connection and writes last-seen times.
func (s *Server) Close() error {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		for l := range s.lns {
			l.Close()
		}
		conns := make([]*conn, 0, len(s.conns))
		for c := range s.conns {
			conns = append(conns, c)
		}
		s.mu.Unlock()
		s.cancel()
		for _, c := range conns {
			c.close()
		}
		s.wg.Wait()
		s.flushLastSeen()
		s.log.Info("server stopped")
	})
	return nil
}

// handle runs one TCP connection from accept to close.
func (s *Server) handle(nc net.Conn) {
	defer nc.Close()
	ip := limits.IPOf(nc.RemoteAddr())
	remote := nc.RemoteAddr().String()

	ctx, cancel := context.WithTimeout(s.ctx, s.cfg.HandshakeTimeout)
	release, err := s.lim.AcquireHandshake(ctx, ip)
	cancel()
	if err != nil {
		s.log.Warn("connection refused", "ip", ip, "remote", remote, "reason", err.Error())
		return
	}
	nc.SetDeadline(time.Now().Add(s.cfg.HandshakeTimeout))
	sc, chans, reqs, err := ssh.NewServerConn(nc, s.sshCfg)
	release(err != nil)
	if err != nil {
		s.log.Info("handshake failed", "ip", ip, "remote", remote, "err", err.Error())
		return
	}
	nc.SetDeadline(time.Time{})
	key, err := auth.VerifiedKey(sc.Permissions)
	if err != nil {
		sc.Close()
		return
	}
	fp := auth.Fingerprint(key)
	// One attempt per key per second, for every role. Checked before Attach,
	// so a refused attempt never replaces the key's existing connection.
	if err := s.lim.AdmitKey(fp); err != nil {
		s.log.Info("connection refused", "fp", fp, "ip", ip, "remote", remote, "reason", err.Error())
		sc.Close()
		go ssh.DiscardRequests(reqs)
		for nch := range chans {
			nch.Reject(ssh.Prohibited, "refused")
		}
		return
	}
	c := &conn{s: s, sc: sc, ip: ip, fp: fp, wake: make(chan struct{}, 1), done: make(chan struct{})}
	rc, err := s.reg.Attach(c, fp, ip, func() (func(), error) { return s.lim.AdmitPending(fp, ip) })
	if err != nil {
		s.log.Warn("auth refused", "fp", fp, "role", string(control.RolePending), "ip", ip, "remote", remote, "reason", err.Error())
		sc.Close()
		go ssh.DiscardRequests(reqs)
		for nch := range chans {
			nch.Reject(ssh.Prohibited, "refused")
		}
		return
	}
	c.rc = rc
	role := s.reg.Role(rc)
	s.log.Info("auth ok", "fp", fp, "role", string(role), "ip", ip, "remote", remote,
		"client_version", string(sc.ClientVersion()))

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		sc.Close()
		s.reg.Detach(rc)
		return
	}
	s.conns[c] = true
	s.mu.Unlock()

	go c.globalRequests(reqs)
	go c.keepalive()
	ctrlTimer := time.AfterFunc(s.cfg.HandshakeTimeout, func() {
		c.mu.Lock()
		open := c.ctrl != nil
		c.mu.Unlock()
		if !open {
			s.log.Info("no control channel, closing", "fp", fp, "ip", ip)
			c.close()
		}
	})
	for nch := range chans {
		c.handleChannel(nch)
	}
	ctrlTimer.Stop()
	c.close()
	s.reg.Detach(rc)
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
	s.log.Info("disconnected", "fp", fp, "role", string(s.reg.Role(rc)), "ip", ip)
}

// maxQueue bounds the control messages waiting for a slow peer.
const maxQueue = 256

// conn is one authenticated SSH connection; it implements registry.Peer.
type conn struct {
	s  *Server
	sc *ssh.ServerConn
	ip string
	fp string
	rc *registry.Conn

	mu       sync.Mutex
	ctrl     ssh.Channel
	queue    []any
	presence bool
	kicked   bool
	reason   string
	wake     chan struct{}
	done     chan struct{}
	once     sync.Once
}

func (c *conn) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Send implements registry.Peer.
func (c *conn) Send(m any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.kicked {
		return
	}
	if len(c.queue) >= maxQueue {
		c.s.log.Warn("control queue full, closing", "fp", c.fp, "ip", c.ip)
		go c.close()
		return
	}
	c.queue = append(c.queue, m)
	c.signal()
}

// NotifyPresence implements registry.Peer.
func (c *conn) NotifyPresence() {
	c.mu.Lock()
	c.presence = true
	c.mu.Unlock()
	c.signal()
}

// Kick implements registry.Peer.
func (c *conn) Kick(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.kicked {
		return
	}
	c.kicked, c.reason = true, reason
	if c.ctrl == nil {
		go c.close()
		return
	}
	c.signal()
	time.AfterFunc(2*time.Second, c.close)
}

func (c *conn) close() {
	c.once.Do(func() {
		close(c.done)
		c.sc.Close()
	})
}

// writer sends queued messages and Presence snapshots on the control channel.
// Snapshots go out at most once per PresenceInterval; changes in between are
// coalesced into the next one.
func (c *conn) writer(ch ssh.Channel) {
	cc := control.NewConn(ch)
	fw := wire.NewWriter(ch)
	interval := c.s.cfg.PresenceInterval
	var sentGen uint64
	var lastPres time.Time
	armed := false
	for {
		select {
		case <-c.done:
			return
		case <-c.wake:
		}
		for {
			c.mu.Lock()
			q := c.queue
			c.queue = nil
			kicked, reason := c.kicked, c.reason
			pres := false
			if c.presence {
				if wait := interval - time.Since(lastPres); wait > 0 {
					if !armed {
						armed = true
						time.AfterFunc(wait, c.signal)
					}
				} else {
					pres, armed = true, false
					c.presence = false
				}
			}
			c.mu.Unlock()
			if len(q) == 0 && !pres && !kicked {
				break
			}
			for _, m := range q {
				if err := cc.Send(m); err != nil {
					c.close()
					return
				}
			}
			if kicked {
				cc.Send(&control.Error{Message: reason})
				c.s.log.Info("kicked", "fp", c.fp, "ip", c.ip, "reason", reason)
				ch.CloseWrite()
				c.close()
				return
			}
			if pres {
				b, gen := c.s.reg.Presence()
				if gen != sentGen {
					if err := fw.WriteFrame(uint8(control.MsgPresence), b); err != nil {
						c.close()
						return
					}
					sentGen = gen
				}
				lastPres = time.Now()
			}
		}
	}
}

func (c *conn) globalRequests(reqs <-chan *ssh.Request) {
	for r := range reqs {
		r.Reply(r.Type == KeepaliveRequest, nil)
	}
}

// keepalive drops the connection after KeepaliveMissed unanswered keepalives.
func (c *conn) keepalive() {
	t := time.NewTicker(c.s.cfg.KeepaliveInterval)
	defer t.Stop()
	var pending chan error
	missed := 0
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
		}
		if pending != nil {
			select {
			case err := <-pending:
				pending = nil
				missed = 0
				if err != nil {
					c.close()
					return
				}
			default:
				missed++
				if missed >= c.s.cfg.KeepaliveMissed {
					c.s.log.Info("keepalive timeout", "fp", c.fp, "ip", c.ip)
					c.close()
					return
				}
				continue
			}
		}
		p := make(chan error, 1)
		pending = p
		go func() {
			_, _, err := c.sc.SendRequest(KeepaliveRequest, true, nil)
			p <- err
		}()
	}
}

func (c *conn) handleChannel(nch ssh.NewChannel) {
	switch nch.ChannelType() {
	case control.ChannelControl:
		c.mu.Lock()
		dup := c.ctrl != nil
		c.mu.Unlock()
		if dup {
			nch.Reject(ssh.Prohibited, "control channel already open")
			return
		}
		ch, reqs, err := nch.Accept()
		if err != nil {
			c.close()
			return
		}
		go func() {
			for r := range reqs {
				r.Reply(false, nil)
			}
			c.close() // the control channel is the connection's lifeline
		}()
		c.mu.Lock()
		c.ctrl = ch
		kicked := c.kicked
		c.mu.Unlock()
		if kicked {
			c.close()
			return
		}
		go c.writer(ch)
		if err := c.s.reg.OpenControl(c.rc); err != nil {
			c.close()
			return
		}
		go c.reader(ch)
	case control.ChannelSession:
		go c.s.handleSession(c, nch)
	default:
		nch.Reject(ssh.UnknownChannelType, "channel type not supported")
	}
}

// Size limits for incoming control messages. Apps only send small messages;
// techies paste key lines, so they get a little more room.
const (
	maxAppMessage    = 16 << 10
	maxTechieMessage = 256 << 10
)

func (c *conn) reader(ch ssh.Channel) {
	br := bufio.NewReaderSize(ch, 32<<10)
	for {
		max := maxAppMessage
		if c.s.reg.Role(c.rc) == control.RoleTechie {
			max = maxTechieMessage
		}
		m, err := readMessage(br, max)
		if err != nil {
			if errors.Is(err, io.EOF) {
				c.close()
			} else {
				c.Kick("bad control message: " + err.Error())
			}
			return
		}
		if err := c.s.reg.Handle(c.rc, m); err != nil {
			c.Kick(err.Error())
			return
		}
	}
}

// readMessage reads one control frame of at most max bytes and decodes it.
// It reads the length itself so an oversized frame is refused before it is
// buffered.
func readMessage(r io.Reader, max int) (any, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > uint32(max) {
		return nil, fmt.Errorf("frame of %d bytes", n)
	}
	frame := make([]byte, 4+n)
	copy(frame, hdr[:])
	if _, err := io.ReadFull(r, frame[4:]); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return control.Decode(frame[4], frame[5:])
}

type openResult struct {
	ch   ssh.Channel
	reqs <-chan *ssh.Request
	err  error
}

// handleSession serves a computer's sv-session channel: it checks it against
// the registry, opens the matching channel to the techie and splices them.
func (s *Server) handleSession(c *conn, nch ssh.NewChannel) {
	data, err := control.UnmarshalSessionChannelData(nch.ExtraData())
	if err != nil {
		nch.Reject(ssh.Prohibited, "bad session channel data")
		return
	}
	sess, hp, hdata, err := s.reg.BeginSession(c.rc, data.RequestID)
	if err != nil {
		s.log.Info("session refused", "fp", c.fp, "ip", c.ip, "request", data.RequestID, "reason", err.Error())
		nch.Reject(ssh.Prohibited, err.Error())
		return
	}
	h := hp.(*conn)
	res := make(chan openResult, 1)
	go func() {
		ch, reqs, err := h.sc.OpenChannel(control.ChannelSession, control.MarshalTechiesessionData(hdata))
		res <- openResult{ch, reqs, err}
	}()
	var hr openResult
	timer := time.NewTimer(s.cfg.SessionOpenTimeout)
	select {
	case hr = <-res:
		timer.Stop()
	case <-timer.C:
		hr.err = errors.New("techie did not answer")
		go func() {
			if late := <-res; late.err == nil {
				late.ch.Close()
				go ssh.DiscardRequests(late.reqs)
			}
		}()
	}
	if hr.err != nil {
		s.log.Info("session refused by techie", "fp", c.fp, "techie", h.fp, "reason", hr.err.Error())
		nch.Reject(ssh.ConnectionFailed, "techie did not accept the session")
		sess.End(splice.Result{})
		return
	}
	if sess.Ended() {
		// Cancelled while the techie's channel was being opened.
		nch.Reject(ssh.Prohibited, "request was cancelled")
		hr.ch.Close()
		go ssh.DiscardRequests(hr.reqs)
		return
	}
	cch, creqs, err := nch.Accept()
	if err != nil {
		hr.ch.Close()
		go ssh.DiscardRequests(hr.reqs)
		sess.End(splice.Result{})
		return
	}
	closeBoth := func() {
		cch.Close()
		hr.ch.Close()
	}
	if !sess.Started(closeBoth) {
		closeBoth()
		go ssh.DiscardRequests(creqs)
		go ssh.DiscardRequests(hr.reqs)
		return
	}
	r := splice.Splice(cch, creqs, hr.ch, hr.reqs)
	sess.End(r)
}
