package views

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/desk/serverconn"
	"github.com/helpbuoy/helpbuoy/desk/session"
	"github.com/helpbuoy/helpbuoy/proto/appkey"
	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/proto/identity"
	"github.com/helpbuoy/helpbuoy/proto/serveraddr"
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
	// The demo plays a server inside the app, as most techies run it, with a
	// made-up address in the local network.
	a.cfg.ServerAddr = "localhost:5959"
	a.addr = serveraddr.Addr{Host: "localhost", Port: 5959}
	a.give.lan = netip.MustParseAddrPort("192.168.1.20:5959")
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
	keyFP := func(name string) string {
		k, _, _ := newKey(name)
		return ssh.FingerprintSHA256(k.PublicKey())
	}
	bob, bobHome, carol := keyFP("bob"), keyFP("bob-home"), keyFP("carol")
	p := &control.Presence{Techies: []control.TechieInfo{
		{Name: "techie", Online: true, Fingerprint: a.key.Fingerprint,
			Keys: []control.TechieKeyInfo{{Fingerprint: a.key.Fingerprint, Online: true, LastSeen: now}}},
		{Name: "bob", Online: true, Fingerprint: bob, Keys: []control.TechieKeyInfo{
			{Fingerprint: bob, Online: true, LastSeen: now},
			{Fingerprint: bobHome, LastSeen: now.Add(-20 * time.Hour)},
		}},
		{Name: "carol", Fingerprint: carol,
			Keys: []control.TechieKeyInfo{{Fingerprint: carol, LastSeen: now.Add(-3 * 24 * time.Hour)}}},
	}}
	anna := computer("anna@ANNA-LAPTOP", true)
	jan := computer("jan@KANTOOR-PC", true)
	other, _, _ := newKey("")
	jan.Listings = []control.ListingRequest{{Address: "help.other-example.com:443", Fingerprint: ssh.FingerprintSHA256(other.PublicKey()), RequestedAt: now.Add(-5 * time.Minute)}}
	oma := computer("oma@WOONKAMER", true)
	oma.Request = &control.RequestInfo{ID: "demo-3", Created: now.Add(-10 * time.Minute), State: control.RequestTaken, TakenBy: "bob"}
	oma.InSessionWith = "bob"
	piet := computer("piet@PIET-PC", true)
	lisa := computer("lisa@LISA-PC", false)
	kees := computer("kees@KEES-LAPTOP", false)
	kees.LastSeen = now.Add(-2 * time.Hour)
	p.Customers = []control.CustomerInfo{{ID: "demo-acme", Name: "Acme Ltd"}, {ID: "demo-family", Name: "Family"}}
	jan.Customer, piet.Customer, kees.Customer = "demo-acme", "demo-acme", "demo-acme"
	oma.Customer, anna.Customer = "demo-family", "demo-family"
	p.Computers = []control.ComputerInfo{piet, oma, anna, lisa, jan, kees}
	a.setPresence(p)
	a.setServerInfo(&control.ServerInfo{Company: "Example Inc", Phone: "+1 234 567 890", Description: "Monday to Friday 9:00 to 17:00"})
	return d
}

// done returns an already answered command result.
func done(err error) <-chan error {
	c := make(chan error, 1)
	c <- err
	return c
}

func (d *demo) RegisterComputer(line, name, customer string) <-chan error {
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
	p.Computers = append(p.Computers, control.ComputerInfo{Fingerprint: fp, Name: name, RegisteredBy: d.a.me, RegisteredAt: time.Now(), Customer: customer})
	return done(nil)
}

// AddCustomer adds a demo customer locally, with the server's rules.
func (d *demo) AddCustomer(name string) <-chan error {
	name = strings.TrimSpace(name)
	if !validCustomerName(name) {
		return done(fmt.Errorf("name must be 1 to %d printable characters", control.MaxCustomerName))
	}
	p := d.a.presence
	for _, cu := range p.Customers {
		if strings.EqualFold(cu.Name, name) {
			return done(fmt.Errorf("customer %q already exists", cu.Name))
		}
	}
	p.Customers = append(p.Customers, control.CustomerInfo{ID: control.NewRequestID()[:16], Name: name})
	return done(nil)
}

// RenameCustomer renames a demo customer locally.
func (d *demo) RenameCustomer(id, name string) <-chan error {
	name = strings.TrimSpace(name)
	p := d.a.presence
	for i, cu := range p.Customers {
		if cu.ID == id {
			p.Customers[i].Name = name
			return done(nil)
		}
	}
	return done(fmt.Errorf("no customer %q", id))
}

// RemoveCustomer removes a demo customer locally; its computers stay.
func (d *demo) RemoveCustomer(id string) <-chan error {
	p := d.a.presence
	for i, cu := range p.Customers {
		if cu.ID == id {
			p.Customers = append(p.Customers[:i:i], p.Customers[i+1:]...)
			for j := range p.Computers {
				if p.Computers[j].Customer == id {
					p.Computers[j].Customer = ""
				}
			}
			return done(nil)
		}
	}
	return done(fmt.Errorf("no customer %q", id))
}

