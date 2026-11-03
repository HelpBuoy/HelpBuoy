// Package inner runs the end-to-end "inner" SSH connection inside the session
// channel that the relay splices between client and agent (PLAN.md section 4).
//
// The client app is the SSH server: its host key is its app key, and it
// accepts public key auth only from the one agent key it was told about. The
// desk app is the SSH client: it accepts only the client's registered key as
// host key. Exactly two channel types are allowed, "screen" (client -> agent
// data) and "input" (agent -> client data); everything else is rejected.
package inner

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// Channel type names inside the inner connection. The agent opens both.
const (
	ChannelScreen = "screen"
	ChannelInput  = "input"
)

// ErrWrongKey is returned when the other end presents an unexpected key.
var ErrWrongKey = errors.New("inner: unexpected key")

// ErrTimeout is returned by Serve and Dial when the handshake and channel
// setup do not finish within the setup timeout.
var ErrTimeout = errors.New("inner: setup timed out")

// Tunables; variables so tests can shorten them.
var (
	setupTimeout      = 15 * time.Second
	keepaliveInterval = 15 * time.Second
	keepaliveMaxMiss  = 3
)

const (
	sshUser          = "helpbuoy"
	keepaliveRequest = "keepalive@openssh.com"
	maxAuthTries     = 3
)

var ciphers = []string{"aes128-gcm@openssh.com", "chacha20-poly1305@openssh.com"}

// Stream is one inner channel as a byte stream; wrap it with proto/wire.
type Stream interface {
	io.ReadWriteCloser
	CloseWrite() error
}

// conn holds what both session types share: the SSH connection and the
// result of its Wait, collected once by a background goroutine.
type conn struct {
	c       ssh.Conn
	done    chan struct{}
	waitErr error
}

func newConn(c ssh.Conn) *conn {
	cn := &conn{c: c, done: make(chan struct{})}
	go func() {
		cn.waitErr = c.Wait()
		close(cn.done)
	}()
	return cn
}

func (cn *conn) close() error {
	err := cn.c.Close()
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
		return nil
	}
	return err
}

func (cn *conn) wait() error {
	<-cn.done
	if errors.Is(cn.waitErr, io.EOF) {
		return nil
	}
	return cn.waitErr
}

// closer closes rw once, from a timer or from the setup path.
type closer struct {
	once  sync.Once
	fired atomic.Bool
	rw    io.Closer
}

func (c *closer) timeout() {
	c.fired.Store(true)
	c.once.Do(func() { c.rw.Close() })
}

func (c *closer) close() { c.once.Do(func() { c.rw.Close() }) }

func sameKey(a, b ssh.PublicKey) bool {
	return a != nil && b != nil && bytes.Equal(a.Marshal(), b.Marshal())
}

// ServerSession is the client app's side of an inner connection.
type ServerSession struct {
	*conn
	screen ssh.Channel
	input  ssh.Channel
}

// Serve runs the inner SSH server on rw (the client's sv-session channel).
// hostKey is the client's app key; agentKey is the only accepted client key
// (from control.SessionReady). It returns after the handshake and after the
// agent has opened both the screen and input channels, or with an error.
//
// An agent that offers a different key gets an error wrapping ErrWrongKey.
// Global requests other than keepalive@openssh.com, all channel requests and
// all channel types except one "screen" and one "input" are rejected. If setup
// does not complete within 15 seconds, rw is closed and ErrTimeout returned.
func Serve(rw io.ReadWriteCloser, hostKey ssh.Signer, agentKey ssh.PublicKey) (*ServerSession, error) {
	if hostKey == nil || agentKey == nil {
		return nil, errors.New("inner: missing key")
	}
	var wrongKey atomic.Bool
	cfg := &ssh.ServerConfig{
		Config:       ssh.Config{Ciphers: ciphers},
		MaxAuthTries: maxAuthTries,
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if !sameKey(key, agentKey) {
				wrongKey.Store(true)
				return nil, ErrWrongKey
			}
			return &ssh.Permissions{}, nil
		},
		ServerVersion: "SSH-2.0-helpbuoy",
	}
	cfg.AddHostKey(hostKey)

	cl := &closer{rw: rw}
	timer := time.AfterFunc(setupTimeout, cl.timeout)
	defer timer.Stop()

	sc, chans, reqs, err := ssh.NewServerConn(rwConn{rw}, cfg)
	if err != nil {
		cl.close()
		switch {
		case cl.fired.Load():
			return nil, ErrTimeout
		case wrongKey.Load():
			return nil, fmt.Errorf("%w: %v", ErrWrongKey, err)
		}
		return nil, fmt.Errorf("inner: handshake: %w", err)
	}
	go serveGlobalRequests(reqs)

	s := &ServerSession{conn: newConn(sc)}
	ready := make(chan struct{})
	go s.serveChannels(chans, ready)

	select {
	case <-ready:
		if !timer.Stop() {
			sc.Close()
			return nil, ErrTimeout
		}
		return s, nil
	case <-s.done:
		if cl.fired.Load() {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("inner: connection ended during setup: %v", s.waitErr)
	}
}

// serveGlobalRequests answers keepalives and rejects everything else.
func serveGlobalRequests(reqs <-chan *ssh.Request) {
	for r := range reqs {
		if r.WantReply {
			r.Reply(r.Type == keepaliveRequest, nil)
		}
	}
}

