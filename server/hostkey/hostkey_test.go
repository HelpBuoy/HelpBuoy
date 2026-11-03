package hostkey

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadOrCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s1, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := os.ReadFile(filepath.Join(dir, FileName))
	s2, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(filepath.Join(dir, FileName))
	if string(s1.PublicKey().Marshal()) != string(s2.PublicKey().Marshal()) || string(b1) != string(b2) {
		t.Fatal("key changed on second load")
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(dir, FileName))
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode %v", fi.Mode())
		}
	}
	if !strings.HasPrefix(PublicLine(s1), "ssh-ed25519 ") {
		t.Error(PublicLine(s1))
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("files %v", ents)
	}
}

func TestDamagedKeyIsNotReplaced(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)
	os.WriteFile(p, []byte("junk"), 0o600)
	if _, err := LoadOrCreate(dir); err == nil {
		t.Fatal("damaged key accepted")
	}
	if b, _ := os.ReadFile(p); string(b) != "junk" {
		t.Fatal("damaged key was overwritten")
	}
}
