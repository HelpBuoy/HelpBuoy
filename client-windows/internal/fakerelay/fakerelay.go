// Package fakerelay is a small stand-in for hmw-relay that speaks the control
// protocol towards clients. It is used by the client's tests and by the
// fakerelay command for manual checks of the windows; it is not a relay.
package fakerelay

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/help-me-win/proto/control"
)

// Received is a control message from a client.
type Received struct {
	Fingerprint string
	Msg         any
}

// Session is a session channel opened by a client.
type Session struct {
	Fingerprint string
	Key         ssh.PublicKey // the client's key
	Data        control.SessionChannelData
	Channel     ssh.Channel
}

// Options for Start.
type Options struct {
	// AutoRegister registers every pending key after this delay; 0 = never.
	AutoRegister time.Duration
	// AutoConnect connects helper HelperName (key HelperKey) this long
	// after a registered client comes online; 0 = never.
	AutoConnect time.Duration
	HelperName  string
	HelperKey   string
	Logger      *slog.Logger
}

// Relay is a running fake relay.
type Relay struct {
	opts    Options
	ln      net.Listener
	hostKey ssh.Signer
	log     *slog.Logger

	recv     chan Received
	sessions chan Session

	mu         sync.Mutex
	registered map[string]bool  // fingerprints of registered keys
	conns      map[string]*conn // online, by fingerprint
	refuseAll  bool
}

type conn struct {
	fp    string
	ssh   *ssh.ServerConn
	ctl   *control.Conn
	reqID string
}

// Start listens on 127.0.0.1:0, or on addr if given.
func Start(addr string, opts Options) (*Relay, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	r := &Relay{
		opts:       opts,
		ln:         ln,
		hostKey:    signer,
		log:        log,
		recv:       make(chan Received, 64),
		sessions:   make(chan Session, 4),
		registered: make(map[string]bool),
		conns:      make(map[string]*conn),
	}
	go r.serve()
	return r, nil
}

// Addr is the listening address.
func (r *Relay) Addr() string { return r.ln.Addr().String() }

// HostKey is the relay's host key.
func (r *Relay) HostKey() ssh.PublicKey { return r.hostKey.PublicKey() }

// Received delivers control messages sent by clients (except ClientHello).
func (r *Relay) Received() <-chan Received { return r.recv }

// Sessions delivers session channels opened by clients.
func (r *Relay) Sessions() <-chan Session { return r.sessions }

// Close stops listening and drops every connection.
func (r *Relay) Close() error {
	err := r.ln.Close()
	r.mu.Lock()
	for _, c := range r.conns {
		c.ssh.Close()
	}
	r.mu.Unlock()
	return err
}

// RefuseAll makes the relay refuse every key in the SSH handshake.
func (r *Relay) RefuseAll(v bool) {
	r.mu.Lock()
	r.refuseAll = v
	r.mu.Unlock()
}

// Register registers the key with fingerprint fp and, if it is online and
// pending, sends it Registered.
func (r *Relay) Register(fp string) {
	r.mu.Lock()
	was := r.registered[fp]
	r.registered[fp] = true
	c := r.conns[fp]
	r.mu.Unlock()
	if c != nil && !was {
		c.ctl.Send(control.Registered{})
		r.autoConnect(fp)
	}
}

// Online reports whether a client with fingerprint fp is connected.
func (r *Relay) Online(fp string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conns[fp] != nil
}

// Send sends m to the client with fingerprint fp.
func (r *Relay) Send(fp string, m any) error {
	r.mu.Lock()
	c := r.conns[fp]
	r.mu.Unlock()
	if c == nil {
		return errors.New("fakerelay: client not online")
	}
	return c.ctl.Send(m)
}

// Kick drops the connection of fp.
func (r *Relay) Kick(fp string) {
	r.mu.Lock()
	c := r.conns[fp]
	r.mu.Unlock()
	if c != nil {
		c.ssh.Close()
	}
}

