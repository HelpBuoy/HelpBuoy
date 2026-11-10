package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/server/auth"
)

func TestCustomers(t *testing.T) {
	st := &State{}
	ck := newKey(t)
	fp, _, err := st.AddComputer(auth.KeyLine(ck, "anna@PC"), "", "alice", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.AddCustomer("Acme BV")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddCustomer(" ACME bv "); err == nil {
		t.Error("duplicate customer name allowed")
	}
	if _, err := st.AddCustomer("bad\x00name"); err == nil {
		t.Error("bad customer name allowed")
	}
	if _, err := st.MoveComputer(fp, "acme bv"); err != nil {
		t.Fatal(err)
	}
	if st.Computers[0].Customer != id {
		t.Fatalf("customer %q, want %q", st.Computers[0].Customer, id)
	}
	if _, err := st.MoveComputer(fp, "Nobody"); err == nil {
		t.Error("moved to an unknown customer")
	}
	other, _ := st.AddCustomer("Other")
	if _, err := st.RenameCustomer(id, "other"); err == nil {
		t.Error("renamed to the name of another customer")
	}
	if old, err := st.RenameCustomer(id, "Acme Holding"); err != nil || old != "Acme BV" {
		t.Fatalf("rename: %q %v", old, err)
	}
	if _, err := NewIndex(st); err != nil {
		t.Fatal(err)
	}

	// Removing a customer keeps its computers.
	if name, err := st.RemoveCustomer(id); err != nil || name != "Acme Holding" {
		t.Fatalf("remove: %q %v", name, err)
	}
	if st.Computers[0].Customer != "" || len(st.Computers) != 1 {
		t.Errorf("computer after removing its customer: %+v", st.Computers[0])
	}
	if _, err := st.RemoveCustomer(other); err != nil {
		t.Fatal(err)
	}
	if _, err := NewIndex(st); err != nil {
		t.Fatal(err)
	}
}

// TestLimitedTechiesDropped: the techies an older version limited to some
// customers are no longer techies, so their keys are refused, but the list
// is kept for that older version.
func TestLimitedTechiesDropped(t *testing.T) {
	carol := auth.KeyLine(newKey(t), "")
	b := []byte(`{"techies":[],"computers":[],"customers":[{"id":"a","name":"A"}],` +
		`"limited_techies":[{"name":"carol","public_key":"` + carol + `","customers":["a"]}]}`)
	idx, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	k, _, _ := auth.ParseKey(carol)
	if id := idx.Lookup(auth.Fingerprint(k)); id.Role != control.RolePending {
		t.Errorf("a limited techie's key is %s", id.Role)
	}
	out, err := json.Marshal(idx.State)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"limited_techies"`) {
		t.Errorf("limited_techies dropped: %s", out)
	}
}

func TestCustomerValidation(t *testing.T) {
	for _, s := range []string{
		`{"customers":[{"id":"a","name":"A"},{"id":"a","name":"B"}]}`,
		`{"customers":[{"id":"a","name":"A"},{"id":"b","name":"a"}]}`,
		`{"customers":[{"id":"A!","name":"A"}]}`,
		`{"computers":[{"public_key":"` + auth.KeyLine(newKey(t), "x@y") + `","name":"x@y","customer":"nope"}]}`,
	} {
		if _, err := Parse([]byte(s)); err == nil {
			t.Errorf("accepted %s", s)
		}
	}
}
