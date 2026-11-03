package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/helpbuoy/proto/serveraddr"
)

func newKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestLoadMissingGivesDefaults(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.ServerAddr != "" || c.ServerHostKey != "" {
		t.Errorf("defaults = %+v", c)
	}
}

func TestIsLocal(t *testing.T) {
	for s, want := range map[string]bool{"localhost": true, "localhost:2300": true, "helpme.example.com": false, "127.0.0.1": false} {
		a, err := serveraddr.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		if IsLocal(a) != want {
			t.Errorf("IsLocal(%s) = %v", s, !want)
		}
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	k := newKey(t)
	c := &Config{ServerAddr: "server.example.com:5959"}
	if _, err := c.Pin(k); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(dir); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if *got != *c {
		t.Errorf("got %+v, want %+v", got, c)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, FileName))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v", fi.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("leftover files: %v", entries)
	}
}

func TestLoadDamaged(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte("{nope"), 0o600)
	c, err := Load(dir)
	if err == nil {
		t.Fatal("no error for damaged file")
	}
	if c == nil || c.ServerAddr != "" {
		t.Errorf("want defaults, got %+v", c)
	}
}

func TestPinTOFU(t *testing.T) {
	k1, k2 := newKey(t), newKey(t)
	c := &Config{}
	pinned, err := c.Pin(k1)
	if err != nil || !pinned {
		t.Fatalf("first pin = %v, %v", pinned, err)
	}
	pinned, err = c.Pin(k1)
	if err != nil || pinned {
		t.Fatalf("same key = %v, %v", pinned, err)
	}
	before := c.ServerHostKey
	if _, err := c.Pin(k2); !errors.Is(err, ErrServerIdentityChanged) {
		t.Fatalf("other key: err = %v", err)
	}
	if c.ServerHostKey != before {
		t.Error("pin changed after mismatch")
	}
	if err := CheckPinned(before, k2); err == nil || err.Error() != "server identity changed" {
		t.Errorf("CheckPinned = %v", err)
	}
	if err := CheckPinned("", k2); err != nil {
		t.Errorf("empty pin: %v", err)
	}
	if err := CheckPinned("garbage", k2); err == nil {
		t.Error("damaged pin accepted a key")
	}
}

func TestSetServerAddrForgetsPin(t *testing.T) {
	c := &Config{}
	a, _ := serveraddr.Parse("Server.Example.com")
	if !c.SetServerAddr(a) {
		t.Fatal("no change reported")
	}
	if c.ServerAddr != "server.example.com:5959" {
		t.Errorf("addr = %q", c.ServerAddr)
	}
	c.Pin(newKey(t))
	if c.SetServerAddr(a) {
		t.Error("same address reported as change")
	}
	if c.ServerHostKey == "" {
		t.Error("pin dropped for the same address")
	}
	b, _ := serveraddr.Parse("other.example.com:2223")
	c.SetServerAddr(b)
	if c.ServerHostKey != "" {
		t.Error("pin kept for a different server")
	}
}

func TestLoadDropsInvalidAddr(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(`{"server_addr":"http://x y"}`), 0o600)
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.ServerAddr != "" {
		t.Errorf("invalid address kept: %q", c.ServerAddr)
	}
}
