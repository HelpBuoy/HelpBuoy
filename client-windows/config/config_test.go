package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadMissing(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	c, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Registered() {
		t.Fatal("empty config reports registered")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "helpbuoy")}
	in := &Config{
		RelayAddress: "relay.example.com:5959",
		RelayHostKey: "ssh-ed25519 AAAA",
		KnownHelpers: map[string]string{"alice": "ssh-ed25519 BBBB"},
	}
	if err := s.Save(in); err != nil {
		t.Fatal(err)
	}
	out, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("got %+v, want %+v", out, in)
	}
	if !out.Registered() {
		t.Fatal("not registered")
	}
	// Overwrite works and leaves no temp files behind.
	in.RelayAddress = "other.example.com:5959"
	if err := s.Save(in); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(s.Dir)
	if len(ents) != 1 || ents[0].Name() != FileName {
		t.Fatalf("directory has %v", ents)
	}
	if fi, _ := os.Stat(s.Path()); fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("config mode %v", fi.Mode())
	}
}

func TestLoadDamaged(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	os.WriteFile(s.Path(), []byte("{not json"), 0o600)
	if _, err := s.Load(); err == nil {
		t.Fatal("damaged config loaded")
	}
}

func TestResetKeepsKey(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	key := filepath.Join(s.Dir, "id_ed25519")
	os.WriteFile(key, []byte("key"), 0o600)
	s.Save(&Config{RelayAddress: "a:1"})
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	if err := s.Reset(); err != nil {
		t.Fatal("second reset:", err)
	}
	if _, err := os.Stat(s.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("config still there")
	}
	if _, err := os.Stat(key); err != nil {
		t.Fatal("key removed by Reset")
	}
}

func TestRemoveKey(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	var removed string
	orig := removeKey
	defer func() { removeKey = orig }()
	removeKey = func(dir string) error { removed = dir; return nil }
	s.Save(&Config{RelayAddress: "a:1"})
	if err := s.RemoveKey(); err != nil {
		t.Fatal(err)
	}
	if removed != s.Dir {
		t.Fatalf("appkey.Remove called with %q", removed)
	}
	if _, err := os.Stat(s.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("config still there")
	}

	// A failing key removal keeps the config.
	s.Save(&Config{RelayAddress: "a:1"})
	removeKey = func(string) error { return errors.New("locked") }
	if err := s.RemoveKey(); err == nil {
		t.Fatal("no error")
	}
	if _, err := os.Stat(s.Path()); err != nil {
		t.Fatal("config removed although the key was not")
	}
}

func TestClone(t *testing.T) {
	c := &Config{KnownHelpers: map[string]string{"a": "x"}}
	n := c.Clone()
	n.KnownHelpers["a"] = "y"
	if c.KnownHelpers["a"] != "x" {
		t.Fatal("clone shares map")
	}
}
