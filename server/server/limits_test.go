package server_test

import (
	"net"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/server/fake"
	"github.com/helpbuoy/helpbuoy/server/limits"
	"github.com/helpbuoy/helpbuoy/server/server"
	"github.com/helpbuoy/helpbuoy/server/store"
)

// dialFrom connects from a given loopback source address and reports the
// role from Hello, or "" if the server refused the connection.
func dialFrom(t *testing.T, e *env, src string, k ssh.Signer) (control.Role, *fake.Conn) {
	t.Helper()
	c, err := fake.Dial(e.addr, k, fake.Options{LocalAddr: &net.TCPAddr{IP: net.ParseIP(src)}})
	if err != nil {
		return "", nil
	}
	t.Cleanup(func() { c.Close() })
	c.Send(&control.ComputerHello{UserMachine: "u@m"})
	h, err := fake.WaitFor[control.Hello](c, wait, nil)
	if err != nil {
		return "", nil
	}
	return h.Role, c
}

func needLoopbackAliases(t *testing.T) {
	t.Helper()
	for _, ip := range []string{"127.0.0.2", "127.0.0.3"} {
		ln, err := net.Listen("tcp", ip+":0")
		if err != nil {
			t.Skipf("no loopback alias %s on this system: %v", ip, err)
		}
		ln.Close()
	}
}

func defaultLimits(c *server.Config) {
	c.Limits = limits.Config{}
}

// TestKeyRate: a key may try at most once per second. A refused attempt
// leaves the key's existing connection alone; a later attempt replaces it.
// Nothing is blocked beyond that second.
func TestKeyRate(t *testing.T) {
	e := newEnv(t, defaultLimits)
	hk := e.addTechie("alice")
	h, _ := e.connect(hk, control.RoleTechie)

	// Same second: refused, and the first connection keeps working.
	if role, _ := dialFrom(t, e, "127.0.0.1", hk); role != "" {
		t.Fatal("second attempt within a second accepted")
	}
	h.Send(&control.Connect{Seq: 9, Fingerprint: "SHA256:none", RequestID: "none"})
	if rep := mustWait[control.Reply](t, h, nil); rep.Seq != 9 {
		t.Fatalf("first connection broken: %+v", rep)
	}

	// A second later the key gets in, and the new connection replaces the old.
	e.clock.Advance(time.Second)
	if role, _ := dialFrom(t, e, "127.0.0.1", hk); role != control.RoleTechie {
		t.Fatalf("after a second: %q", role)
	}
	if msg := waitClosed(t, h); msg == "" {
		t.Error("replaced connection got no Error")
	}

	// Unknown keys are limited the same way, with no block after hammering.
	k := fake.NewKey()
	e.clock.Advance(time.Second)
	if role, _ := dialFrom(t, e, "127.0.0.1", k); role != control.RolePending {
		t.Fatal("unknown key refused")
	}
	for range 20 {
		if role, _ := dialFrom(t, e, "127.0.0.1", k); role != "" {
			t.Fatal("hammering key accepted within its second")
		}
	}
	e.clock.Advance(time.Second)
	if role, _ := dialFrom(t, e, "127.0.0.1", k); role != control.RolePending {
		t.Fatal("key blocked after hammering")
	}
}

