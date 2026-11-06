package salt

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/helpbuoy/helpbuoy/server/store"
)

func TestLoadOrCreate(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadOrCreate(dir)
	if err != nil || len(a) != 32 {
		t.Fatalf("LoadOrCreate = %x, %v", a, err)
	}
	b, err := LoadOrCreate(dir)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatalf("second LoadOrCreate = %x, %v, want %x", b, err, a)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, FileName))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v, %v", fi.Mode(), err)
		}
	}
}

func TestDamagedIsAnError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)
	os.WriteFile(p, []byte("abcd\n"), 0o600)
	if _, err := LoadOrCreate(dir); err == nil {
		t.Fatal("a damaged salt was accepted")
	}
	if b, _ := os.ReadFile(p); string(b) != "abcd\n" {
		t.Fatal("a damaged salt was replaced")
	}
}

// An older binary that saves state.json must not touch the salt.
func TestStateSaveKeepsSalt(t *testing.T) {
	dir := t.TempDir()
	a, _ := LoadOrCreate(dir)
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Update(func(s *store.State) error { s.Title = "Example Inc"; return nil }); err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreate(dir)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatalf("salt changed after saving state.json: %x -> %x (%v)", a, b, err)
	}
}

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	cur, _ := LoadOrCreate(dir)
	if ok, err := Apply(dir); ok || err != nil {
		t.Fatalf("Apply without a next salt = %v, %v", ok, err)
	}
	next, err := Next(dir)
	if err != nil || bytes.Equal(next, cur) {
		t.Fatalf("Next = %x, %v", next, err)
	}
	if again, _ := Next(dir); !bytes.Equal(again, next) {
		t.Fatal("Next made another salt")
	}
	if p, ok, _ := Pending(dir); !ok || !bytes.Equal(p, next) {
		t.Fatal("Pending does not report the next salt")
	}
	if b, _ := LoadOrCreate(dir); !bytes.Equal(b, cur) {
		t.Fatal("the current salt changed before the restart")
	}
	if ok, err := Apply(dir); !ok || err != nil {
		t.Fatalf("Apply = %v, %v", ok, err)
	}
	if b, _ := LoadOrCreate(dir); !bytes.Equal(b, next) {
		t.Fatal("Apply did not make the next salt current")
	}
	if _, ok, _ := Pending(dir); ok {
		t.Fatal("still pending after Apply")
	}
}