// Connect plays a helper connecting to fp: it sends SessionReady with a new
// request id and returns that id.
func (r *Relay) Connect(fp, helperName, helperKey string) (string, error) {
	id := control.NewRequestID()
	r.mu.Lock()
	c := r.conns[fp]
	if c != nil {
		c.reqID = id
	}
	r.mu.Unlock()
	if c == nil {
		return "", errors.New("fakerelay: client not online")
	}
	return id, c.ctl.Send(control.SessionReady{
		RequestID:  id,
		HelperName: helperName,
		HelperKey:  helperKey,
	})
}

// autoConnect schedules a Connect for AutoConnect.
func (r *Relay) autoConnect(fp string) {
	if r.opts.AutoConnect > 0 {
		time.AfterFunc(r.opts.AutoConnect, func() {
			r.Connect(fp, r.opts.HelperName, r.opts.HelperKey)
		})
	}
}

func (r *Relay) serve() {
	for {
		nc, err := r.ln.Accept()
		if err != nil {
			return
		}
		go r.handle(nc)
	}
}

func (r *Relay) handle(nc net.Conn) {
	defer nc.Close()
	var fp string
	var key ssh.PublicKey
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			r.mu.Lock()
			refuse := r.refuseAll
			r.mu.Unlock()
			if refuse {
				return nil, errors.New("refused")
			}
			fp, key = ssh.FingerprintSHA256(k), k
			return nil, nil
		},
	}
	cfg.AddHostKey(r.hostKey)
	sc, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	go func() {
		for req := range reqs {
			if req.WantReply {
				req.Reply(false, nil) // like OpenSSH for unknown requests, also keepalives
			}
		}
	}()
	c := &conn{fp: fp, ssh: sc}
	for nch := range chans {
		switch nch.ChannelType() {
		case control.ChannelControl:
			if c.ctl != nil {
				nch.Reject(ssh.Prohibited, "one control channel only")
				continue
			}
			ch, chReqs, err := nch.Accept()
			if err != nil {
				return
			}
			go ssh.DiscardRequests(chReqs)
			c.ctl = control.NewConn(ch)
			go r.control(c)
		case control.ChannelSession:
			d, err := control.UnmarshalSessionChannelData(nch.ExtraData())
			if err != nil {
				nch.Reject(ssh.ConnectionFailed, "bad extra data")
				continue
			}
			ch, chReqs, err := nch.Accept()
			if err != nil {
				return
			}
			go ssh.DiscardRequests(chReqs)
			r.sessions <- Session{Fingerprint: fp, Key: key, Data: d, Channel: ch}
		default:
			nch.Reject(ssh.UnknownChannelType, "no")
		}
	}
}

func (r *Relay) control(c *conn) {
	defer c.ssh.Close()
	m, err := c.ctl.Receive()
	if err != nil {
		return
	}
	if _, ok := m.(*control.ClientHello); !ok {
		c.ctl.Send(control.Error{Message: "expected ClientHello"})
		return
	}
	r.mu.Lock()
	if old := r.conns[c.fp]; old != nil {
		old.ssh.Close() // a known key that reconnects replaces its connection
	}
	r.conns[c.fp] = c
	registered := r.registered[c.fp]
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if r.conns[c.fp] == c {
			delete(r.conns, c.fp)
		}
		r.mu.Unlock()
	}()

	hello := control.Hello{Role: control.RolePending, RelayVersion: "fake", ServerTime: time.Now()}
	if registered {
		hello.Role = control.RoleClient
	}
	if err := c.ctl.Send(hello); err != nil {
		return
	}
	if registered {
		r.autoConnect(c.fp)
	} else if r.opts.AutoRegister > 0 {
		time.AfterFunc(r.opts.AutoRegister, func() {
			r.Register(c.fp)
		})
	}
	for {
		m, err := c.ctl.Receive()
		if err != nil {
			return
		}
		switch m := m.(type) {
		case *control.CancelRequest:
			r.mu.Lock()
			if c.reqID == m.RequestID {
				c.reqID = ""
			}
			r.mu.Unlock()
			c.ctl.Send(control.RequestStateMsg{RequestID: m.RequestID, State: control.RequestCancelled})
		}
		select {
		case r.recv <- Received{Fingerprint: c.fp, Msg: m}:
		default: // nobody is reading
		}
	}
}
