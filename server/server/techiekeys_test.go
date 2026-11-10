package server_test

import (
	"testing"

	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/server/fake"
)

func techieInfo(p *control.Presence, name string) control.TechieInfo {
	for _, h := range p.Techies {
		if h.Name == name {
			return h
		}
	}
	return control.TechieInfo{}
}

// TestTechieKeys: a techie has a key per computer it uses. It connects
// with any of them, computers get the key it is connected with, Presence
// shows which key is online and when each was last seen, and removing a
// key closes its connection.
func TestTechieKeys(t *testing.T) {
	e := newEnv(t, nil)
	ak := e.addTechie("alice")
	alice, _ := e.connect(ak, control.RoleTechie)
	obs := e.techie("carol") // watches Presence; alice's own is mixed with its Replies
	k1 := e.addTechie("bob")
	k2, k3 := fake.NewKey(), fake.NewKey()
	c, _ := e.computer("anna@PC")

	if msg := command(t, alice, &control.AddTechieKey{Seq: 1, Name: "bob", PublicKey: keyLine(k2, "")}, 1); msg != "" {
		t.Fatal(msg)
	}
	if msg := command(t, alice, &control.AddTechieKey{Seq: 2, Name: "bob", PublicKey: keyLine(k1, "")}, 2); msg == "" {
		t.Error("added a key bob already has")
	}
	p := presenceWhere(t, obs, func(p *control.Presence) bool { return len(techieInfo(p, "bob").Keys) == 2 })
	if b := techieInfo(p, "bob"); b.Online || b.Fingerprint != fp(k1) || b.Keys[1].Fingerprint != fp(k2) || !b.Keys[1].LastSeen.IsZero() {
		t.Fatalf("bob before connecting: %+v", b)
	}

	bob, _ := e.connect(k2, control.RoleTechie)
	p = presenceWhere(t, obs, func(p *control.Presence) bool { return techieInfo(p, "bob").Online })
	if b := techieInfo(p, "bob"); b.Fingerprint != fp(k2) || b.Keys[0].Online || !b.Keys[1].Online {
		t.Fatalf("bob online with his second key: %+v", b)
	}
	if err := bob.Send(&control.Connect{Seq: 1, Fingerprint: computerFP(c), RequestID: control.NewRequestID()}); err != nil {
		t.Fatal(err)
	}
	if sr := mustWait[control.SessionReady](t, c, nil); sr.TechieName != "bob" || sr.TechieKey != keyLine(k2, "") {
		t.Fatalf("session ready %+v, want bob's second key", sr)
	}

	if msg := command(t, alice, &control.RemoveTechieKey{Seq: 3, Name: "alice", Fingerprint: fp(ak)}, 3); msg == "" {
		t.Error("alice removed the key she is connected with")
	}
	if msg := command(t, alice, &control.AddTechieKey{Seq: 4, Name: "bob", PublicKey: keyLine(k3, "")}, 4); msg != "" {
		t.Fatal(msg)
	}
	if msg := command(t, alice, &control.RemoveTechieKey{Seq: 5, Name: "bob", Fingerprint: fp(k2)}, 5); msg != "" {
		t.Fatal(msg)
	}
	if msg := waitClosed(t, bob); msg == "" {
		t.Error("bob's connection with the removed key closed without Error")
	}
	p = presenceWhere(t, obs, func(p *control.Presence) bool {
		b := techieInfo(p, "bob")
		return !b.Online && len(b.Keys) == 2
	})
	if b := techieInfo(p, "bob"); b.Keys[0].Fingerprint != fp(k1) || b.Keys[1].Fingerprint != fp(k3) {
		t.Errorf("bob's keys after removing one: %+v", b)
	}
	if msg := command(t, alice, &control.RemoveTechieKey{Seq: 6, Name: "bob", Fingerprint: fp(k1)}, 6); msg != "" {
		t.Fatal(msg)
	}
	if msg := command(t, alice, &control.RemoveTechieKey{Seq: 7, Name: "bob", Fingerprint: fp(k3)}, 7); msg == "" {
		t.Error("removed bob's last key")
	}
}

// TestTechieLastSeen: Presence keeps when a techie key was last online, and
// the server stores it.
func TestTechieLastSeen(t *testing.T) {
	e := newEnv(t, nil)
	obs := e.techie("alice")
	k := e.addTechie("bob")
	bob, _ := e.connect(k, control.RoleTechie)
	presenceWhere(t, obs, func(p *control.Presence) bool { return techieInfo(p, "bob").Online })
	bob.Close()
	p := presenceWhere(t, obs, func(p *control.Presence) bool {
		b := techieInfo(p, "bob")
		return !b.Online && len(b.Keys) == 1
	})
	if b := techieInfo(p, "bob"); b.Keys[0].LastSeen.IsZero() {
		t.Fatalf("bob after going offline: %+v", b)
	}
	e.restart()
	idx, _, err := e.st.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range idx.State.Techies {
		if h.Name == "bob" && h.Keys[0].LastSeen.IsZero() {
			t.Error("bob's last seen time was not stored")
		}
	}
}
