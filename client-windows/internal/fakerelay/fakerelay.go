// Package fakerelay is a small stand-in for hmw-relay that speaks the control
// protocol towards clients. It is used by the client's tests and by the
// fakerelay command for manual checks of the windows; it is not a relay.
package fakerelay

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
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
	// AutoName is the name used by AutoRegister (default "demo@PC"); the
	// real relay takes it from the comment of the pasted key line.
	AutoName string
	// AutoTake takes every open request after this delay and sends
	// SessionReady with HelperName and HelperKey; 0 = never.
	AutoTake   time.Duration
	HelperName string
	HelperKey  string
	Logger     *slog.Logger
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
	registered map[string]string // fingerprint -> name
	conns      map[string]*conn  // online, by fingerprint
	nextReq    int
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
		registered: make(map[string]string),
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

// Register registers the key with fingerprint fp under name and, if it is
// online and pending, sends it Registered.
func (r *Relay) Register(fp, name string) {
	r.mu.Lock()
	_, was := r.registered[fp]
	r.registered[fp] = name
	c := r.conns[fp]
	r.mu.Unlock()
	if c != nil && !was {
		c.ctl.Send(control.Registered{Name: name})
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

// Take marks the open request of fp as taken and sends SessionReady.
func (r *Relay) Take(fp, helperName, helperKey string) error {
	r.mu.Lock()
	c := r.conns[fp]
	var id string
	if c != nil {
		id, c.reqID = c.reqID, ""
	}
	r.mu.Unlock()
	if id == "" {
		return errors.New("fakerelay: no open request")
	}
	if err := c.ctl.Send(control.RequestStateMsg{RequestID: id, State: control.RequestTaken}); err != nil {
		return err
	}
	return c.ctl.Send(control.SessionReady{
		RequestID:         id,
		HelperName:        helperName,
		HelperDisplayName: "Helper " + helperName,
		HelperKey:         helperKey,
	})
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
	name, registered := r.registered[c.fp]
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
		hello.Role, hello.Name = control.RoleClient, name
	}
	if err := c.ctl.Send(hello); err != nil {
		return
	}
	if !registered && r.opts.AutoRegister > 0 {
		time.AfterFunc(r.opts.AutoRegister, func() {
			name := r.opts.AutoName
			if name == "" {
				name = "demo@PC"
			}
			r.Register(c.fp, name)
		})
	}
	for {
		m, err := c.ctl.Receive()
		if err != nil {
			return
		}
		switch m := m.(type) {
		case *control.RequestHelp:
			r.mu.Lock()
			r.nextReq++
			id := fmt.Sprintf("req-%d", r.nextReq)
			c.reqID = id
			r.mu.Unlock()
			c.ctl.Send(control.RequestStateMsg{RequestID: id, State: control.RequestOpen})
			if r.opts.AutoTake > 0 {
				time.AfterFunc(r.opts.AutoTake, func() {
					r.Take(c.fp, r.opts.HelperName, r.opts.HelperKey)
				})
			}
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
