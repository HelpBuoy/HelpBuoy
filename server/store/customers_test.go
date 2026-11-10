package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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
	if err := st.AddTechie("carol", auth.KeyLine(newKey(t), "")); err != nil {
		t.Fatal(err)
	}
	if err := st.LimitTechie("carol", true, []string{"Acme Holding", other, other}); err != nil {
		t.Fatal(err)
	}
	if h := st.Techies[0]; !h.Limited || len(h.Customers) != 2 {
		t.Fatalf("limited techie %+v", h)
	}
	if _, err := NewIndex(st); err != nil {
		t.Fatal(err)
	}

	// Removing a customer keeps its computers and drops it from limits.
	if name, err := st.RemoveCustomer(id); err != nil || name != "Acme Holding" {
		t.Fatalf("remove: %q %v", name, err)
	}
	if st.Computers[0].Customer != "" || len(st.Computers) != 1 {
		t.Errorf("computer after removing its customer: %+v", st.Computers[0])
	}
	if h := st.Techies[0]; !h.Limited || len(h.Customers) != 1 || h.Customers[0] != other {
		t.Errorf("limited techie after removing a customer: %+v", h)
	}
	st.RemoveCustomer(other)
	if h := st.Techies[0]; !h.Limited || len(h.Customers) != 0 {
		t.Errorf("a techie without customers left must stay limited: %+v", h)
	}
	if _, err := NewIndex(st); err != nil {
		t.Fatal(err)
	}
}

// TestLimitedTechiesStoredApart: limited techies are written to
// limited_techies, not techies, so an older server treats their keys as
// unknown; they read back limited, also with no customers left.
func TestLimitedTechiesStoredApart(t *testing.T) {
	st := &State{}
	id, _ := st.AddCustomer("A")
	st.AddTechie("alice", auth.KeyLine(newKey(t), ""))
	st.AddTechie("carol", auth.KeyLine(newKey(t), ""))
	st.AddTechie("dave", auth.KeyLine(newKey(t), ""))
	if err := st.LimitTechie("carol", true, []string{id}); err != nil {
		t.Fatal(err)
	}
	if err := st.LimitTechie("dave", true, nil); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var old struct {
		Techies []struct{ Name string } `json:"techies"`
	}
	if err := json.Unmarshal(b, &old); err != nil {
		t.Fatal(err)
	}
	if len(old.Techies) != 1 || old.Techies[0].Name != "alice" {
		t.Errorf("an older server would see techies %+v in %s", old.Techies, b)
	}
	if !strings.Contains(string(b), `"limited_techies"`) {
		t.Errorf("no limited_techies in %s", b)
	}
	idx, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Techie{}
	for _, h := range idx.State.Techies {
		got[h.Name] = h
	}
	if got["alice"].Limited || !got["carol"].Limited || len(got["carol"].Customers) != 1 ||
		!got["dave"].Limited || len(got["dave"].Customers) != 0 {
		t.Errorf("read back %+v", idx.State.Techies)
	}
	if idx.State.LimitedTechies != nil {
		t.Error("LimitedTechies left set after reading")
	}
	// Lifting the limit moves the techie back.
	st = idx.State.Clone()
	if err := st.LimitTechie("carol", false, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ = json.Marshal(st); strings.Count(string(b), `"name":"carol"`) != 1 {
		t.Errorf("carol after lifting the limit: %s", b)
	}
}

func TestCustomerValidation(t *testing.T) {
	for _, s := range []string{
		`{"customers":[{"id":"a","name":"A"},{"id":"a","name":"B"}]}`,
		`{"customers":[{"id":"a","name":"A"},{"id":"b","name":"a"}]}`,
		`{"customers":[{"id":"A!","name":"A"}]}`,
		`{"computers":[{"public_key":"` + auth.KeyLine(newKey(t), "x@y") + `","name":"x@y","customer":"nope"}]}`,
		`{"limited_techies":[{"name":"carol","public_key":"` + auth.KeyLine(newKey(t), "") + `","customers":["nope"]}]}`,
		`{"techies":[{"name":"carol","public_key":"` + auth.KeyLine(newKey(t), "") + `","customers":["a"]}],"customers":[{"id":"a","name":"A"}]}`,
	} {
		if _, err := Parse([]byte(s)); err == nil {
			t.Errorf("accepted %s", s)
		}
	}
}