// TestFreshKeyFloodPerIP: fresh keys from one IP fill only that IP's pending
// slots; other IPs and known keys from the same IP still get in, and a closed
// pending connection frees its slot at once (no ban).
func TestFreshKeyFloodPerIP(t *testing.T) {
	needLoopbackAliases(t)
	e := newEnv(t, defaultLimits)
	var first *fake.Conn
	for i := range 10 {
		role, c := dialFrom(t, e, "127.0.0.2", fake.NewKey())
		if role != control.RolePending {
			t.Fatalf("unknown key %d refused too early", i)
		}
		if i == 0 {
			first = c
		}
	}
	for i := range 5 {
		if role, _ := dialFrom(t, e, "127.0.0.2", fake.NewKey()); role != "" {
			t.Fatalf("flood key %d got in as %s", i, role)
		}
	}
	if role, _ := dialFrom(t, e, "127.0.0.3", fake.NewKey()); role != control.RolePending {
		t.Fatal("other IP refused")
	}
	hk := e.addTechie("alice")
	if role, _ := dialFrom(t, e, "127.0.0.2", hk); role != control.RoleTechie {
		t.Fatalf("techie from the full IP: %q", role)
	}
	first.Close()
	deadline := time.Now().Add(wait)
	for {
		if role, _ := dialFrom(t, e, "127.0.0.2", fake.NewKey()); role == control.RolePending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("closed pending connection did not free its slot")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestManyComputersBehindFullIP: 500 registered computers behind one address
// connect while that address has no pending slots left.
func TestManyComputersBehindFullIP(t *testing.T) {
	needLoopbackAliases(t)
	e := newEnv(t, func(c *server.Config) {
		defaultLimits(c)
		c.PresenceInterval = 250 * time.Millisecond
	})
	const n = 500
	keys := make([]ssh.Signer, n)
	if _, _, err := e.st.Update(func(st *store.State) error {
		for i := range keys {
			keys[i] = fake.NewKey()
			if _, _, err := st.AddComputer(keyLine(keys[i], uniq("user@pc")), "", "admin", e.clock.Now()); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.srv.Reload()
	techies := make([]*fake.Conn, 5)
	for i := range techies {
		techies[i] = e.techie(uniq("h"))
	}
	for range 10 {
		dialFrom(t, e, "127.0.0.2", fake.NewKey())
	}
	if role, _ := dialFrom(t, e, "127.0.0.2", fake.NewKey()); role != "" {
		t.Fatal("pending slots of the address not full")
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, 50)
	failed := make(chan int, n)
	conns := make([]*fake.Conn, n)
	for i, k := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			c, err := fake.Dial(e.addr, k, fake.Options{LocalAddr: &net.TCPAddr{IP: net.ParseIP("127.0.0.2")}})
			if err != nil {
				failed <- i
				return
			}
			conns[i] = c
			c.Send(&control.ComputerHello{UserMachine: "u@m"})
			if h, err := fake.WaitFor[control.Hello](c, wait, nil); err != nil || h.Role != control.RoleComputer {
				failed <- i
			}
		}()
	}
	wg.Wait()
	close(failed)
	for _, c := range conns {
		if c != nil {
			defer c.Close()
		}
	}
	nf := 0
	for range failed {
		nf++
	}
	if nf > 0 {
		t.Fatalf("%d of %d registered computers could not connect", nf, n)
	}
	if got := e.srv.Registry().Counts()[control.RoleComputer]; got != n {
		t.Errorf("online computers %d", got)
	}
	for _, h := range techies {
		presenceWhere(t, h, func(p *control.Presence) bool {
			on := 0
			for _, c := range p.Computers {
				if c.Online {
					on++
				}
			}
			return on == n
		})
	}
}

func TestPendingTotal(t *testing.T) {
	e := newEnv(t, func(c *server.Config) {
		c.Limits = looseLimits()
		c.Limits.PendingTotal = 3
	})
	for range 3 {
		if role, _ := dialFrom(t, e, "127.0.0.1", fake.NewKey()); role != control.RolePending {
			t.Fatal("refused below the cap")
		}
	}
	if role, _ := dialFrom(t, e, "127.0.0.1", fake.NewKey()); role != "" {
		t.Fatal("pending cap not enforced")
	}
	// Known keys are not counted against it.
	e.connect(e.addTechie("alice"), control.RoleTechie)
	e.connect(e.addComputer("anna@PC"), control.RoleComputer)
	if got := e.srv.Limiter().Pending(); got != 3 {
		t.Errorf("pending %d", got)
	}
}

func TestPromotionFreesPendingSlot(t *testing.T) {
	e := newEnv(t, func(c *server.Config) {
		c.Limits = looseLimits()
		c.Limits.PendingTotal = 1
	})
	h := e.techie("alice")
	k := fake.NewKey()
	pc, _ := e.connect(k, control.RolePending)
	h.Send(&control.RegisterComputer{Seq: 1, PublicKey: keyLine(k, "anna@PC")})
	mustWait[control.Registered](t, pc, nil)
	if role, _ := dialFrom(t, e, "127.0.0.1", fake.NewKey()); role != control.RolePending {
		t.Fatal("slot not freed by promotion")
	}
}

// TestFailedHandshakesNoBan: garbage connections don't lead to any block;
// a techie from the same address gets in right after them.
func TestFailedHandshakesNoBan(t *testing.T) {
	e := newEnv(t, defaultLimits)
	hk := e.addTechie("alice")
	for range 50 {
		nc, err := net.Dial("tcp", e.addr)
		if err != nil {
			t.Fatal(err)
		}
		nc.Write([]byte("GET / HTTP/1.0\r\n\r\n"))
		nc.Read(make([]byte, 512)) // wait for the server to give up
		nc.Close()
	}
	if role, _ := dialFrom(t, e, "127.0.0.1", hk); role != control.RoleTechie {
		t.Fatalf("techie after failed handshakes: %q", role)
	}
}

// TestManyTechiesAndComputers connects 100 techies and 1000 computers at once
// (fewer under the race detector) and checks that every techie ends up with
// a Presence showing all of them.
func TestManyTechiesAndComputers(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	e := newEnv(t, func(c *server.Config) { c.PresenceInterval = 250 * time.Millisecond })
	nh, nc := 100, 1000
	if raceEnabled {
		nh, nc = 25, 250
	}
	hkeys := make([]ssh.Signer, nh)
	ckeys := make([]ssh.Signer, nc)
	if _, _, err := e.st.Update(func(st *store.State) error {
		for i := range hkeys {
			hkeys[i] = fake.NewKey()
			if err := st.AddTechie(uniq("h"), keyLine(hkeys[i], "")); err != nil {
				return err
			}
		}
		for i := range ckeys {
			ckeys[i] = fake.NewKey()
			if _, _, err := st.AddComputer(keyLine(ckeys[i], uniq("u@pc")), "", "admin", e.clock.Now()); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.srv.Reload()
	all := append(append([]ssh.Signer{}, hkeys...), ckeys...)
	conns := make([]*fake.Conn, len(all))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 64)
	errs := make(chan error, len(all))
	for i, k := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			c, err := fake.Dial(e.addr, k, fake.Options{})
			if err != nil {
				errs <- err
				return
			}
			conns[i] = c
			if i >= nh {
				c.Send(&control.ComputerHello{UserMachine: "u@m"})
			}
			if _, err := fake.WaitFor[control.Hello](c, wait, nil); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for _, c := range conns {
		if c != nil {
			defer c.Close()
		}
	}
	for err := range errs {
		t.Fatal(err)
	}
	counts := e.srv.Registry().Counts()
	if counts[control.RoleTechie] != nh || counts[control.RoleComputer] != nc {
		t.Fatalf("online %v", counts)
	}
	for _, h := range conns[:nh] {
		presenceWhere(t, h, func(p *control.Presence) bool {
			on := 0
			for _, c := range p.Computers {
				if c.Online {
					on++
				}
			}
			for _, x := range p.Techies {
				if x.Online {
					on++
				}
			}
			return on == nh+nc
		})
	}
}
