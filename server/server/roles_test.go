package server_test

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/server/fake"
	"github.com/helpbuoy/helpbuoy/server/hostkey"
	"github.com/helpbuoy/helpbuoy/server/registry"
	"github.com/helpbuoy/helpbuoy/server/server"
	"github.com/helpbuoy/helpbuoy/server/store"
)

type roleConn struct {
	role control.Role
	conn func(e *env) *fake.Conn
}

func roles() []roleConn {
	return []roleConn{
		{control.RoleTechie, func(e *env) *fake.Conn { return e.techie(uniq("techie")) }},
		{control.RoleComputer, func(e *env) *fake.Conn { c, _ := e.computer(uniq("user@pc")); return c }},
		{control.RolePending, func(e *env) *fake.Conn { c, _ := e.connect(fake.NewKey(), control.RolePending); return c }},
	}
}

func TestHello(t *testing.T) {
	e := newEnv(t, nil)
	hk := e.addTechie("alice")
	_, h := e.connect(hk, control.RoleTechie)
	if h.Name != "alice" || h.ServerVersion != "test" ||
		!h.ServerTime.Equal(e.clock.Now()) {
		t.Errorf("techie hello %+v", h)
	}
	_, h = e.connect(e.addComputer("anna@PC"), control.RoleComputer)
	if h.Name != "" {
		t.Errorf("computer hello tells the computer its name: %+v", h)
	}
	_, h = e.connect(fake.NewKey(), control.RolePending)
	if h.Name != "" {
		t.Errorf("pending hello %+v", h)
	}
}

func TestHostKeyStable(t *testing.T) {
	e := newEnv(t, nil)
	k1 := e.srv.HostKey()
	e.restart()
	if string(k1.Marshal()) != string(e.srv.HostKey().Marshal()) {
		t.Fatal("host key changed across restart")
	}
	s, err := hostkey.LoadOrCreate(e.dir)
	if err != nil || string(s.PublicKey().Marshal()) != string(k1.Marshal()) {
		t.Fatalf("hostkey file: %v", err)
	}
}

