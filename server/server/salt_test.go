package server_test

import (
	"encoding/hex"
	"testing"

	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/proto/verify"
	"github.com/helpbuoy/helpbuoy/server/fake"
	"github.com/helpbuoy/helpbuoy/server/hostkey"
	"github.com/helpbuoy/helpbuoy/server/salt"
)

// The salt goes to techies and registered computers only (PLAN.md 12.4).
func TestHelloSalt(t *testing.T) {
	e := newEnv(t, nil)
	_, th := e.connect(e.addTechie("alice"), control.RoleTechie)
	_, ch := e.connect(e.addComputer("anna@PC"), control.RoleComputer)
	_, ph := e.connect(fake.NewKey(), control.RolePending)
	if _, ok := verify.ParseSalt(th.Salt); !ok {
		t.Fatalf("techie hello salt %q", th.Salt)
	}
	if ch.Salt != th.Salt {
		t.Errorf("computer salt %q, techie salt %q", ch.Salt, th.Salt)
	}
	if ph.Salt != "" {
		t.Errorf("a pending key got the salt %q", ph.Salt)
	}
}

func TestRegisteredSalt(t *testing.T) {
	e := newEnv(t, nil)
	h := e.techie("alice")
	_, th := e.connect(e.addTechie("bob"), control.RoleTechie)
	k := fake.NewKey()
	pc, ph := e.connect(k, control.RolePending)
	if ph.Salt != "" {
		t.Fatal("pending got the salt")
	}
	h.Send(&control.RegisterComputer{Seq: 1, PublicKey: keyLine(k, "anna@PC")})
	mustWait[control.Reply](t, h, nil)
	r := mustWait[control.Registered](t, pc, nil)
	if r.Salt != th.Salt {
		t.Fatalf("Registered salt %q, want %q", r.Salt, th.Salt)
	}
}

func TestSaltRotationAtRestart(t *testing.T) {
	e := newEnv(t, nil)
	k := e.addTechie("alice")
	_, h1 := e.connect(k, control.RoleTechie)
	e.restart()
	_, h2 := e.connect(k, control.RoleTechie)
	if h1.Salt != h2.Salt {
		t.Fatal("the salt changed across a restart")
	}
	next, err := salt.Next(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, h := e.connect(k, control.RoleTechie); h.Salt != h1.Salt {
		t.Fatal("the running server uses the next salt")
	}
	e.restart()
	if _, h := e.connect(k, control.RoleTechie); h.Salt == h1.Salt || h.Salt != hexOf(next) {
		t.Fatalf("after a restart the salt is %q, want %x", h.Salt, next)
	}
}

func TestNewHostKeyAtRestart(t *testing.T) {
	e := newEnv(t, nil)
	old := e.srv.HostKey()
	nk, err := hostkey.New(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(e.srv.HostKey().Marshal()) != string(old.Marshal()) {
		t.Fatal("the running server changed its host key")
	}
	k := e.addTechie("alice")
	e.restart()
	if string(e.srv.HostKey().Marshal()) != string(nk.PublicKey().Marshal()) {
		t.Fatal("the new host key is not in use after the restart")
	}
	e.connect(k, control.RoleTechie) // techies and computers are kept
}

func hexOf(b []byte) string { return hex.EncodeToString(b) }
