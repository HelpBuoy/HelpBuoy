package server_test

import (
	"testing"

	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/server/fake"
)

// command sends a techie command and returns the error of its Reply.
func command(t *testing.T, h *fake.Conn, m any, seq uint32) string {
	t.Helper()
	if err := h.Send(m); err != nil {
		t.Fatal(err)
	}
	return mustWait(t, h, func(r *control.Reply) bool { return r.Seq == seq }).Error
}

// customerID returns the id of the customer with the name in p, or "".
func customerID(p *control.Presence, name string) string {
	for _, cu := range p.Customers {
		if cu.Name == name {
			return cu.ID
		}
	}
	return ""
}

// TestCustomers: techies add, rename and remove customers, register
// computers for a customer and move them; Presence shows it all, and
// removing a customer keeps its computers.
func TestCustomers(t *testing.T) {
	e := newEnv(t, nil)
	h := e.techie("alice")
	obs := e.techie("bob") // watches Presence; h's own is mixed with its Replies
	_, ck := e.computer("anna@PC")

	if msg := command(t, h, &control.AddCustomer{Seq: 1, Name: "  Acme BV "}, 1); msg != "" {
		t.Fatal(msg)
	}
	if msg := command(t, h, &control.AddCustomer{Seq: 2, Name: "acme bv"}, 2); msg == "" {
		t.Error("a second customer with the same name was added")
	}
	p := presenceWhere(t, obs, func(p *control.Presence) bool { return len(p.Customers) == 1 })
	acme := customerID(p, "Acme BV")
	if acme == "" {
		t.Fatalf("customers %+v", p.Customers)
	}

	if msg := command(t, h, &control.MoveComputer{Seq: 3, Fingerprint: fp(ck), Customer: acme}, 3); msg != "" {
		t.Fatal(msg)
	}
	presenceWhere(t, obs, func(p *control.Presence) bool { return computerInfo(p, fp(ck)).Customer == acme })
	if msg := command(t, h, &control.MoveComputer{Seq: 4, Fingerprint: fp(ck), Customer: "nope"}, 4); msg == "" {
		t.Error("moved to an unknown customer")
	}

	k := fake.NewKey()
	if msg := command(t, h, &control.RegisterComputer{Seq: 5, PublicKey: keyLine(k, "bob@PC"), Customer: acme}, 5); msg != "" {
		t.Fatal(msg)
	}
	presenceWhere(t, obs, func(p *control.Presence) bool { return computerInfo(p, fp(k)).Customer == acme })

	if msg := command(t, h, &control.RenameCustomer{Seq: 6, ID: acme, Name: "Acme Holding"}, 6); msg != "" {
		t.Fatal(msg)
	}
	presenceWhere(t, obs, func(p *control.Presence) bool { return customerID(p, "Acme Holding") == acme })

	if msg := command(t, h, &control.RemoveCustomer{Seq: 7, ID: acme}, 7); msg != "" {
		t.Fatal(msg)
	}
	p = presenceWhere(t, obs, func(p *control.Presence) bool { return len(p.Customers) == 0 })
	if len(p.Computers) != 2 || p.Computers[0].Customer != "" || p.Computers[1].Customer != "" {
		t.Errorf("computers after removing their customer: %+v", p.Computers)
	}
}
