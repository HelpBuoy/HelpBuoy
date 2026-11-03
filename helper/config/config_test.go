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

	"github.com/mevdschee/help-me-win/proto/relayaddr"
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
	if c.RelayAddr != "" || c.RelayHostKey != "" || c.LocalRelay.Enabled {
		t.Errorf("defaults = %+v", c)
	}
	if c.LocalRelay.ListenPort() != relayaddr.DefaultPort {
		t.Errorf("default port = %d", c.LocalRelay.ListenPort())
	}
	if c.ConnectAddr() != "" {
		t.Errorf("ConnectAddr = %q", c.ConnectAddr())
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	k := newKey(t)
	c := &Config{RelayAddr: "relay.example.com:5938", LocalRelay: LocalRelay{Enabled: true, Port: 2300}, HideClientList: true}
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
	if got.ConnectAddr() != "127.0.0.1:2300" {
		t.Errorf("ConnectAddr = %q", got.ConnectAddr())
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
	if c == nil || c.RelayAddr != "" {
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
	before := c.RelayHostKey
	if _, err := c.Pin(k2); !errors.Is(err, ErrRelayIdentityChanged) {
		t.Fatalf("other key: err = %v", err)
	}
	if c.RelayHostKey != before {
		t.Error("pin changed after mismatch")
	}
	if err := CheckPinned(before, k2); err == nil || err.Error() != "relay identity changed" {
		t.Errorf("CheckPinned = %v", err)
	}
	if err := CheckPinned("", k2); err != nil {
		t.Errorf("empty pin: %v", err)
	}
	if err := CheckPinned("garbage", k2); err == nil {
		t.Error("damaged pin accepted a key")
	}
}

func TestSetRelayAddrForgetsPin(t *testing.T) {
	c := &Config{}
	a, _ := relayaddr.Parse("Relay.Example.com")
	if !c.SetRelayAddr(a) {
		t.Fatal("no change reported")
	}
	if c.RelayAddr != "relay.example.com:5938" {
		t.Errorf("addr = %q", c.RelayAddr)
	}
	c.Pin(newKey(t))
	if c.SetRelayAddr(a) {
		t.Error("same address reported as change")
	}
	if c.RelayHostKey == "" {
		t.Error("pin dropped for the same address")
	}
	b, _ := relayaddr.Parse("other.example.com:2223")
	c.SetRelayAddr(b)
	if c.RelayHostKey != "" {
		t.Error("pin kept for a different relay")
	}
}

func TestLoadDropsInvalidAddr(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(`{"relay_addr":"http://x y"}`), 0o600)
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.RelayAddr != "" {
		t.Errorf("invalid address kept: %q", c.RelayAddr)
	}
}
