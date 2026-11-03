package views

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/helpbuoy/desk/serverconn"
	"github.com/mevdschee/helpbuoy/desk/session"
	"github.com/mevdschee/helpbuoy/proto/appkey"
	"github.com/mevdschee/helpbuoy/proto/control"
	"github.com/mevdschee/helpbuoy/proto/identity"
)

// demo fakes a server with a few computers so the UI can be seen and tried
// without a network. Sessions are served by an in-process fake computer
// (demoComputer) through the real screen and input encodings.
type demo struct {
	a     *App
	stops []func()
}

func newKey(comment string) (ssh.Signer, string, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, "", err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey())))
	if comment != "" {
		line += " " + comment
	}
	return s, line, nil
}

func newDemo(a *App) *demo {
	d := &demo{a: a}
	s, line, err := newKey("techie@demo")
	if err != nil {
		a.keyErr = err
		return d
	}
	a.key = &appkey.Key{Signer: s, Public: s.PublicKey(), Comment: "techie@demo", Line: line, Fingerprint: ssh.FingerprintSHA256(s.PublicKey())}
	hk, _, _ := newKey("")
	a.hostKey = hk.PublicKey()
	a.cfg.ServerAddr = "helpme.example.com:5959"
	a.status = serverconn.StatusEvent{Status: serverconn.StatusOnline, HostKey: a.hostKey}
	a.me = "techie"
	a.backend = d

	now := time.Now()
	computer := func(name string, online bool) control.ComputerInfo {
		k, _, _ := newKey(name)
		return control.ComputerInfo{
			Fingerprint: ssh.FingerprintSHA256(k.PublicKey()), Name: name, Online: online,
			RegisteredBy: "techie", RegisteredAt: now.Add(-30 * 24 * time.Hour), LastSeen: now.Add(-26 * time.Hour),
		}
	}
	p := &control.Presence{Techies: []control.TechieInfo{{Name: "techie", Online: true}, {Name: "bob", Online: true}}}
	anna := computer("anna@ANNA-LAPTOP", true)
	jan := computer("jan@KANTOOR-PC", true)
	oma := computer("oma@WOONKAMER", true)
	oma.Request = &control.RequestInfo{ID: "demo-3", Created: now.Add(-10 * time.Minute), State: control.RequestTaken, TakenBy: "bob"}
	oma.InSessionWith = "bob"
	piet := computer("piet@PIET-PC", true)
	lisa := computer("lisa@LISA-PC", false)
	kees := computer("kees@KEES-LAPTOP", false)
	kees.LastSeen = now.Add(-2 * time.Hour)
	p.Computers = []control.ComputerInfo{piet, oma, anna, lisa, jan, kees}
	a.setPresence(p)
	a.setServerInfo(&control.ServerInfo{Title: "Example Inc", Instruction: "Call +1 234 567 890 for support"})
	return d
}

// done returns an already answered command result.
func done(err error) <-chan error {
	c := make(chan error, 1)
	c <- err
	return c
}

func (d *demo) RegisterComputer(line, name string) <-chan error {
	key, comment, err := identity.ParseLine(line)
	if err != nil {
		return done(err)
	}
	if name == "" {
		name = comment
	}
	p := d.a.presence
	fp := ssh.FingerprintSHA256(key)
	for _, c := range p.Computers {
		if c.Fingerprint == fp {
			return done(fmt.Errorf("already registered as %s", c.Name))
		}
	}
	p.Computers = append(p.Computers, control.ComputerInfo{Fingerprint: fp, Name: name, RegisteredBy: d.a.me, RegisteredAt: time.Now()})
	return done(nil)
}

// SetServerInfo changes the demo server's title and instruction locally.
func (d *demo) SetServerInfo(title, instruction string) <-chan error {
	d.a.setServerInfo(&control.ServerInfo{Title: strings.TrimSpace(title), Instruction: strings.TrimSpace(instruction)})
	return done(nil)
}

// RegisterTechie adds a demo techie locally.
func (d *demo) RegisterTechie(line, name string) <-chan error {
	key, _, err := identity.ParseLine(line)
	if err != nil {
		return done(err)
	}
	p := d.a.presence
	fp := ssh.FingerprintSHA256(key)
	for _, h := range p.Techies {
		if h.Name == name {
			return done(fmt.Errorf("techie %q already exists", name))
		}
		if h.Fingerprint == fp {
			return done(fmt.Errorf("key %s is already the key of techie %q", fp, h.Name))
		}
	}
	p.Techies = append(p.Techies, control.TechieInfo{Name: name, Fingerprint: fp})
	return done(nil)
}

// RenameComputer renames a demo computer locally, with the server's rules.
func (d *demo) RenameComputer(fp, name string) <-chan error {
	name = strings.TrimSpace(name)
	if !validComputerName(name) {
		return done(fmt.Errorf("name must be 1 to %d printable characters", maxComputerName))
	}
	p := d.a.presence
	for i := range p.Computers {
		if p.Computers[i].Fingerprint == fp {
			p.Computers[i].Name = name
			return done(nil)
		}
	}
	return done(fmt.Errorf("no computer with fingerprint %s", fp))
}

func (d *demo) UnregisterComputer(fp string) <-chan error {
	p := d.a.presence
	for i, c := range p.Computers {
		if c.Fingerprint == fp {
			p.Computers = append(p.Computers[:i], p.Computers[i+1:]...)
			return done(nil)
		}
	}
	return done(fmt.Errorf("unknown computer"))
}

// Connect starts a demo session right away, as if the user allowed it.
func (d *demo) Connect(fp, _ string) <-chan error {
	p := d.a.presence
	for i := range p.Computers {
		c := &p.Computers[i]
		if c.Fingerprint != fp {
			continue
		}
		if !c.Online {
			return done(fmt.Errorf("computer is not online"))
		}
		if c.Request != nil || c.InSessionWith != "" {
			return done(fmt.Errorf("computer is busy"))
		}
		c.InSessionWith = d.a.me
		d.a.addSession(d.startSession(c.Name, c.Fingerprint))
		return done(nil)
	}
	return done(fmt.Errorf("computer is not online"))
}

// connectFirst connects to the first online computer, for --page session.
func (d *demo) connectFirst() {
	online, _ := sortedComputers(d.a.presence)
	for _, c := range online {
		if c.Request == nil && c.InSessionWith == "" {
			d.a.connectComputer(c)
			return
		}
	}
}

// frame updates the fake presence once per frame: computers whose demo session
// ended are no longer in session.
func (d *demo) frame() {
	p := d.a.presence
	if p == nil {
		return
	}
	for i := range p.Computers {
		c := &p.Computers[i]
		if c.InSessionWith == d.a.me && d.a.sessionFor(c.Fingerprint) < 0 {
			c.InSessionWith = ""
		}
	}
}

func (d *demo) stop() {
	for _, f := range d.stops {
		f()
	}
}

type closers []io.Closer

func (cs closers) Close() error {
	for _, c := range cs {
		c.Close()
	}
	return nil
}

// startSession runs a session against an in-process fake computer.
func (d *demo) startSession(name, fp string) *session.Session {
	sa, sb := net.Pipe()
	ia, ib := net.Pipe()
	s := session.Run(session.Info{ComputerName: name, UserMachine: name, Fingerprint: fp}, sa, ia, closers{sa, ia}, session.ProtoCodec{})
	go runDemoComputer(sb, ib, name)
	d.stops = append(d.stops, s.Close)
	return s
}