// serveChannels accepts one screen and one input channel, closes ready once
// both are open, and rejects every other channel for the rest of the
// connection.
func (s *ServerSession) serveChannels(chans <-chan ssh.NewChannel, ready chan struct{}) {
	signalled := false
	for nc := range chans {
		var slot *ssh.Channel
		switch nc.ChannelType() {
		case ChannelScreen:
			slot = &s.screen
		case ChannelInput:
			slot = &s.input
		default:
			nc.Reject(ssh.UnknownChannelType, "channel type not allowed")
			continue
		}
		if *slot != nil {
			nc.Reject(ssh.Prohibited, "channel already open")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go ssh.DiscardRequests(creqs)
		// Written before ready is closed, read only after; no lock needed.
		*slot = ch
		if !signalled && s.screen != nil && s.input != nil {
			signalled = true
			close(ready)
		}
	}
}

// Screen is the stream on which the client sends screen messages and reads acks.
func (s *ServerSession) Screen() Stream { return s.screen }

// Input is the stream on which the client reads input messages.
func (s *ServerSession) Input() Stream { return s.input }

// Close ends the inner connection.
func (s *ServerSession) Close() error { return s.close() }

// Wait blocks until the inner connection ends.
func (s *ServerSession) Wait() error { return s.wait() }

// ClientSession is the desk app's side of an inner connection.
type ClientSession struct {
	*conn
	screen ssh.Channel
	input  ssh.Channel
	rtt    atomic.Int64
}

// Dial runs the inner SSH client on rw (the agent's sv-session channel).
// signer is the agent's app key; clientKey is the only accepted host key (the
// registered client key). It opens the screen and input channels.
//
// A host presenting any other key fails with an error wrapping ErrWrongKey.
// After setup the session sends a keepalive every 15 seconds and closes the
// connection when three in a row go unanswered. If setup does not complete
// within 15 seconds, rw is closed and ErrTimeout returned.
func Dial(rw io.ReadWriteCloser, signer ssh.Signer, clientKey ssh.PublicKey) (*ClientSession, error) {
	if signer == nil || clientKey == nil {
		return nil, errors.New("inner: missing key")
	}
	var wrongKey atomic.Bool
	cfg := &ssh.ClientConfig{
		Config: ssh.Config{Ciphers: ciphers},
		User:   sshUser,
		Auth:   []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if !sameKey(key, clientKey) {
				wrongKey.Store(true)
				return ErrWrongKey
			}
			return nil
		},
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		ClientVersion:     "SSH-2.0-helpbuoy",
	}

	cl := &closer{rw: rw}
	timer := time.AfterFunc(setupTimeout, cl.timeout)
	defer timer.Stop()

	fail := func(err error) (*ClientSession, error) {
		cl.close()
		switch {
		case cl.fired.Load():
			return nil, ErrTimeout
		case wrongKey.Load():
			return nil, ErrWrongKey
		}
		return nil, err
	}

	cc, chans, reqs, err := ssh.NewClientConn(rwConn{rw}, "inner", cfg)
	if err != nil {
		return fail(fmt.Errorf("inner: handshake: %w", err))
	}
	go ssh.DiscardRequests(reqs)
	go func() {
		for nc := range chans {
			nc.Reject(ssh.Prohibited, "no channels accepted")
		}
	}()

	c := &ClientSession{conn: newConn(cc)}
	open := func(typ string) (ssh.Channel, error) {
		ch, creqs, err := cc.OpenChannel(typ, nil)
		if err != nil {
			return nil, fmt.Errorf("inner: open %s: %w", typ, err)
		}
		go ssh.DiscardRequests(creqs)
		return ch, nil
	}
	if c.screen, err = open(ChannelScreen); err != nil {
		cc.Close()
		return fail(err)
	}
	if c.input, err = open(ChannelInput); err != nil {
		cc.Close()
		return fail(err)
	}
	if !timer.Stop() {
		cc.Close()
		return nil, ErrTimeout
	}
	go c.keepalive(keepaliveInterval, keepaliveMaxMiss)
	return c, nil
}

// keepalive sends keepalive@openssh.com requests and records the round trip
// time. A tick that finds the previous request still unanswered counts as a
// miss; after maxMiss misses in a row the connection is closed.
func (c *ClientSession) keepalive(interval time.Duration, maxMiss int) {
	t := time.NewTicker(interval)
	defer t.Stop()
	var (
		result chan error
		sent   time.Time
		missed int
	)
	send := func() {
		result = make(chan error, 1)
		sent = time.Now()
		go func(res chan error) {
			_, _, err := c.c.SendRequest(keepaliveRequest, true, nil)
			res <- err
		}(result)
	}
	send()
	for {
		select {
		case <-c.done:
			return
		case err := <-result:
			result = nil
			if err != nil {
				c.c.Close()
				return
			}
			c.rtt.Store(int64(time.Since(sent)))
			missed = 0
		case <-t.C:
			if result == nil {
				send()
				continue
			}
			missed++
			if missed >= maxMiss {
				c.c.Close()
				return
			}
		}
	}
}

// RTT returns the round trip time of the most recent answered keepalive, or
// 0 if none has been answered yet. The first keepalive is sent right after
// setup, so a value is usually available within one round trip.
func (c *ClientSession) RTT() time.Duration { return time.Duration(c.rtt.Load()) }

// Screen is the stream on which the agent reads screen messages and sends acks.
func (c *ClientSession) Screen() Stream { return c.screen }

// Input is the stream on which the agent sends input messages.
func (c *ClientSession) Input() Stream { return c.input }

// Close ends the inner connection.
func (c *ClientSession) Close() error { return c.close() }

// Wait blocks until the inner connection ends.
func (c *ClientSession) Wait() error { return c.wait() }