// MoveComputer moves a demo computer to another customer locally.
func (d *demo) MoveComputer(fp, customer string) <-chan error {
	p := d.a.presence
	for i := range p.Computers {
		if p.Computers[i].Fingerprint == fp {
			p.Computers[i].Customer = customer
			return done(nil)
		}
	}
	return done(fmt.Errorf("no computer with fingerprint %s", fp))
}

// SetServerInfo changes who runs the demo server locally, with the server's
// rules.
func (d *demo) SetServerInfo(info control.ServerInfo) <-chan error {
	if problem, _ := checkInfo(info); problem != "" {
		return done(errors.New(problem))
	}
	d.a.setServerInfo(&info)
	return done(nil)
}

// DecideListing allows or refuses a demo listing request locally.
func (d *demo) DecideListing(fp string, req control.ListingRequest, allow bool) <-chan error {
	p := d.a.presence
	for i := range p.Computers {
		c := &p.Computers[i]
		if c.Fingerprint != fp {
			continue
		}
		for j, l := range c.Listings {
			if l.Address == req.Address && l.Fingerprint == req.Fingerprint {
				c.Listings = append(c.Listings[:j:j], c.Listings[j+1:]...)
				if allow {
					c.Approved = append(c.Approved, control.ApprovedServer{Address: l.Address, Fingerprint: l.Fingerprint})
				}
				return done(nil)
			}
		}
		return done(fmt.Errorf("no such listing request"))
	}
	return done(fmt.Errorf("no computer with fingerprint %s", fp))
}

// RegisterTechie adds a demo techie locally.
func (d *demo) RegisterTechie(line, name string) <-chan error {
	fp, err := d.newTechieKey(line)
	if err != nil {
		return done(err)
	}
	p := d.a.presence
	for _, h := range p.Techies {
		if h.Name == name {
			return done(fmt.Errorf("techie %q already exists", name))
		}
	}
	p.Techies = append(p.Techies, control.TechieInfo{Name: name, Fingerprint: fp, Keys: []control.TechieKeyInfo{{Fingerprint: fp}}})
	return done(nil)
}

// newTechieKey parses a key line for a demo techie and checks that no
// techie has it yet.
func (d *demo) newTechieKey(line string) (string, error) {
	key, _, err := identity.ParseLine(line)
	if err != nil {
		return "", err
	}
	fp := ssh.FingerprintSHA256(key)
	for _, h := range d.a.presence.Techies {
		for _, k := range h.Keys {
			if k.Fingerprint == fp {
				return "", fmt.Errorf("key %s is already a key of techie %q", fp, h.Name)
			}
		}
	}
	return fp, nil
}

// AddTechieKey adds a key to a demo techie locally.
func (d *demo) AddTechieKey(name, line string) <-chan error {
	fp, err := d.newTechieKey(line)
	if err != nil {
		return done(err)
	}
	p := d.a.presence
	for i := range p.Techies {
		if p.Techies[i].Name == name {
			p.Techies[i].Keys = append(p.Techies[i].Keys, control.TechieKeyInfo{Fingerprint: fp})
			return done(nil)
		}
	}
	return done(fmt.Errorf("no techie named %q", name))
}

// RemoveTechieKey removes a key of a demo techie locally, with the server's
// rules.
func (d *demo) RemoveTechieKey(name, fp string) <-chan error {
	p := d.a.presence
	for i := range p.Techies {
		h := &p.Techies[i]
		if h.Name != name {
			continue
		}
		j := slices.IndexFunc(h.Keys, func(k control.TechieKeyInfo) bool { return k.Fingerprint == fp })
		switch {
		case j < 0:
			return done(fmt.Errorf("techie %q has no key %s", name, fp))
		case h.Keys[j].Online && name == d.a.me:
			return done(fmt.Errorf("you cannot remove the key you are connected with"))
		case len(h.Keys) == 1:
			return done(fmt.Errorf("%s is the only key of techie %q; remove the techie instead", fp, name))
		}
		h.Keys = slices.Delete(slices.Clone(h.Keys), j, j+1)
		if h.Fingerprint == fp {
			h.Fingerprint = h.Keys[0].Fingerprint
		}
		return done(nil)
	}
	return done(fmt.Errorf("no techie named %q", name))
}

// UnregisterTechie removes a demo techie locally, with the server's rules.
func (d *demo) UnregisterTechie(name string) <-chan error {
	p := d.a.presence
	if name == d.a.me {
		return done(fmt.Errorf("you cannot remove yourself"))
	}
	for i, h := range p.Techies {
		if h.Name == name {
			p.Techies = append(p.Techies[:i], p.Techies[i+1:]...)
			return done(nil)
		}
	}
	return done(fmt.Errorf("no techie named %q", name))
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

// CancelRequest does nothing: demo Connects never wait.
func (d *demo) CancelRequest(string) {}

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
