// Package fake is a small headless implementation of the app side of the
// sv-control protocol, used by cmd/faketechie, cmd/fakecomputer and the server
// tests.
package fake

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/proto/transport"
)

// KeyFile is the private key file name inside a key directory.
const KeyFile = "id_ed25519"

// NewKey returns a fresh in-memory Ed25519 signer.
func NewKey() ssh.Signer {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		panic(err)
	}
	return s
}

// LoadOrCreateKey loads dir/id_ed25519, creating it (and dir/id_ed25519.pub
// with the comment) if it does not exist.
func LoadOrCreateKey(dir, comment string) (ssh.Signer, error) {
	p := filepath.Join(dir, KeyFile)
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		if err := createKey(p, comment); err != nil {
			return nil, err
		}
		b, err = os.ReadFile(p)
	}
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(b)
}

func createKey(p, comment string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	blk, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return err
	}
	spub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, pem.EncodeToMemory(blk), 0o600); err != nil {
		return err
	}
	return os.WriteFile(p+".pub", []byte(KeyLine(spub, comment)+"\n"), 0o644)
}

// KeyLine returns the authorized_keys line of k with an optional comment.
func KeyLine(k ssh.PublicKey, comment string) string {
	s := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
	if comment != "" {
		s += " " + comment
	}
	return s
}

// Options tune Dial.
type Options struct {
	// HostKey pins the server host key; nil accepts any key.
	HostKey ssh.PublicKey
	// LocalAddr is the local address to dial from (tests use it to simulate
	// several source IPs on loopback).
	LocalAddr net.Addr
	// Via is the "host:port" to connect to instead of addr, which then only
	// names the host (as SNI and Host header), like a DNS name pointing at
	// a shared relay.
	Via string
	// NoControl skips opening the control channel.
	NoControl bool
	Timeout   time.Duration
}

func noProxy(*http.Request) (*url.URL, error) { return nil, nil }

// Conn is an app connection to the server.
type Conn struct {
	Client   *ssh.Client
	HostKey  ssh.PublicKey
	Ctrl     ssh.Channel
	Sessions <-chan ssh.NewChannel // incoming sv-session channels (techies)
	Msgs     chan any              // decoded control messages; closed at end

	ctrl    *control.Conn
	mu      sync.Mutex
	readErr error
}

// Dial connects to the server with signer and opens the control channel.
func Dial(addr string, signer ssh.Signer, o Options) (*Conn, error) {
	if o.Timeout == 0 {
		o.Timeout = 10 * time.Second
	}
	c := &Conn{Msgs: make(chan any, 4096)}
	cfg := &ssh.ClientConfig{
		User: control.SSHUser,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
			if o.HostKey != nil && !keysEqual(o.HostKey, k) {
				return fmt.Errorf("server host key mismatch: got %s", ssh.FingerprintSHA256(k))
			}
			c.HostKey = k
			return nil
		},
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		Timeout:           o.Timeout,
	}
	d := net.Dialer{Timeout: o.Timeout, LocalAddr: o.LocalAddr}
	ctx, cancel := context.WithTimeout(context.Background(), o.Timeout)
	dial := d.DialContext
	if o.Via != "" {
		dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return d.DialContext(ctx, network, o.Via)
		}
	}
	nc, err := transport.Dial(ctx, addr, transport.DialOptions{Dial: dial, Proxy: noProxy})
	cancel()
	if err != nil {
		return nil, err
	}
	nc.SetDeadline(time.Now().Add(o.Timeout))
	sc, chans, reqs, err := ssh.NewClientConn(nc, addr, cfg)
	if err != nil {
		nc.Close()
		return nil, err
	}
	nc.SetDeadline(time.Time{})
	c.Client = ssh.NewClient(sc, chans, reqs)
	c.Sessions = c.Client.HandleChannelOpen(control.ChannelSession)
	if o.NoControl {
		close(c.Msgs)
		return c, nil
	}
	if err := c.OpenControl(); err != nil {
		c.Client.Close()
		return nil, err
	}
	return c, nil
}

// OpenControl opens the control channel and starts reading messages.
func (c *Conn) OpenControl() error {
	ch, reqs, err := c.Client.OpenChannel(control.ChannelControl, nil)
	if err != nil {
		return err
	}
	go ssh.DiscardRequests(reqs)
	c.Ctrl = ch
	c.ctrl = control.NewConn(ch)
	go func() {
		defer close(c.Msgs)
		for {
			m, err := c.ctrl.Receive()
			if err != nil {
				c.mu.Lock()
				c.readErr = err
				c.mu.Unlock()
				return
			}
			c.Msgs <- m
		}
	}()
	return nil
}

func keysEqual(a, b ssh.PublicKey) bool { return string(a.Marshal()) == string(b.Marshal()) }

// Send writes one control message.
func (c *Conn) Send(m any) error { return c.ctrl.Send(m) }

// Close closes the connection.
func (c *Conn) Close() error { return c.Client.Close() }

// ErrClosed is returned when the control channel ended.
var ErrClosed = errors.New("control channel closed")

// ErrTimeout is returned when no matching message arrived in time.
var ErrTimeout = errors.New("timed out waiting for a control message")

// Next returns the next message.
func (c *Conn) Next(timeout time.Duration) (any, error) {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case m, ok := <-c.Msgs:
		if !ok {
			return nil, ErrClosed
		}
		return m, nil
	case <-t.C:
		return nil, ErrTimeout
	}
}

// WaitFor returns the first message of type *T that satisfies ok (nil
// matches any), dropping other messages on the way.
func WaitFor[T any](c *Conn, timeout time.Duration, ok func(*T) bool) (*T, error) {
	deadline := time.Now().Add(timeout)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return nil, ErrTimeout
		}
		m, err := c.Next(left)
		if err != nil {
			return nil, err
		}
		if v, is := m.(*T); is && (ok == nil || ok(v)) {
			return v, nil
		}
		if e, is := m.(*control.Error); is {
			if _, wantErr := any((*T)(nil)).(*control.Error); !wantErr {
				return nil, fmt.Errorf("server error: %s", e.Message)
			}
		}
	}
}
