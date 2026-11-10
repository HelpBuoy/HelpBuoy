// Package fakeserver is a small stand-in for helpbuoy-server that speaks the control
// protocol towards computers. It is used by the computer's tests and by the
// fakeserver command for manual checks of the windows; it is not a server.
package fakeserver

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/proto/control"
)

// Received is a control message from a computer.
type Received struct {
	Fingerprint string
	Msg         any
}

// Session is a session channel opened by a computer.
type Session struct {
	Fingerprint string
	Key         ssh.PublicKey // the computer's key
	Data        control.SessionChannelData
	Channel     ssh.Channel
}

// Options for Start.
type Options struct {
	// AutoRegister registers every pending key after this delay; 0 = never.
	AutoRegister time.Duration
	// AutoConnect connects techie TechieName (key TechieKey) this long
	// after a registered computer comes online; 0 = never.
	AutoConnect time.Duration
	TechieName  string
	TechieKey   string
	// Info is sent after every Hello.
	Info   control.ServerInfo
	Logger *slog.Logger
}

// Server is a running fake server.
type Server struct {
	opts    Options
	ln      net.Listener
	hostKey ssh.Signer
	log     *slog.Logger

	recv     chan Received
	sessions chan Session

	mu         sync.Mutex
	registered map[string]bool                     // fingerprints of registered keys
	approved   map[string][]control.ApprovedServer // by computer fingerprint
	conns      map[string]*conn                    // online, by fingerprint
	refuseAll  bool
}

type conn struct {
	fp    string
	ssh   *ssh.ServerConn
	ctl   *control.Conn
	reqID string
}

// Start listens on 127.0.0.1:0, or on addr if given.
func Start(addr string, opts Options) (*Server, error) {
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
	r := &Server{
		opts:       opts,
		ln:         ln,
		hostKey:    signer,
		log:        log,
		recv:       make(chan Received, 64),
		sessions:   make(chan Session, 4),
		registered: make(map[string]bool),
		approved:   make(map[string][]control.ApprovedServer),
		conns:      make(map[string]*conn),
	}
	go r.serve()
	return r, nil
}

// Addr is the listening address.
func (r *Server) Addr() string { return r.ln.Addr().String() }

// HostKey is the server's host key.
func (r *Server) HostKey() ssh.PublicKey { return r.hostKey.PublicKey() }

// SetApproved sets the approved list of computer fp and sends it if the
// computer is online and registered.
func (r *Server) SetApproved(fp string, servers []control.ApprovedServer) {
	r.mu.Lock()
	r.approved[fp] = servers
	c := r.conns[fp]
	reg := r.registered[fp]
	r.mu.Unlock()
	if c != nil && reg {
		c.ctl.Send(r.approvedFor(fp))
	}
}

func (r *Server) approvedFor(fp string) control.ApprovedServers {
	r.mu.Lock()
	defer r.mu.Unlock()
	return control.ApprovedServers{Servers: append([]control.ApprovedServer{}, r.approved[fp]...)}
}

// Received delivers control messages sent by computers (except ComputerHello).
func (r *Server) Received() <-chan Received { return r.recv }

// Sessions delivers session channels opened by computers.
func (r *Server) Sessions() <-chan Session { return r.sessions }

// Close stops listening and drops every connection.
func (r *Server) Close() error {
	err := r.ln.Close()
	r.mu.Lock()
	for _, c := range r.conns {
		c.ssh.Close()
	}
	r.mu.Unlock()
	return err
}

// RefuseAll makes the server refuse every key in the SSH handshake.
func (r *Server) RefuseAll(v bool) {
	r.mu.Lock()
	r.refuseAll = v
	r.mu.Unlock()
}

// Register registers the key with fingerprint fp and, if it is online and
// pending, sends it Registered.
func (r *Server) Register(fp string) {
	r.mu.Lock()
	was := r.registered[fp]
	r.registered[fp] = true
	c := r.conns[fp]
	r.mu.Unlock()
	if c != nil && !was {
		c.ctl.Send(control.Registered{})
		c.ctl.Send(r.approvedFor(fp))
		r.autoConnect(fp)
	}
}

// Online reports whether a computer with fingerprint fp is connected.
func (r *Server) Online(fp string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conns[fp] != nil
}

// Send sends m to the computer with fingerprint fp.
func (r *Server) Send(fp string, m any) error {
	r.mu.Lock()
	c := r.conns[fp]
	r.mu.Unlock()
	if c == nil {
		return errors.New("fakeserver: computer not online")
	}
	return c.ctl.Send(m)
}

// Kick drops the connection of fp.
func (r *Server) Kick(fp string) {
	r.mu.Lock()
	c := r.conns[fp]
	r.mu.Unlock()
	if c != nil {
		c.ssh.Close()
	}
}

// Connect plays a techie connecting to fp: it sends SessionReady with a new
// request id and returns that id.
func (r *Server) Connect(fp, techieName, techieKey string) (string, error) {
	id := control.NewRequestID()
	r.mu.Lock()
	c := r.conns[fp]
	if c != nil {
		c.reqID = id
	}
	r.mu.Unlock()
	if c == nil {
		return "", errors.New("fakeserver: computer not online")
	}
	return id, c.ctl.Send(control.SessionReady{
		RequestID:  id,
		TechieName: techieName,
		TechieKey:  techieKey,
	})
}

// autoConnect schedules a Connect for AutoConnect.
func (r *Server) autoConnect(fp string) {
	if r.opts.AutoConnect > 0 {
		time.AfterFunc(r.opts.AutoConnect, func() {
			r.Connect(fp, r.opts.TechieName, r.opts.TechieKey)
		})
	}
}

func (r *Server) serve() {
	for {
		nc, err := r.ln.Accept()
		if err != nil {
			return
		}
		go r.handle(nc)
	}
}

func (r *Server) handle(nc net.Conn) {
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

func (r *Server) control(c *conn) {
	defer c.ssh.Close()
	m, err := c.ctl.Receive()
	if err != nil {
		return
	}
	if _, ok := m.(*control.ComputerHello); !ok {
		c.ctl.Send(control.Error{Message: "expected ComputerHello"})
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

	hello := control.Hello{Role: control.RolePending, ServerVersion: "fake", ServerTime: time.Now()}
	if registered {
		hello.Role = control.RoleComputer
	}
	if err := c.ctl.Send(hello); err != nil {
		return
	}
	r.mu.Lock()
	info := r.opts.Info
	r.mu.Unlock()
	c.ctl.Send(info)
	if registered {
		c.ctl.Send(r.approvedFor(c.fp))
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
