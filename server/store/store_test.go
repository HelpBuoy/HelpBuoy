package store

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/server/auth"
)

func newKey(t *testing.T) ssh.PublicKey {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	idx, raw, err := s.Load()
	if err != nil || raw != nil || len(idx.State.Techies) != 0 {
		t.Fatalf("empty load: %v", err)
	}
	hk, ck := newKey(t), newKey(t)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	_, _, err = s.Update(func(st *State) error {
		if err := st.AddTechie("alice", auth.KeyLine(hk, "laptop")); err != nil {
			return err
		}
		_, _, err := st.AddComputer(auth.KeyLine(ck, "anna@PC"), "", "alice", now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	idx, _, err = s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if id := idx.Lookup(auth.Fingerprint(hk)); id.Role != control.RoleTechie || id.Name != "alice" {
		t.Errorf("techie %+v", id)
	}
	if id := idx.Lookup(auth.Fingerprint(ck)); id.Role != control.RoleComputer || id.Name != "anna@PC" {
		t.Errorf("computer %+v", id)
	}
	if id := idx.Lookup("SHA256:nope"); id.Role != control.RolePending {
		t.Errorf("unknown %+v", id)
	}
	c, _ := idx.Computer(auth.Fingerprint(ck))
	if c.RegisteredBy != "alice" || !c.RegisteredAt.Equal(now) {
		t.Errorf("computer %+v", c)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(s.Path())
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("mode %v %v", fi.Mode(), err)
		}
	}
	// No temp or lock files are left behind.
	ents, _ := os.ReadDir(s.Dir())
	if len(ents) != 1 {
		t.Errorf("files %v", ents)
	}
}

func TestRules(t *testing.T) {
	st := &State{}
	hk, ck := newKey(t), newKey(t)
	at := time.Now()
	if err := st.AddTechie("alice", auth.KeyLine(hk, "")); err != nil {
		t.Fatal(err)
	}
	if err := st.AddTechie("alice", auth.KeyLine(newKey(t), "")); err == nil {
		t.Error("duplicate techie name")
	}
	if err := st.AddTechie("bob", auth.KeyLine(hk, "")); err == nil {
		t.Error("duplicate techie key")
	}
	if err := st.AddTechie("with space", auth.KeyLine(newKey(t), "")); err == nil {
		t.Error("techie name with space")
	}
	if _, _, err := st.AddComputer(auth.KeyLine(hk, "x@y"), "", "alice", at); err == nil {
		t.Error("techie key registered as computer")
	}
	if _, _, err := st.AddComputer(auth.KeyLine(ck, ""), "", "alice", at); err == nil {
		t.Error("computer without name")
	}
	if _, name, err := st.AddComputer(auth.KeyLine(ck, ""), "anna@PC", "alice", at); err != nil || name != "anna@PC" {
		t.Errorf("computer with name: %v", err)
	}
	if _, _, err := st.AddComputer(auth.KeyLine(ck, "again@PC"), "", "alice", at); err == nil {
		t.Error("duplicate computer")
	}
	if err := st.AddTechie("carol", auth.KeyLine(ck, "")); err == nil {
		t.Error("computer key added as techie")
	}
	if _, err := st.RemoveComputer("SHA256:nope"); err == nil {
		t.Error("removed unknown computer")
	}
	if name, err := st.RemoveComputer(auth.Fingerprint(ck)); err != nil || name != "anna@PC" {
		t.Errorf("remove computer %q %v", name, err)
	}
	if err := st.RemoveTechie("nobody"); err == nil {
		t.Error("removed unknown techie")
	}
	if err := st.RemoveTechie("alice"); err != nil || len(st.Techies) != 0 {
		t.Errorf("remove techie %v", err)
	}
}

func TestParseRejectsBadState(t *testing.T) {
	hk := newKey(t)
	line := auth.KeyLine(hk, "")
	for _, s := range []string{
		`{`,
		`{"techies":[{"name":"a","public_key":"garbage"}]}`,
		`{"techies":[{"name":"a","public_key":"` + line + `"},{"name":"b","public_key":"` + line + `"}]}`,
		`{"techies":[{"name":"a","public_key":"` + line + `"}],"computers":[{"name":"x","public_key":"` + line + `"}]}`,
	} {
		if _, err := Parse([]byte(s)); err == nil {
			t.Errorf("accepted %s", s)
		}
	}
	if _, err := Parse([]byte("  \n")); err != nil {
		t.Errorf("empty file: %v", err)
	}
}

func TestConcurrentUpdates(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, _ := Open(dir) // separate Store values, as separate processes would have
			k := newKey(t)
			_, _, err := s.Update(func(st *State) error {
				_, _, err := st.AddComputer(auth.KeyLine(k, "u@pc"+string(rune('a'+i))), "", "x", time.Now())
				return err
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	s, _ := Open(dir)
	idx, _, err := s.Load()
	if err != nil || len(idx.State.Computers) != 20 {
		t.Fatalf("computers %d, %v", len(idx.State.Computers), err)
	}
}

func TestStaleLockIsBroken(t *testing.T) {
	s, _ := Open(t.TempDir())
	p := filepath.Join(s.Dir(), lockName)
	os.WriteFile(p, []byte("1\n"), 0o600)
	old := time.Now().Add(-time.Minute)
	os.Chtimes(p, old, old)
	if _, _, err := s.Update(func(*State) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestSetLastSeen(t *testing.T) {
	st := &State{}
	k := newKey(t)
	st.AddComputer(auth.KeyLine(k, "a@b"), "", "x", time.Now())
	t1 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	st.SetLastSeen(map[string]time.Time{auth.Fingerprint(k): t1})
	st.SetLastSeen(map[string]time.Time{auth.Fingerprint(k): t1.Add(-time.Hour)})
	if !st.Computers[0].LastSeen.Equal(t1) {
		t.Errorf("last seen %v", st.Computers[0].LastSeen)
	}
}

func TestUnknownFieldsAreKept(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hk, ck := newKey(t), newKey(t)
	in := `{"title":"T","msp":{"phone":"+1 234"},"techies":[{"name":"alice","admin":true,"public_key":"` +
		auth.KeyLine(hk, "") + `"}],"computers":[{"public_key":"` + auth.KeyLine(ck, "anna@PC") +
		`","name":"anna@PC","registered_by":"alice","registered_at":"2026-01-02T03:04:05Z","approved":["a.example.com"]}]}`
	if err := os.WriteFile(s.Path(), []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	_, out, err := s.Update(func(st *State) error {
		if err := st.SetInfo("New", ""); err != nil {
			return err
		}
		_, err := st.RenameComputer(auth.Fingerprint(ck), "anna@LAPTOP")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	st := idx.State
	if st.Title != "New" || st.Computers[0].Name != "anna@LAPTOP" {
		t.Errorf("update lost: %s", out)
	}
	if string(st.Extra["msp"]) != `{"phone":"+1 234"}` {
		t.Errorf("state extra %q in %s", st.Extra, out)
	}
	if string(st.Techies[0].Extra["admin"]) != "true" {
		t.Errorf("techie extra %q", st.Techies[0].Extra)
	}
	if string(st.Computers[0].Extra["approved"]) != `["a.example.com"]` {
		t.Errorf("computer extra %q", st.Computers[0].Extra)
	}
	if _, ok := st.Extra["title"]; ok {
		t.Error("known field kept as extra")
	}
}
