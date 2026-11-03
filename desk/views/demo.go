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

	"github.com/mevdschee/helpbuoy/desk/relayconn"
	"github.com/mevdschee/helpbuoy/desk/session"
	"github.com/mevdschee/helpbuoy/proto/appkey"
	"github.com/mevdschee/helpbuoy/proto/control"
	"github.com/mevdschee/helpbuoy/proto/identity"
)

// demo fakes a relay with a few clients so the UI can be seen and tried
// without a network. Sessions are served by an in-process fake client
// (demoClient) through the real screen and input encodings.
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
	s, line, err := newKey("helper@demo")
	if err != nil {
		a.keyErr = err
		return d
	}
	a.key = &appkey.Key{Signer: s, Public: s.PublicKey(), Comment: "helper@demo", Line: line, Fingerprint: ssh.FingerprintSHA256(s.PublicKey())}
	hk, _, _ := newKey("")
	a.hostKey = hk.PublicKey()
	a.cfg.RelayAddr = "helpme.example.com:5939"
	a.status = relayconn.StatusEvent{Status: relayconn.StatusOnline, HostKey: a.hostKey}
	a.me = "helper"
	a.backend = d

	now := time.Now()
	client := func(name string, online bool) control.ClientInfo {
		k, _, _ := newKey(name)
		return control.ClientInfo{
			Fingerprint: ssh.FingerprintSHA256(k.PublicKey()), Name: name, Online: online,
			RegisteredBy: "helper", RegisteredAt: now.Add(-30 * 24 * time.Hour), LastSeen: now.Add(-26 * time.Hour),
		}
	}
	p := &control.Presence{Helpers: []control.HelperInfo{{Name: "helper", Online: true}, {Name: "bob", Online: true}}}
	anna := client("anna@ANNA-LAPTOP", true)
	jan := client("jan@KANTOOR-PC", true)
	oma := client("oma@WOONKAMER", true)
	oma.Request = &control.RequestInfo{ID: "demo-3", Created: now.Add(-10 * time.Minute), State: control.RequestTaken, TakenBy: "bob"}
	oma.InSessionWith = "bob"
	piet := client("piet@PIET-PC", true)
	lisa := client("lisa@LISA-PC", false)
	kees := client("kees@KEES-LAPTOP", false)
	kees.LastSeen = now.Add(-2 * time.Hour)
	p.Clients = []control.ClientInfo{piet, oma, anna, lisa, jan, kees}
	a.setPresence(p)
	return d
}

// done returns an already answered command result.
func done(err error) <-chan error {
	c := make(chan error, 1)
	c <- err
	return c
}

func (d *demo) RegisterClient(line, name string) <-chan error {
	key, comment, err := identity.ParseLine(line)
	if err != nil {
		return done(err)
	}
	if name == "" {
		name = comment
	}
	p := d.a.presence
	fp := ssh.FingerprintSHA256(key)
	for _, c := range p.Clients {
		if c.Fingerprint == fp {
			return done(fmt.Errorf("already registered as %s", c.Name))
		}
	}
	p.Clients = append(p.Clients, control.ClientInfo{Fingerprint: fp, Name: name, RegisteredBy: d.a.me, RegisteredAt: time.Now()})
	return done(nil)
}

// RegisterHelper adds a demo helper locally.
func (d *demo) RegisterHelper(line, name string) <-chan error {
	key, _, err := identity.ParseLine(line)
	if err != nil {
		return done(err)
	}
	p := d.a.presence
	fp := ssh.FingerprintSHA256(key)
	for _, h := range p.Helpers {
		if h.Name == name {
			return done(fmt.Errorf("helper %q already exists", name))
		}
		if h.Fingerprint == fp {
			return done(fmt.Errorf("key %s is already the key of helper %q", fp, h.Name))
		}
	}
	p.Helpers = append(p.Helpers, control.HelperInfo{Name: name, Fingerprint: fp})
	return done(nil)
}

// RenameClient renames a demo client locally, with the relay's rules.
func (d *demo) RenameClient(fp, name string) <-chan error {
	name = strings.TrimSpace(name)
	if !validClientName(name) {
		return done(fmt.Errorf("name must be 1 to %d printable characters", maxClientName))
	}
	p := d.a.presence
	for i := range p.Clients {
		if p.Clients[i].Fingerprint == fp {
			p.Clients[i].Name = name
			return done(nil)
		}
	}
	return done(fmt.Errorf("no client with fingerprint %s", fp))
}

func (d *demo) UnregisterClient(fp string) <-chan error {
	p := d.a.presence
	for i, c := range p.Clients {
		if c.Fingerprint == fp {
			p.Clients = append(p.Clients[:i], p.Clients[i+1:]...)
			return done(nil)
		}
	}
	return done(fmt.Errorf("unknown client"))
}

// Connect starts a demo session right away, as if the user allowed it.
func (d *demo) Connect(fp, _ string) <-chan error {
	p := d.a.presence
	for i := range p.Clients {
		c := &p.Clients[i]
		if c.Fingerprint != fp {
			continue
		}
		if !c.Online {
			return done(fmt.Errorf("client is not online"))
		}
		if c.Request != nil || c.InSessionWith != "" {
			return done(fmt.Errorf("client is busy"))
		}
		c.InSessionWith = d.a.me
		d.a.addSession(d.startSession(c.Name, c.Fingerprint))
		return done(nil)
	}
	return done(fmt.Errorf("client is not online"))
}

// connectFirst connects to the first online client, for --page session.
func (d *demo) connectFirst() {
	online, _ := sortedClients(d.a.presence)
	for _, c := range online {
		if c.Request == nil && c.InSessionWith == "" {
			d.a.connectClient(c)
			return
		}
	}
}

// frame updates the fake presence once per frame: clients whose demo session
// ended are no longer in session.
func (d *demo) frame() {
	p := d.a.presence
	if p == nil {
		return
	}
	for i := range p.Clients {
		c := &p.Clients[i]
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

// startSession runs a session against an in-process fake client.
func (d *demo) startSession(name, fp string) *session.Session {
	sa, sb := net.Pipe()
	ia, ib := net.Pipe()
	s := session.Run(session.Info{ClientName: name, UserMachine: name, Fingerprint: fp}, sa, ia, closers{sa, ia}, session.ProtoCodec{})
	go runDemoClient(sb, ib, name)
	d.stops = append(d.stops, s.Close)
	return s
}
