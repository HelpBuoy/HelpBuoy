// Package salt keeps the server's verification salt (PLAN.md 12.4) in its
// own files in the state directory, never in state.json: an older binary
// that saves state.json drops fields it doesn't know.
//
//	verification_salt       the current salt, sent to computers and techies
//	verification_salt.next  the next salt, made by verification --new; it
//	                        replaces the current one at the next start
//
// Each file holds the salt in hex. A file is created once, mode 0600, and
// never rewritten; only Apply renames the next one over the current one.
package salt

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/helpbuoy/helpbuoy/proto/verify"
)

// File names in the state directory.
const (
	FileName = "verification_salt"
	NextName = FileName + ".next"
)

// Apply makes the next salt the current one. Only the server calls it, when
// it starts.
func Apply(dir string) (bool, error) {
	err := os.Rename(filepath.Join(dir, NextName), filepath.Join(dir, FileName))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// LoadOrCreate returns the current salt, making it when there is none.
func LoadOrCreate(dir string) ([]byte, error) {
	return loadOrCreate(dir, FileName)
}

// Next returns the next salt, making it when there is none. The clients
// don't see it until the server restarts.
func Next(dir string) ([]byte, error) {
	return loadOrCreate(dir, NextName)
}

// Pending returns the salt that becomes current at the next start, if any.
func Pending(dir string) ([]byte, bool, error) {
	b, err := read(filepath.Join(dir, NextName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return b, err == nil, err
}

func loadOrCreate(dir, name string) ([]byte, error) {
	p := filepath.Join(dir, name)
	b, err := read(p)
	if !errors.Is(err, os.ErrNotExist) {
		return b, err
	}
	if err := create(dir, p); err != nil {
		return nil, err
	}
	return read(p)
}

func read(p string) ([]byte, error) {
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	b, ok := verify.ParseSalt(strings.TrimSpace(string(raw)))
	if !ok {
		return nil, fmt.Errorf("salt %s: not %d bytes of hex", p, verify.SaltSize)
	}
	return b, nil
}

// create writes a new salt to p, unless p exists. The salt is written to a
// temporary file first and linked into place, so nobody ever reads a half
// written salt, and two processes starting at once both end up with the
// first one's salt.
func create(dir, p string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b := make([]byte, verify.SaltSize)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".salt-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteString(hex.EncodeToString(b) + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Link(tmp, p); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		// Hard links may be unsupported; rename only while p is still missing.
		if _, serr := os.Stat(p); serr == nil {
			return nil
		}
		return os.Rename(tmp, p)
	}
	return nil
}
