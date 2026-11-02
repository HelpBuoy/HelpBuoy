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

	"github.com/mevdschee/help-me-win/helper/relayconn"
	"github.com/mevdschee/help-me-win/helper/session"
	"github.com/mevdschee/help-me-win/proto/appkey"
	"github.com/mevdschee/help-me-win/proto/control"
	"github.com/mevdschee/help-me-win/proto/identity"
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
	a.cfg.RelayAddr = "relay.example.com:2222"
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
	p := &control.Presence{Helpers: []control.HelperInfo{{Name: "helper", DisplayName: "Helper", Online: true}, {Name: "bob", DisplayName: "Bob", Online: true}}}
	anna := client("anna@ANNA-LAPTOP", true)
	anna.Request = &control.RequestInfo{ID: "demo-1", Created: now.Add(-3 * time.Minute), State: control.RequestOpen}
	jan := client("jan@KANTOOR-PC", true)
	jan.Request = &control.RequestInfo{ID: "demo-2", Created: now.Add(-40 * time.Second), State: control.RequestOpen}
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

func (d *demo) OpenRequest(id string) <-chan error {
	p := d.a.presence
	for i := range p.Clients {
		c := &p.Clients[i]
		if c.Request == nil || c.Request.ID != id {
			continue
		}
		if c.Request.State != control.RequestOpen {
			return done(fmt.Errorf("request is not open"))
		}
		c.Request = nil
		c.InSessionWith = d.a.me
		d.a.addSession(d.startSession(c.Name))
		return done(nil)
	}
	return done(fmt.Errorf("request is not open"))
}

// openFirstRequest opens the first open request, for --page session.
func (d *demo) openFirstRequest() {
	online, _ := sortedClients(d.a.presence)
	for _, c := range online {
		if c.Request != nil && c.Request.State == control.RequestOpen {
			d.a.openRequest(c)
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
		if c.InSessionWith == d.a.me && d.a.sessionFor(c.Name) < 0 {
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
func (d *demo) startSession(name string) *session.Session {
	sa, sb := net.Pipe()
	ia, ib := net.Pipe()
	s := session.Run(session.Info{ClientName: name, UserMachine: name}, sa, ia, closers{sa, ia}, session.ProtoCodec{})
	go runDemoClient(sb, ib, name)
	d.stops = append(d.stops, s.Close)
	return s
}
