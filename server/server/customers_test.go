package server_test

import (
	"slices"
	"testing"

	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/server/fake"
	"github.com/helpbuoy/helpbuoy/server/store"
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

func names(p *control.Presence) (computers, techies, customers []string) {
	for _, c := range p.Computers {
		computers = append(computers, c.Name)
	}
	for _, h := range p.Techies {
		techies = append(techies, h.Name)
	}
	for _, cu := range p.Customers {
		customers = append(customers, cu.Name)
	}
	return
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

// limitedEnv is a server with the customers A and B, a computer of each and
// one without a customer, an unlimited techie alice, carol limited to A and
// dave limited to B.
type limitedEnv struct {
	*env
	alice, carol     *fake.Conn
	a, b, none       *fake.Conn
	aFP, bFP, noneFP string
	idA, idB         string
}

func newLimitedEnv(t *testing.T) *limitedEnv {
	e := &limitedEnv{env: newEnv(t, nil)}
	var ak, bk, nk = fake.NewKey(), fake.NewKey(), fake.NewKey()
	ck, dk := fake.NewKey(), fake.NewKey()
	e.update(func(st *store.State) error {
		var err error
		if e.idA, err = st.AddCustomer("A"); err != nil {
			return err
		}
		if e.idB, err = st.AddCustomer("B"); err != nil {
			return err
		}
		for _, c := range []struct {
			k        any
			line, cu string
		}{{ak, keyLine(ak, "a@PC"), e.idA}, {bk, keyLine(bk, "b@PC"), e.idB}, {nk, keyLine(nk, "n@PC"), ""}} {
			fp, _, err := st.AddComputer(c.line, "", "admin", e.clock.Now())
			if err != nil {
				return err
			}
			if _, err := st.MoveComputer(fp, c.cu); err != nil {
				return err
			}
		}
		if err := st.AddTechie("carol", keyLine(ck, "")); err != nil {
			return err
		}
		if err := st.LimitTechie("carol", true, []string{"A"}); err != nil {
			return err
		}
		if err := st.AddTechie("dave", keyLine(dk, "")); err != nil {
			return err
		}
		return st.LimitTechie("dave", true, []string{"B"})
	})
	e.aFP, e.bFP, e.noneFP = fp(ak), fp(bk), fp(nk)
	e.alice = e.techie("alice")
	e.carol, _ = e.connect(ck, control.RoleTechie)
	e.a, _ = e.connect(ak, control.RoleComputer)
	e.b, _ = e.connect(bk, control.RoleComputer)
	e.none, _ = e.connect(nk, control.RoleComputer)
	return e
}

// TestLimitedTechiePresence: a limited techie only sees its customers,
// their computers and the techies it shares a customer with.
func TestLimitedTechiePresence(t *testing.T) {
	e := newLimitedEnv(t)
	p := presenceWhere(t, e.carol, func(p *control.Presence) bool {
		return len(p.Computers) == 1 && p.Computers[0].Online
	})
	computers, techies, customers := names(p)
	if !slices.Equal(computers, []string{"a@PC"}) || !slices.Equal(techies, []string{"alice", "carol"}) ||
		!slices.Equal(customers, []string{"A"}) {
		t.Errorf("carol sees computers %v, techies %v, customers %v", computers, techies, customers)
	}
	for _, h := range p.Techies {
		if h.Name == "carol" && (!h.Limited || !slices.Equal(h.Customers, []string{e.idA})) {
			t.Errorf("carol in her own Presence: %+v", h)
		}
	}

	p = presenceWhere(t, e.alice, func(p *control.Presence) bool { return len(p.Computers) == 3 && len(p.Techies) == 3 })
	computers, techies, customers = names(p)
	if !slices.Equal(techies, []string{"alice", "carol", "dave"}) || !slices.Equal(customers, []string{"A", "B"}) {
		t.Errorf("alice sees computers %v, techies %v, customers %v", computers, techies, customers)
	}
}

// TestLimitedTechieCommands: the server refuses a limited techie's commands
// for computers of other customers and the commands that affect every
// customer, and allows the rest.
func TestLimitedTechieCommands(t *testing.T) {
	e := newLimitedEnv(t)
	h := e.carol
	presenceWhere(t, h, func(p *control.Presence) bool { return len(p.Computers) == 1 })

	if msg := connect(t, h, 1, e.bFP, control.NewRequestID()); msg != "computer is not online" {
		t.Errorf("connect to another customer's computer: %q", msg)
	}
	if msg := connect(t, h, 2, e.noneFP, control.NewRequestID()); msg != "computer is not online" {
		t.Errorf("connect to a computer without customer: %q", msg)
	}
	newKey := fake.NewKey()
	seq := uint32(10)
	for i, m := range []any{
		&control.RenameComputer{Fingerprint: e.bFP, Name: "x"},
		&control.UnregisterComputer{Fingerprint: e.bFP},
		&control.MoveComputer{Fingerprint: e.bFP, Customer: e.idA},
		&control.MoveComputer{Fingerprint: e.aFP, Customer: e.idB},
		&control.MoveComputer{Fingerprint: e.aFP, Customer: ""},
		&control.DecideListing{Computer: e.bFP, Address: "other.example.com:443", Fingerprint: e.bFP, Allow: true},
		&control.RegisterComputer{PublicKey: keyLine(newKey, "x@PC")},
		&control.RegisterComputer{PublicKey: keyLine(newKey, "x@PC"), Customer: e.idB},
		&control.AddCustomer{Name: "C"},
		&control.RenameCustomer{ID: e.idA, Name: "AA"},
		&control.RemoveCustomer{ID: e.idA},
		&control.RegisterTechie{PublicKey: keyLine(fake.NewKey(), ""), Name: "eve"},
		&control.UnregisterTechie{Name: "dave"},
		&control.LimitTechie{Name: "dave", Limited: false},
		&control.SetServerInfo{Company: "Evil", Phone: "1"},
	} {
		seq++
		setSeq(m, seq)
		if msg := command(t, h, m, seq); msg == "" {
			t.Errorf("case %d (%T) was allowed", i, m)
		}
	}

	// Within its own customer it works as before.
	if msg := command(t, h, &control.RenameComputer{Seq: 50, Fingerprint: e.aFP, Name: "Anna"}, 50); msg != "" {
		t.Errorf("rename own computer: %s", msg)
	}
	if msg := command(t, h, &control.RegisterComputer{Seq: 51, PublicKey: keyLine(newKey, "x@PC"), Customer: e.idA}, 51); msg != "" {
		t.Errorf("register for own customer: %s", msg)
	}
	presenceWhere(t, e.alice, func(p *control.Presence) bool { return computerInfo(p, fp(newKey)).Customer == e.idA })
	if msg := connect(t, h, 52, e.aFP, control.NewRequestID()); msg != "" {
		t.Errorf("connect to own computer: %q", msg)
	}
	mustWait[control.SessionReady](t, e.a, nil)
}

// setSeq sets the Seq field of a techie command.
func setSeq(m any, seq uint32) {
	switch m := m.(type) {
	case *control.RenameComputer:
		m.Seq = seq
	case *control.UnregisterComputer:
		m.Seq = seq
	case *control.MoveComputer:
		m.Seq = seq
	case *control.DecideListing:
		m.Seq = seq
	case *control.RegisterComputer:
		m.Seq = seq
	case *control.AddCustomer:
		m.Seq = seq
	case *control.RenameCustomer:
		m.Seq = seq
	case *control.RemoveCustomer:
		m.Seq = seq
	case *control.RegisterTechie:
		m.Seq = seq
	case *control.UnregisterTechie:
		m.Seq = seq
	case *control.LimitTechie:
		m.Seq = seq
	case *control.SetServerInfo:
		m.Seq = seq
	}
}

// TestLimitChangeEndsSession: moving a computer away from a limited techie's
// customers ends its request at once, and the computer leaves its Presence.
// Lifting the limit shows everything again.
func TestLimitChangeEndsSession(t *testing.T) {
	e := newLimitedEnv(t)
	presenceWhere(t, e.carol, func(p *control.Presence) bool { return len(p.Computers) == 1 })
	if msg := connect(t, e.carol, 1, e.aFP, control.NewRequestID()); msg != "" {
		t.Fatal(msg)
	}
	mustWait[control.SessionReady](t, e.a, nil)

	if msg := command(t, e.alice, &control.MoveComputer{Seq: 2, Fingerprint: e.aFP, Customer: e.idB}, 2); msg != "" {
		t.Fatal(msg)
	}
	mustWait(t, e.a, func(m *control.RequestStateMsg) bool { return m.State == control.RequestEnded })
	presenceWhere(t, e.carol, func(p *control.Presence) bool { return len(p.Computers) == 0 })

	if msg := command(t, e.alice, &control.LimitTechie{Seq: 3, Name: "carol", Limited: false}, 3); msg != "" {
		t.Fatal(msg)
	}
	presenceWhere(t, e.carol, func(p *control.Presence) bool { return len(p.Computers) == 3 && len(p.Customers) == 2 })

	if msg := command(t, e.alice, &control.LimitTechie{Seq: 4, Name: "alice", Limited: true}, 4); msg == "" {
		t.Error("alice changed her own limit")
	}
}
