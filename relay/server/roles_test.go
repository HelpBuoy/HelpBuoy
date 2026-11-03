package server_test

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/helpbuoy/proto/control"
	"github.com/mevdschee/helpbuoy/relay/fake"
	"github.com/mevdschee/helpbuoy/relay/hostkey"
	"github.com/mevdschee/helpbuoy/relay/server"
	"github.com/mevdschee/helpbuoy/relay/store"
)

type roleConn struct {
	role control.Role
	conn func(e *env) *fake.Conn
}

func roles() []roleConn {
	return []roleConn{
		{control.RoleAgent, func(e *env) *fake.Conn { return e.agent(uniq("agent")) }},
		{control.RoleClient, func(e *env) *fake.Conn { c, _ := e.client(uniq("user@pc")); return c }},
		{control.RolePending, func(e *env) *fake.Conn { c, _ := e.connect(fake.NewKey(), control.RolePending); return c }},
	}
}

func TestHello(t *testing.T) {
	e := newEnv(t, nil)
	hk := e.addAgent("alice")
	_, h := e.connect(hk, control.RoleAgent)
	if h.Name != "alice" || h.RelayVersion != "test" ||
		!h.ServerTime.Equal(e.clock.Now()) {
		t.Errorf("agent hello %+v", h)
	}
	_, h = e.connect(e.addClient("anna@PC"), control.RoleClient)
	if h.Name != "" {
		t.Errorf("client hello tells the client its name: %+v", h)
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

// TestMessagesPerRole sends every app->relay message from every role. The
// allowed ones keep the connection open; the others end it with an Error.
func TestMessagesPerRole(t *testing.T) {
	msgs := []struct {
		name string
		m    any
		ok   map[control.Role]bool
	}{
		{"ClientHello", &control.ClientHello{UserMachine: "x@y"}, map[control.Role]bool{control.RoleClient: true, control.RolePending: true, control.RoleAgent: true}},
		{"CancelRequest", &control.CancelRequest{RequestID: "x"}, map[control.Role]bool{control.RoleClient: true}},
		{"Connect", &control.Connect{Seq: 1, Fingerprint: "SHA256:x", RequestID: "x"}, map[control.Role]bool{control.RoleAgent: true}},
		{"RegisterClient", &control.RegisterClient{Seq: 1, PublicKey: "bad"}, map[control.Role]bool{control.RoleAgent: true}},
		{"UnregisterClient", &control.UnregisterClient{Seq: 1, Fingerprint: "SHA256:x"}, map[control.Role]bool{control.RoleAgent: true}},
		{"RenameClient", &control.RenameClient{Seq: 1, Fingerprint: "SHA256:x", Name: "x"}, map[control.Role]bool{control.RoleAgent: true}},
		{"RegisterAgent", &control.RegisterAgent{Seq: 1, PublicKey: "bad", Name: "x"}, map[control.Role]bool{control.RoleAgent: true}},
		// Relay->app messages are never accepted from apps.
		{"Hello", &control.Hello{Role: control.RoleAgent}, nil},
		{"Presence", &control.Presence{}, nil},
		{"Reply", &control.Reply{}, nil},
		{"Registered", &control.Registered{}, nil},
		{"RequestState", &control.RequestStateMsg{}, nil},
		{"SessionReady", &control.SessionReady{}, nil},
		{"Error", &control.Error{}, nil},
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
					// Allowed: the connection stays and answers agent commands.
					if rc.role == control.RoleAgent && mm.name != "ClientHello" {
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
		{0, 0, 0, 2, 99, '{'},                                // unknown type
		{0, 0, 0, 3, byte(control.MsgClientHello), '{', '['}, // bad JSON
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
	h := e.agent("alice")
	k := fake.NewKey()
	e.connect(k, control.RolePending)
	// A later change produces a fresh snapshot; the pending key must not be in it.
	e.addClient("bob@PC")
	p := presenceWhere(t, h, func(p *control.Presence) bool { return len(p.Clients) == 1 })
	if clientInfo(p, fp(k)).Fingerprint != "" {
		t.Fatal("pending key shown in Presence")
	}
}

func TestPresenceContent(t *testing.T) {
	e := newEnv(t, nil)
	e.addAgent("bob") // offline agent
	offline := e.addClient("zed@OFF")
	h := e.agent("alice")
	c, ck := e.client("anna@PC")
	if err := c.Send(&control.ClientHello{UserMachine: "anna@PC-NOW"}); err != nil {
		t.Fatal(err)
	}
	p := presenceWhere(t, h, func(p *control.Presence) bool {
		ci := clientInfo(p, fp(ck))
		return ci != nil && ci.UserMachine == "anna@PC-NOW"
	})
	if len(p.Agents) != 2 || p.Agents[0].Name != "alice" || !p.Agents[0].Online ||
		p.Agents[1].Name != "bob" || p.Agents[1].Online {
		t.Errorf("agents %+v", p.Agents)
	}
	if len(p.Clients) != 2 || p.Clients[0].Name != "anna@PC" || p.Clients[1].Name != "zed@OFF" {
		t.Fatalf("clients %+v", p.Clients)
	}
	on, off := clientInfo(p, fp(ck)), clientInfo(p, fp(offline))
	if !on.Online || on.RegisteredBy != "admin" || on.Request != nil || on.InSessionWith != "" {
		t.Errorf("online client %+v", on)
	}
	if off.Online || off.UserMachine != "" || !off.RegisteredAt.Equal(e.clock.Now()) {
		t.Errorf("offline client %+v", off)
	}
	c.Close()
	presenceWhere(t, h, func(p *control.Presence) bool { return !clientInfo(p, fp(ck)).Online })
}

func TestPromotion(t *testing.T) {
	e := newEnv(t, nil)
	h := e.agent("alice")
	k := fake.NewKey()
	pc, _ := e.connect(k, control.RolePending)

	// Bad registrations are answered with an error.
	for i, pk := range []string{
		"garbage",
		"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQC7 user@host",
		keyLine(k, ""), // no comment and no name
	} {
		h.Send(&control.RegisterClient{Seq: uint32(10 + i), PublicKey: pk})
		if rep := mustWait[control.Reply](t, h, nil); rep.Seq != uint32(10+i) || rep.Error == "" {
			t.Errorf("register %q: reply %+v", pk, rep)
		}
	}

	h.Send(&control.RegisterClient{Seq: 1, PublicKey: "  " + keyLine(k, "anna@PC") + "\n"})
	if rep := mustWait[control.Reply](t, h, nil); rep.Seq != 1 || rep.Error != "" {
		t.Fatalf("reply %+v", rep)
	}
	mustWait[control.Registered](t, pc, nil)
	p := presenceWhere(t, h, func(p *control.Presence) bool {
		ci := clientInfo(p, fp(k))
		return ci != nil && ci.Online
	})
	if ci := clientInfo(p, fp(k)); ci.RegisteredBy != "alice" || ci.Name != "anna@PC" || ci.UserMachine != "user@machine" {
		t.Errorf("client info %+v", ci)
	}
	// The same connection now acts as a client: agents can connect to it.
	if msg := connect(t, h, 10, fp(k), control.NewRequestID()); msg != "" {
		t.Errorf("connect to promoted client: %q", msg)
	}
	mustWait[control.SessionReady](t, pc, nil)

	// Registering again, or registering an agent key, fails.
	h.Send(&control.RegisterClient{Seq: 2, PublicKey: keyLine(k, "anna@PC")})
	if rep := mustWait[control.Reply](t, h, nil); rep.Error == "" {
		t.Error("duplicate registration accepted")
	}
	hk := e.addAgent("bob")
	h.Send(&control.RegisterClient{Seq: 3, PublicKey: keyLine(hk, "bob@PC")})
	if rep := mustWait[control.Reply](t, h, nil); rep.Error == "" {
		t.Error("agent key registered as client")
	}
	// A name overrides the key comment.
	k2 := fake.NewKey()
	h.Send(&control.RegisterClient{Seq: 4, PublicKey: keyLine(k2, ""), Name: "carl@PC"})
	if rep := mustWait[control.Reply](t, h, nil); rep.Error != "" {
		t.Errorf("register with name: %+v", rep)
	}
	e.connect(k2, control.RoleClient)
	presenceWhere(t, h, func(p *control.Presence) bool {
		ci := clientInfo(p, fp(k2))
		return ci != nil && ci.Name == "carl@PC" && ci.Online
	})
}

func TestReconnectReplaces(t *testing.T) {
	e := newEnv(t, nil)
	hk := e.addAgent("alice")
	h1, _ := e.connect(hk, control.RoleAgent)
	h2, _ := e.connect(hk, control.RoleAgent)
	if msg := waitClosed(t, h1); !strings.Contains(msg, "replaced") {
		t.Errorf("old connection error %q", msg)
	}
	assertOpen(t, h2)

	ck := e.addClient("anna@PC")
	c1, _ := e.connect(ck, control.RoleClient)
	if msg := connect(t, h2, 1, fp(ck), control.NewRequestID()); msg != "" {
		t.Fatal(msg)
	}
	mustWait[control.SessionReady](t, c1, nil)
	c2, _ := e.connect(ck, control.RoleClient)
	waitClosed(t, c1)
	// The old connection's request went away with it.
	p := presenceWhere(t, h2, func(p *control.Presence) bool {
		ci := clientInfo(p, fp(ck))
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
	hk := e.addAgent("alice")
	h, _ := e.connect(hk, control.RoleAgent)
	h2 := e.agent("bob")
	c, ck := e.client("anna@PC")

	// An agent removes a client: the client is disconnected at once and is
	// pending when it comes back.
	h.Send(&control.UnregisterClient{Seq: 7, Fingerprint: fp(ck)})
	if rep := mustWait[control.Reply](t, h, nil); rep.Seq != 7 || rep.Error != "" {
		t.Fatalf("reply %+v", rep)
	}
	if msg := waitClosed(t, c); msg == "" {
		t.Error("removed client got no Error")
	}
	e.connect(ck, control.RolePending)
	h.Send(&control.UnregisterClient{Seq: 8, Fingerprint: fp(ck)})
	if rep := mustWait[control.Reply](t, h, nil); rep.Error == "" {
		t.Error("removing an unknown client succeeded")
	}

	// The admin removes an agent through state.json (as the CLI does,
	// without telling the relay): it is kicked by the reload poll.
	if _, _, err := e.st.Update(func(st *store.State) error { return st.RemoveAgent("alice") }); err != nil {
		t.Fatal(err)
	}
	if msg := waitClosed(t, h); msg == "" {
		t.Error("removed agent got no Error")
	}
	presenceWhere(t, h2, func(p *control.Presence) bool { return len(p.Agents) == 1 })
	e.connect(hk, control.RolePending)

	// The admin removes a client through state.json.
	c3, ck3 := e.client("carl@PC")
	if _, _, err := e.st.Update(func(st *store.State) error { _, err := st.RemoveClient(fp(ck3)); return err }); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, c3)
}

func TestStateReload(t *testing.T) {
	e := newEnv(t, nil)
	h := e.agent("alice")
	k := fake.NewKey()
	pc, _ := e.connect(k, control.RolePending)
	// CLI-style changes, picked up by the poll only.
	hk := fake.NewKey()
	if _, _, err := e.st.Update(func(st *store.State) error { return st.AddAgent("bob", keyLine(hk, "")) }); err != nil {
		t.Fatal(err)
	}
	presenceWhere(t, h, func(p *control.Presence) bool { return len(p.Agents) == 2 })
	e.connect(hk, control.RoleAgent)
	// A client added outside the control protocol promotes its pending connection.
	if _, _, err := e.st.Update(func(st *store.State) error {
		_, _, err := st.AddClient(keyLine(k, "anna@PC"), "", "admin", time.Now())
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
	hk := e.addAgent("alice")
	h, _ := e.connect(hk, control.RoleAgent)
	k := fake.NewKey()
	h.Send(&control.RegisterClient{Seq: 1, PublicKey: keyLine(k, "anna@PC")})
	if rep := mustWait[control.Reply](t, h, nil); rep.Error != "" {
		t.Fatal(rep.Error)
	}
	c, _ := e.connect(k, control.RoleClient)
	c.Close()
	e.restart()
	e.connect(k, control.RoleClient) // still registered after the restart
	h, _ = e.connect(hk, control.RoleAgent)
	p := mustWait[control.Presence](t, h, nil)
	ci := clientInfo(p, fp(k))
	if ci == nil || ci.RegisteredBy != "alice" || ci.LastSeen.IsZero() {
		t.Errorf("client after restart %+v", ci)
	}
}

func TestKeepaliveDropsDeadPeer(t *testing.T) {
	e := newEnv(t, func(c *server.Config) { c.KeepaliveInterval = 30 * time.Millisecond })
	h := e.agent("alice")
	ck := e.addClient("anna@PC")
	px := newProxy(t, e.addr)
	c, err := fake.Dial(px.addr(), ck, fake.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	presenceWhere(t, h, func(p *control.Presence) bool { return clientInfo(p, fp(ck)).Online })
	// A live peer survives several keepalive rounds.
	time.Sleep(200 * time.Millisecond)
	assertOpen(t, c)
	px.freeze()
	presenceWhere(t, h, func(p *control.Presence) bool { return !clientInfo(p, fp(ck)).Online })
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

func TestRenameClient(t *testing.T) {
	e := newEnv(t, nil)
	h := e.agent("alice")
	h2 := e.agent("bob")
	_, ck := e.client("anna@PC")

	// An agent renames a client: every agent's Presence shows the new name,
	// and the stored key line carries it as its comment.
	h.Send(&control.RenameClient{Seq: 3, Fingerprint: fp(ck), Name: "  Anna laptop  "})
	if rep := mustWait[control.Reply](t, h, nil); rep.Seq != 3 || rep.Error != "" {
		t.Fatalf("reply %+v", rep)
	}
	presenceWhere(t, h2, func(p *control.Presence) bool {
		return len(p.Clients) == 1 && p.Clients[0].Name == "Anna laptop" && p.Clients[0].Online
	})
	idx, _, err := e.st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if c := idx.State.Clients[0]; c.Name != "Anna laptop" || !strings.HasSuffix(c.PublicKey, " Anna laptop") {
		t.Fatalf("stored %+v", c)
	}

	// Bad names and unknown clients are refused with a Reply error.
	for i, m := range []*control.RenameClient{
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

// TestRegisterAgent: an agent adds another agent, who can then log in as
// one; duplicates, bad names and client keys are refused.
func TestRegisterAgent(t *testing.T) {
	e := newEnv(t, nil)
	h := e.agent("alice")
	bk := fake.NewKey()
	h.Send(&control.RegisterAgent{Seq: 1, PublicKey: keyLine(bk, "bob@pc"), Name: "bob"})
	if rep := mustWait[control.Reply](t, h, func(r *control.Reply) bool { return r.Seq == 1 }); rep.Error != "" {
		t.Fatalf("register: %s", rep.Error)
	}
	p := presenceWhere(t, h, func(p *control.Presence) bool { return len(p.Agents) == 2 })
	if p.Agents[1].Name != "bob" || p.Agents[1].Fingerprint != fp(bk) {
		t.Errorf("agents %+v", p.Agents)
	}
	if _, hello := e.connect(bk, control.RoleAgent); hello.Name != "bob" {
		t.Errorf("hello %+v", hello)
	}
	ck := e.addClient("anna@PC")
	for i, m := range []*control.RegisterAgent{
		{Seq: 2, PublicKey: keyLine(bk, ""), Name: "bob2"},           // key already an agent
		{Seq: 3, PublicKey: keyLine(fake.NewKey(), ""), Name: "bob"}, // name taken
		{Seq: 4, PublicKey: keyLine(fake.NewKey(), ""), Name: "with space"},
		{Seq: 5, PublicKey: keyLine(ck, ""), Name: "anna"}, // a client key
	} {
		h.Send(m)
		if rep := mustWait[control.Reply](t, h, func(r *control.Reply) bool { return r.Seq == m.Seq }); rep.Error == "" {
			t.Errorf("case %d accepted", i)
		}
	}
}