func TestGlobalRequests(t *testing.T) {
	for _, rc := range roles() {
		t.Run(string(rc.role), func(t *testing.T) {
			e := newEnv(t, nil)
			c := rc.conn(e)
			for _, typ := range []string{"tcpip-forward", "cancel-tcpip-forward", "streamlocal-forward@openssh.com",
				"no-more-sessions@openssh.com", "hostkeys-prove-00@openssh.com", "bogus"} {
				ok, _, err := c.Client.SendRequest(typ, true, []byte("x"))
				if err != nil || ok {
					t.Errorf("%s: ok=%v err=%v, want refused", typ, ok, err)
				}
			}
			ok, _, err := c.Client.SendRequest("keepalive@openssh.com", true, nil)
			if err != nil || !ok {
				t.Errorf("keepalive: ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestChannelTypes(t *testing.T) {
	for _, rc := range roles() {
		t.Run(string(rc.role), func(t *testing.T) {
			e := newEnv(t, nil)
			c := rc.conn(e)
			for _, typ := range []string{"session", "direct-tcpip", "direct-streamlocal@openssh.com", "x11",
				"forwarded-tcpip", "auth-agent@openssh.com", "bogus"} {
				_, _, err := c.Client.OpenChannel(typ, nil)
				if openReason(err) != ssh.UnknownChannelType {
					t.Errorf("%s: err=%v, want unknown channel type", typ, err)
				}
			}
			_, _, err := c.Client.OpenChannel(control.ChannelControl, nil)
			if openReason(err) != ssh.Prohibited {
				t.Errorf("second control channel: err=%v, want prohibited", err)
			}
			data := control.MarshalSessionChannelData(control.SessionChannelData{RequestID: "nope"})
			_, _, err = c.Client.OpenChannel(control.ChannelSession, data)
			if openReason(err) != ssh.Prohibited {
				t.Errorf("session without request: err=%v, want prohibited", err)
			}
			_, _, err = c.Client.OpenChannel(control.ChannelSession, []byte("garbage"))
			if openReason(err) != ssh.Prohibited {
				t.Errorf("session with bad data: err=%v, want prohibited", err)
			}
			assertOpen(t, c)
		})
	}
}

func TestControlChannelRequests(t *testing.T) {
	for _, rc := range roles() {
		t.Run(string(rc.role), func(t *testing.T) {
			e := newEnv(t, nil)
			c := rc.conn(e)
			for _, typ := range []string{"pty-req", "shell", "exec", "subsystem", "env", "x11-req",
				"auth-agent-req@openssh.com", "window-change", "signal", "bogus"} {
				ok, err := c.Ctrl.SendRequest(typ, true, ssh.Marshal(struct{ S string }{"sh"}))
				if err != nil || ok {
					t.Errorf("%s: ok=%v err=%v, want refused", typ, ok, err)
				}
			}
			assertOpen(t, c)
		})
	}
}

// TestMessagesPerRole sends every app->server message from every role. The
// allowed ones keep the connection open; the others end it with an Error.
func TestMessagesPerRole(t *testing.T) {
	msgs := []struct {
		name string
		m    any
		ok   map[control.Role]bool
	}{
		{"ComputerHello", &control.ComputerHello{UserMachine: "x@y"}, map[control.Role]bool{control.RoleComputer: true, control.RolePending: true, control.RoleTechie: true}},
		{"CancelRequest", &control.CancelRequest{RequestID: "x"}, map[control.Role]bool{control.RoleComputer: true, control.RoleTechie: true}},
		{"Connect", &control.Connect{Seq: 1, Fingerprint: "SHA256:x", RequestID: "x"}, map[control.Role]bool{control.RoleTechie: true}},
		{"RegisterComputer", &control.RegisterComputer{Seq: 1, PublicKey: "bad"}, map[control.Role]bool{control.RoleTechie: true}},
		{"UnregisterComputer", &control.UnregisterComputer{Seq: 1, Fingerprint: "SHA256:x"}, map[control.Role]bool{control.RoleTechie: true}},
		{"RenameComputer", &control.RenameComputer{Seq: 1, Fingerprint: "SHA256:x", Name: "x"}, map[control.Role]bool{control.RoleTechie: true}},
		{"RegisterTechie", &control.RegisterTechie{Seq: 1, PublicKey: "bad", Name: "x"}, map[control.Role]bool{control.RoleTechie: true}},
		{"UnregisterTechie", &control.UnregisterTechie{Seq: 1, Name: "nobody"}, map[control.Role]bool{control.RoleTechie: true}},
		{"SetServerInfo", &control.SetServerInfo{Seq: 1, Company: "bad\x00title"}, map[control.Role]bool{control.RoleTechie: true}},
		{"DecideListing", &control.DecideListing{Seq: 1, Computer: "SHA256:x"}, map[control.Role]bool{control.RoleTechie: true}},
		{"RequestListing", &control.RequestListing{Address: "bad address"}, map[control.Role]bool{control.RoleComputer: true}},
		// Server->app messages are never accepted from apps.
		{"Hello", &control.Hello{Role: control.RoleTechie}, nil},
		{"Presence", &control.Presence{}, nil},
		{"Reply", &control.Reply{}, nil},
		{"Registered", &control.Registered{}, nil},
		{"RequestState", &control.RequestStateMsg{}, nil},
		{"SessionReady", &control.SessionReady{}, nil},
		{"Error", &control.Error{}, nil},
		{"ServerInfo", &control.ServerInfo{}, nil},
		{"ApprovedServers", &control.ApprovedServers{}, nil},
		{"ListingRefused", &control.ListingRefused{}, nil},
	}
	for _, rc := range roles() {
		t.Run(string(rc.role), func(t *testing.T) {
			e := newEnv(t, nil)
			for _, mm := range msgs {
				c := rc.conn(e)
				if err := c.Send(mm.m); err != nil {
					t.Fatal(err)
				}
				if mm.ok[rc.role] {
					// Allowed: the connection stays and answers techie commands
					// (ComputerHello and CancelRequest have no reply).
					if rc.role == control.RoleTechie && mm.name != "ComputerHello" && mm.name != "CancelRequest" {
						rep := mustWait[control.Reply](t, c, nil)
						if rep.Seq != 1 || rep.Error == "" {
							t.Errorf("%s: reply %+v, want an error for the bogus argument", mm.name, rep)
						}
					}
					assertOpen(t, c)
				} else if msg := waitClosed(t, c); msg == "" {
					t.Errorf("%s from %s: closed without Error", mm.name, rc.role)
				}
				c.Close()
			}
		})
	}
}

func TestBadFrames(t *testing.T) {
	e := newEnv(t, nil)
	for _, frame := range [][]byte{
		{0, 0, 0, 2, 99, '{'}, // unknown type
		{0, 0, 0, 3, byte(control.MsgComputerHello), '{', '['}, // bad JSON
		{0, 0, 0, 0}, // empty frame
		{0, 1, 0, 0}, // larger than the app limit
	} {
		c, _ := e.connect(fake.NewKey(), control.RolePending)
		if _, err := c.Ctrl.Write(frame); err != nil {
			t.Fatal(err)
		}
		if msg := waitClosed(t, c); !strings.Contains(msg, "bad control message") {
			t.Errorf("frame %v: error %q", frame, msg)
		}
	}
}

func TestNoControlChannelTimesOut(t *testing.T) {
	e := newEnv(t, func(c *server.Config) { c.HandshakeTimeout = 200 * time.Millisecond })
	c, err := fake.Dial(e.addr, fake.NewKey(), fake.Options{NoControl: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	done := make(chan error, 1)
	go func() { done <- c.Client.Wait() }()
	select {
	case <-done:
	case <-time.After(wait):
		t.Fatal("connection without control channel was not closed")
	}
}

func TestPendingNotInPresence(t *testing.T) {
	e := newEnv(t, nil)
	h := e.techie("alice")
	k := fake.NewKey()
	e.connect(k, control.RolePending)
	// A later change produces a fresh snapshot; the pending key must not be in it.
	e.addComputer("bob@PC")
	p := presenceWhere(t, h, func(p *control.Presence) bool { return len(p.Computers) == 1 })
	if computerInfo(p, fp(k)).Fingerprint != "" {
		t.Fatal("pending key shown in Presence")
	}
}

func TestPresenceContent(t *testing.T) {
	e := newEnv(t, nil)
	e.addTechie("bob") // offline techie
	offline := e.addComputer("zed@OFF")
	h := e.techie("alice")
	c, ck := e.computer("anna@PC")
	if err := c.Send(&control.ComputerHello{UserMachine: "anna@PC-NOW"}); err != nil {
		t.Fatal(err)
	}
	p := presenceWhere(t, h, func(p *control.Presence) bool {
		ci := computerInfo(p, fp(ck))
		return ci != nil && ci.UserMachine == "anna@PC-NOW"
	})
	if len(p.Techies) != 2 || p.Techies[0].Name != "alice" || !p.Techies[0].Online ||
		p.Techies[1].Name != "bob" || p.Techies[1].Online {
		t.Errorf("techies %+v", p.Techies)
	}
	if len(p.Computers) != 2 || p.Computers[0].Name != "anna@PC" || p.Computers[1].Name != "zed@OFF" {
		t.Fatalf("computers %+v", p.Computers)
	}
	on, off := computerInfo(p, fp(ck)), computerInfo(p, fp(offline))
	if !on.Online || on.RegisteredBy != "admin" || on.Request != nil || on.InSessionWith != "" {
		t.Errorf("online computer %+v", on)
	}
	if off.Online || off.UserMachine != "" || !off.RegisteredAt.Equal(e.clock.Now()) {
		t.Errorf("offline computer %+v", off)
	}
	c.Close()
	presenceWhere(t, h, func(p *control.Presence) bool { return !computerInfo(p, fp(ck)).Online })
}

func TestPromotion(t *testing.T) {
	e := newEnv(t, nil)
	h := e.techie("alice")
	k := fake.NewKey()
	pc, _ := e.connect(k, control.RolePending)

	// Bad registrations are answered with an error.
	for i, pk := range []string{
		"garbage",
		"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQC7 user@host",
		keyLine(k, ""), // no comment and no name
	} {
		h.Send(&control.RegisterComputer{Seq: uint32(10 + i), PublicKey: pk})
		if rep := mustWait[control.Reply](t, h, nil); rep.Seq != uint32(10+i) || rep.Error == "" {
			t.Errorf("register %q: reply %+v", pk, rep)
		}
	}

	h.Send(&control.RegisterComputer{Seq: 1, PublicKey: "  " + keyLine(k, "anna@PC") + "\n"})
	if rep := mustWait[control.Reply](t, h, nil); rep.Seq != 1 || rep.Error != "" {
		t.Fatalf("reply %+v", rep)
	}
	mustWait[control.Registered](t, pc, nil)
	p := presenceWhere(t, h, func(p *control.Presence) bool {
		ci := computerInfo(p, fp(k))
		return ci != nil && ci.Online
	})
	if ci := computerInfo(p, fp(k)); ci.RegisteredBy != "alice" || ci.Name != "anna@PC" || ci.UserMachine != "user@machine" {
		t.Errorf("computer info %+v", ci)
	}
	// The same connection now acts as a computer: techies can connect to it.
	if msg := connect(t, h, 10, fp(k), control.NewRequestID()); msg != "" {
		t.Errorf("connect to promoted computer: %q", msg)
	}
	mustWait[control.SessionReady](t, pc, nil)

	// Registering again, or registering a techie key, fails.
	h.Send(&control.RegisterComputer{Seq: 2, PublicKey: keyLine(k, "anna@PC")})
	if rep := mustWait[control.Reply](t, h, nil); rep.Error == "" {
		t.Error("duplicate registration accepted")
	}
	hk := e.addTechie("bob")
	h.Send(&control.RegisterComputer{Seq: 3, PublicKey: keyLine(hk, "bob@PC")})
	if rep := mustWait[control.Reply](t, h, nil); rep.Error == "" {
		t.Error("techie key registered as computer")
	}
	// A name overrides the key comment.
	k2 := fake.NewKey()
	h.Send(&control.RegisterComputer{Seq: 4, PublicKey: keyLine(k2, ""), Name: "carl@PC"})
	if rep := mustWait[control.Reply](t, h, nil); rep.Error != "" {
		t.Errorf("register with name: %+v", rep)
	}
	e.connect(k2, control.RoleComputer)
	presenceWhere(t, h, func(p *control.Presence) bool {
		ci := computerInfo(p, fp(k2))
		return ci != nil && ci.Name == "carl@PC" && ci.Online
	})
}

func TestReconnectReplaces(t *testing.T) {
	e := newEnv(t, nil)
	hk := e.addTechie("alice")
	h1, _ := e.connect(hk, control.RoleTechie)
	h2, _ := e.connect(hk, control.RoleTechie)
	if msg := waitClosed(t, h1); !strings.Contains(msg, "replaced") {
		t.Errorf("old connection error %q", msg)
	}
	assertOpen(t, h2)

	ck := e.addComputer("anna@PC")
	c1, _ := e.connect(ck, control.RoleComputer)
	if msg := connect(t, h2, 1, fp(ck), control.NewRequestID()); msg != "" {
		t.Fatal(msg)
	}
	mustWait[control.SessionReady](t, c1, nil)
	c2, _ := e.connect(ck, control.RoleComputer)
	waitClosed(t, c1)
	// The old connection's request went away with it.
	p := presenceWhere(t, h2, func(p *control.Presence) bool {
		ci := computerInfo(p, fp(ck))
		return ci != nil && ci.Online && ci.Request == nil
	})
	_ = p
	assertOpen(t, c2)

	pk := fake.NewKey()
	p1, _ := e.connect(pk, control.RolePending)
	p2, _ := e.connect(pk, control.RolePending)
	waitClosed(t, p1)
	assertOpen(t, p2)
}

func TestRemoval(t *testing.T) {
	e := newEnv(t, nil)
	hk := e.addTechie("alice")
	h, _ := e.connect(hk, control.RoleTechie)
	h2 := e.techie("bob")
	c, ck := e.computer("anna@PC")

	// A techie removes a computer: the computer is disconnected at once and is
	// pending when it comes back.
	h.Send(&control.UnregisterComputer{Seq: 7, Fingerprint: fp(ck)})
	if rep := mustWait[control.Reply](t, h, nil); rep.Seq != 7 || rep.Error != "" {
		t.Fatalf("reply %+v", rep)
	}
	if msg := waitClosed(t, c); msg == "" {
		t.Error("removed computer got no Error")
	}
	e.connect(ck, control.RolePending)
	h.Send(&control.UnregisterComputer{Seq: 8, Fingerprint: fp(ck)})
	if rep := mustWait[control.Reply](t, h, nil); rep.Error == "" {
		t.Error("removing an unknown computer succeeded")
	}

	// A techie cannot remove itself, nor an unknown techie; removing another
	// techie disconnects it at once.
	h.Send(&control.UnregisterTechie{Seq: 9, Name: "alice"})
	if rep := mustWait[control.Reply](t, h, nil); rep.Seq != 9 || rep.Error == "" {
		t.Fatalf("removing yourself: reply %+v", rep)
	}
	h.Send(&control.UnregisterTechie{Seq: 10, Name: "nobody"})
	if rep := mustWait[control.Reply](t, h, nil); rep.Error == "" {
		t.Error("removing an unknown techie succeeded")
	}
	h3k := e.addTechie("carol")
	h3, _ := e.connect(h3k, control.RoleTechie)
	h.Send(&control.UnregisterTechie{Seq: 11, Name: "carol"})
	if rep := mustWait[control.Reply](t, h, nil); rep.Seq != 11 || rep.Error != "" {
		t.Fatalf("reply %+v", rep)
	}
	if msg := waitClosed(t, h3); msg == "" {
		t.Error("removed techie got no Error")
	}
	e.connect(h3k, control.RolePending)

	// The admin removes a techie through state.json (as the CLI does,
	// without telling the server): it is kicked by the reload poll.
	if _, _, err := e.st.Update(func(st *store.State) error { return st.RemoveTechie("alice") }); err != nil {
		t.Fatal(err)
	}
	if msg := waitClosed(t, h); msg == "" {
		t.Error("removed techie got no Error")
	}
	presenceWhere(t, h2, func(p *control.Presence) bool { return len(p.Techies) == 1 })
	e.connect(hk, control.RolePending)

	// The admin removes a computer through state.json.
	c3, ck3 := e.computer("carl@PC")
	if _, _, err := e.st.Update(func(st *store.State) error { _, err := st.RemoveComputer(fp(ck3)); return err }); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, c3)
}

func TestStateReload(t *testing.T) {
	e := newEnv(t, nil)
	h := e.techie("alice")
	k := fake.NewKey()
	pc, _ := e.connect(k, control.RolePending)
	// CLI-style changes, picked up by the poll only.
	hk := fake.NewKey()
	if _, _, err := e.st.Update(func(st *store.State) error { return st.AddTechie("bob", keyLine(hk, "")) }); err != nil {
		t.Fatal(err)
	}
	presenceWhere(t, h, func(p *control.Presence) bool { return len(p.Techies) == 2 })
	e.connect(hk, control.RoleTechie)
	// A computer added outside the control protocol promotes its pending connection.
	if _, _, err := e.st.Update(func(st *store.State) error {
		_, _, err := st.AddComputer(keyLine(k, "anna@PC"), "", "admin", time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	mustWait[control.Registered](t, pc, nil)
	// A broken file is ignored and the old state kept.
	if err := writeFile(e.st.Path(), "{not json"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	assertOpen(t, h)
	assertOpen(t, pc)
}

func TestRestartKeepsRegistrations(t *testing.T) {
	e := newEnv(t, nil)
	hk := e.addTechie("alice")
	h, _ := e.connect(hk, control.RoleTechie)
	k := fake.NewKey()
	h.Send(&control.RegisterComputer{Seq: 1, PublicKey: keyLine(k, "anna@PC")})
	if rep := mustWait[control.Reply](t, h, nil); rep.Error != "" {
		t.Fatal(rep.Error)
	}
	c, _ := e.connect(k, control.RoleComputer)
	c.Close()
	e.restart()
	e.connect(k, control.RoleComputer) // still registered after the restart
	h, _ = e.connect(hk, control.RoleTechie)
	p := mustWait[control.Presence](t, h, nil)
	ci := computerInfo(p, fp(k))
	if ci == nil || ci.RegisteredBy != "alice" || ci.LastSeen.IsZero() {
		t.Errorf("computer after restart %+v", ci)
	}
}

func TestKeepaliveDropsDeadPeer(t *testing.T) {
	e := newEnv(t, func(c *server.Config) { c.KeepaliveInterval = 30 * time.Millisecond })
	h := e.techie("alice")
	ck := e.addComputer("anna@PC")
	px := newProxy(t, e.addr)
	c, err := fake.Dial(px.addr(), ck, fake.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	presenceWhere(t, h, func(p *control.Presence) bool { return computerInfo(p, fp(ck)).Online })
	// A live peer survives several keepalive rounds.
	time.Sleep(200 * time.Millisecond)
	assertOpen(t, c)
	px.freeze()
	presenceWhere(t, h, func(p *control.Presence) bool { return !computerInfo(p, fp(ck)).Online })
}

func TestPendingIdleTimeout(t *testing.T) {
	e := newEnv(t, nil)
	pc, _ := e.connect(fake.NewKey(), control.RolePending)
	e.clock.Advance(23 * time.Hour)
	time.Sleep(50 * time.Millisecond)
	assertOpen(t, pc)
	e.clock.Advance(time.Hour)
	if msg := waitClosed(t, pc); !strings.Contains(msg, "timed out") {
		t.Errorf("error %q", msg)
	}
}

func TestPasswordAuthRefused(t *testing.T) {
	e := newEnv(t, nil)
	cfg := &ssh.ClientConfig{
		User:            "helpbuoy",
		Auth:            []ssh.AuthMethod{ssh.Password("x"), ssh.KeyboardInteractive(func(string, string, []string, []bool) ([]string, error) { return nil, nil })},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         wait,
	}
	if c, err := ssh.Dial("tcp", e.addr, cfg); err == nil {
		c.Close()
		t.Fatal("password login accepted")
	}
}

func TestRenameComputer(t *testing.T) {
	e := newEnv(t, nil)
	h := e.techie("alice")
	h2 := e.techie("bob")
	_, ck := e.computer("anna@PC")

	// A techie renames a computer: every techie's Presence shows the new name,
	// and the stored key line carries it as its comment.
	h.Send(&control.RenameComputer{Seq: 3, Fingerprint: fp(ck), Name: "  Anna laptop  "})
	if rep := mustWait[control.Reply](t, h, nil); rep.Seq != 3 || rep.Error != "" {
		t.Fatalf("reply %+v", rep)
	}
	presenceWhere(t, h2, func(p *control.Presence) bool {
		return len(p.Computers) == 1 && p.Computers[0].Name == "Anna laptop" && p.Computers[0].Online
	})
	idx, _, err := e.st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if c := idx.State.Computers[0]; c.Name != "Anna laptop" || !strings.HasSuffix(c.PublicKey, " Anna laptop") {
		t.Fatalf("stored %+v", c)
	}

	// Bad names and unknown computers are refused with a Reply error.
	for i, m := range []*control.RenameComputer{
		{Seq: 4, Fingerprint: fp(ck), Name: "   "},
		{Seq: 5, Fingerprint: fp(ck), Name: "bad\x00name"},
		{Seq: 6, Fingerprint: "SHA256:nope", Name: "x"},
	} {
		h.Send(m)
		if rep := mustWait[control.Reply](t, h, nil); rep.Seq != m.Seq || rep.Error == "" {
			t.Errorf("case %d: reply %+v, want an error", i, rep)
		}
	}
}

// TestSetServerInfo: a techie sets who runs the server; every connection
// gets the new ServerInfo, it is stored, and new connections get it after
// Hello. Bad values are refused.
func TestSetServerInfo(t *testing.T) {
	e := newEnv(t, nil)
	h := e.techie("alice")
	cl, _ := e.computer("anna@PC")
	mustWait[control.ServerInfo](t, cl, func(m *control.ServerInfo) bool { return *m == testInfo })

	h.Send(&control.SetServerInfo{Seq: 2, Address: "Help.Example.com", Company: " Other Inc ", Phone: "+31 20 123 4567", Description: "Mon-Fri 9-17"})
	if rep := mustWait[control.Reply](t, h, func(r *control.Reply) bool { return r.Seq == 2 }); rep.Error != "" {
		t.Fatalf("set: %s", rep.Error)
	}
	wantInfo := control.ServerInfo{Address: "help.example.com:5959", Company: "Other Inc", Phone: "+31 20 123 4567", Description: "Mon-Fri 9-17"}
	want := func(m *control.ServerInfo) bool { return *m == wantInfo }
	mustWait[control.ServerInfo](t, cl, want)
	idx, _, err := e.st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if idx.State.Info() != wantInfo {
		t.Fatalf("stored %+v", idx.State.Info())
	}
	h2 := e.techie("bob")
	mustWait[control.ServerInfo](t, h2, want)

	for i, m := range []*control.SetServerInfo{
		{Seq: 3, Company: strings.Repeat("x", 65), Phone: "1"},
		{Seq: 4, Company: "A", Phone: "two\nlines"},
		{Seq: 5, Company: "", Phone: "1"},
		{Seq: 6, Company: "A", Phone: ""},
		{Seq: 7, Company: "A", Phone: "1", Address: "not an address!"},
	} {
		h.Send(m)
		if rep := mustWait[control.Reply](t, h, func(r *control.Reply) bool { return r.Seq == m.Seq }); rep.Error == "" {
			t.Errorf("case %d: no error", i)
		}
	}
}

// TestNotSetUp: a server without a company name and phone number refuses
// computers and pending keys with an Error, but lets techies in.
func TestNotSetUp(t *testing.T) {
	e := newEnv(t, nil)
	e.update(func(st *store.State) error {
		st.Company, st.Phone = "", ""
		return nil
	})
	ck := e.addComputer("anna@PC")
	for _, k := range []ssh.Signer{ck, fake.NewKey()} {
		c := e.dial(k)
		if msg := waitClosed(t, c); msg != registry.ReasonNotSetUp {
			t.Fatalf("closed with %q, want the not set up Error", msg)
		}
	}
	e.techie("alice")
}

// TestListing: a computer asks to approve another server, techies see the
// request in Presence, and allowing it sends the computer its new approved
// list, also after reconnecting. Refusing sends ListingRefused.
func TestListing(t *testing.T) {
	e := newEnv(t, nil)
	h := e.techie("alice")
	cl, ck := e.computer("anna@PC")
	mustWait[control.ApprovedServers](t, cl, func(m *control.ApprovedServers) bool { return len(m.Servers) == 0 })
	other := fp(fake.NewKey())
	cl.Send(&control.RequestListing{Address: "Vendor.example.com", Fingerprint: other})
	p := presenceWhere(t, h, func(p *control.Presence) bool { return len(p.Computers) == 1 && len(p.Computers[0].Listings) == 1 })
	l := p.Computers[0].Listings[0]
	if l.Address != "vendor.example.com:5959" || l.Fingerprint != other || l.RequestedAt.IsZero() {
		t.Fatalf("listing %+v", l)
	}
	// Asking again changes nothing.
	cl.Send(&control.RequestListing{Address: "vendor.example.com", Fingerprint: other})

	h.Send(&control.DecideListing{Seq: 1, Computer: fp(ck), Address: l.Address, Fingerprint: other, Allow: true})
	if rep := mustWait[control.Reply](t, h, func(r *control.Reply) bool { return r.Seq == 1 }); rep.Error != "" {
		t.Fatalf("allow: %s", rep.Error)
	}
	approved := control.ApprovedServer{Address: "vendor.example.com:5959", Fingerprint: other}
	mustWait[control.ApprovedServers](t, cl, func(m *control.ApprovedServers) bool {
		return len(m.Servers) == 1 && m.Servers[0] == approved
	})
	p = presenceWhere(t, h, func(p *control.Presence) bool { return len(p.Computers[0].Listings) == 0 })
	if len(p.Computers[0].Approved) != 1 || p.Computers[0].Approved[0] != approved {
		t.Fatalf("approved %+v", p.Computers[0].Approved)
	}
	// Deciding a request that is not open fails.
	h.Send(&control.DecideListing{Seq: 2, Computer: fp(ck), Address: l.Address, Fingerprint: other, Allow: true})
	if rep := mustWait[control.Reply](t, h, func(r *control.Reply) bool { return r.Seq == 2 }); rep.Error == "" {
		t.Fatal("deciding twice succeeded")
	}

	// A reconnect gets the list after Hello.
	cl.Close()
	cl2, _ := e.connect(ck, control.RoleComputer)
	mustWait[control.ApprovedServers](t, cl2, func(m *control.ApprovedServers) bool { return len(m.Servers) == 1 })

	// Refusing.
	third := fp(fake.NewKey())
	cl2.Send(&control.RequestListing{Address: "scam.example.com", Fingerprint: third})
	presenceWhere(t, h, func(p *control.Presence) bool { return len(p.Computers[0].Listings) == 1 })
	h.Send(&control.DecideListing{Seq: 3, Computer: fp(ck), Address: "scam.example.com:5959", Fingerprint: third})
	if rep := mustWait[control.Reply](t, h, func(r *control.Reply) bool { return r.Seq == 3 }); rep.Error != "" {
		t.Fatalf("refuse: %s", rep.Error)
	}
	mustWait[control.ListingRefused](t, cl2, func(m *control.ListingRefused) bool { return m.Fingerprint == third })
	p = presenceWhere(t, h, func(p *control.Presence) bool { return len(p.Computers[0].Listings) == 0 })
	if len(p.Computers[0].Approved) != 1 {
		t.Fatalf("refusing changed the approved list: %+v", p.Computers[0].Approved)
	}
}

// TestListingPromoted: a pending key that gets registered receives its
// (empty) approved list after Registered.
func TestListingPromoted(t *testing.T) {
	e := newEnv(t, nil)
	h := e.techie("alice")
	k := fake.NewKey()
	pc, _ := e.connect(k, control.RolePending)
	h.Send(&control.RegisterComputer{Seq: 1, PublicKey: keyLine(k, "anna@PC")})
	mustWait[control.Registered](t, pc, func(*control.Registered) bool { return true })
	mustWait[control.ApprovedServers](t, pc, func(*control.ApprovedServers) bool { return true })
}

// TestRegisterTechie: a techie adds another techie, who can then log in as
// one; duplicates, bad names and computer keys are refused.
func TestRegisterTechie(t *testing.T) {
	e := newEnv(t, nil)
	h := e.techie("alice")
	bk := fake.NewKey()
	h.Send(&control.RegisterTechie{Seq: 1, PublicKey: keyLine(bk, "bob@pc"), Name: "bob"})
	if rep := mustWait[control.Reply](t, h, func(r *control.Reply) bool { return r.Seq == 1 }); rep.Error != "" {
		t.Fatalf("register: %s", rep.Error)
	}
	p := presenceWhere(t, h, func(p *control.Presence) bool { return len(p.Techies) == 2 })
	if p.Techies[1].Name != "bob" || p.Techies[1].Fingerprint != fp(bk) {
		t.Errorf("techies %+v", p.Techies)
	}
	if _, hello := e.connect(bk, control.RoleTechie); hello.Name != "bob" {
		t.Errorf("hello %+v", hello)
	}
	ck := e.addComputer("anna@PC")
	for i, m := range []*control.RegisterTechie{
		{Seq: 2, PublicKey: keyLine(bk, ""), Name: "bob2"},           // key already a techie
		{Seq: 3, PublicKey: keyLine(fake.NewKey(), ""), Name: "bob"}, // name taken
		{Seq: 4, PublicKey: keyLine(fake.NewKey(), ""), Name: "with space"},
		{Seq: 5, PublicKey: keyLine(ck, ""), Name: "anna"}, // a computer key
	} {
		h.Send(m)
		if rep := mustWait[control.Reply](t, h, func(r *control.Reply) bool { return r.Seq == m.Seq }); rep.Error == "" {
			t.Errorf("case %d accepted", i)
		}
	}
}
