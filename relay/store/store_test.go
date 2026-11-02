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

	"github.com/mevdschee/help-me-win/proto/control"
	"github.com/mevdschee/help-me-win/relay/auth"
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
	if err != nil || raw != nil || len(idx.State.Helpers) != 0 {
		t.Fatalf("empty load: %v", err)
	}
	hk, ck := newKey(t), newKey(t)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	_, _, err = s.Update(func(st *State) error {
		if err := st.AddHelper("alice", "Alice", auth.KeyLine(hk, "laptop")); err != nil {
			return err
		}
		_, _, err := st.AddClient(auth.KeyLine(ck, "anna@PC"), "", "alice", now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	idx, _, err = s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if id := idx.Lookup(auth.Fingerprint(hk)); id.Role != control.RoleHelper || id.Name != "alice" || id.DisplayName != "Alice" {
		t.Errorf("helper %+v", id)
	}
	if id := idx.Lookup(auth.Fingerprint(ck)); id.Role != control.RoleClient || id.Name != "anna@PC" {
		t.Errorf("client %+v", id)
	}
	if id := idx.Lookup("SHA256:nope"); id.Role != control.RolePending {
		t.Errorf("unknown %+v", id)
	}
	c, _ := idx.Client(auth.Fingerprint(ck))
	if c.RegisteredBy != "alice" || !c.RegisteredAt.Equal(now) {
		t.Errorf("client %+v", c)
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
	if err := st.AddHelper("alice", "", auth.KeyLine(hk, "")); err != nil {
		t.Fatal(err)
	}
	if st.Helpers[0].DisplayName != "alice" {
		t.Error("display name default")
	}
	if err := st.AddHelper("alice", "", auth.KeyLine(newKey(t), "")); err == nil {
		t.Error("duplicate helper name")
	}
	if err := st.AddHelper("bob", "", auth.KeyLine(hk, "")); err == nil {
		t.Error("duplicate helper key")
	}
	if err := st.AddHelper("with space", "", auth.KeyLine(newKey(t), "")); err == nil {
		t.Error("helper name with space")
	}
	if _, _, err := st.AddClient(auth.KeyLine(hk, "x@y"), "", "alice", at); err == nil {
		t.Error("helper key registered as client")
	}
	if _, _, err := st.AddClient(auth.KeyLine(ck, ""), "", "alice", at); err == nil {
		t.Error("client without name")
	}
	if _, name, err := st.AddClient(auth.KeyLine(ck, ""), "anna@PC", "alice", at); err != nil || name != "anna@PC" {
		t.Errorf("client with name: %v", err)
	}
	if _, _, err := st.AddClient(auth.KeyLine(ck, "again@PC"), "", "alice", at); err == nil {
		t.Error("duplicate client")
	}
	if err := st.AddHelper("carol", "", auth.KeyLine(ck, "")); err == nil {
		t.Error("client key added as helper")
	}
	if _, err := st.RemoveClient("SHA256:nope"); err == nil {
		t.Error("removed unknown client")
	}
	if name, err := st.RemoveClient(auth.Fingerprint(ck)); err != nil || name != "anna@PC" {
		t.Errorf("remove client %q %v", name, err)
	}
	if err := st.RemoveHelper("nobody"); err == nil {
		t.Error("removed unknown helper")
	}
	if err := st.RemoveHelper("alice"); err != nil || len(st.Helpers) != 0 {
		t.Errorf("remove helper %v", err)
	}
}

func TestParseRejectsBadState(t *testing.T) {
	hk := newKey(t)
	line := auth.KeyLine(hk, "")
	for _, s := range []string{
		`{`,
		`{"helpers":[{"name":"a","display_name":"A","public_key":"garbage"}]}`,
		`{"helpers":[{"name":"a","public_key":"` + line + `"},{"name":"b","public_key":"` + line + `"}]}`,
		`{"helpers":[{"name":"a","public_key":"` + line + `"}],"clients":[{"name":"x","public_key":"` + line + `"}]}`,
		`{"unknown":1}`,
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
				_, _, err := st.AddClient(auth.KeyLine(k, "u@pc"+string(rune('a'+i))), "", "x", time.Now())
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
	if err != nil || len(idx.State.Clients) != 20 {
		t.Fatalf("clients %d, %v", len(idx.State.Clients), err)
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
	st.AddClient(auth.KeyLine(k, "a@b"), "", "x", time.Now())
	t1 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	st.SetLastSeen(map[string]time.Time{auth.Fingerprint(k): t1})
	st.SetLastSeen(map[string]time.Time{auth.Fingerprint(k): t1.Add(-time.Hour)})
	if !st.Clients[0].LastSeen.Equal(t1) {
		t.Errorf("last seen %v", st.Clients[0].LastSeen)
	}
}
