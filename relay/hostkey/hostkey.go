// Package hostkey creates and loads the relay's Ed25519 host key,
// hostkey_ed25519 in the state directory (OpenSSH format, mode 0600).
package hostkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// FileName is the host key file inside the state directory.
const FileName = "hostkey_ed25519"

// LoadOrCreate loads the host key from dir, generating it on first use. An
// existing file that can't be parsed is an error; it is never replaced.
func LoadOrCreate(dir string) (ssh.Signer, error) {
	p := filepath.Join(dir, FileName)
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		if err := create(dir, p); err != nil {
			return nil, err
		}
		b, err = os.ReadFile(p)
	}
	if err != nil {
		return nil, err
	}
	s, err := ssh.ParsePrivateKey(b)
	if err != nil {
		return nil, fmt.Errorf("host key %s: %w", p, err)
	}
	if s.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return nil, fmt.Errorf("host key %s: not an ed25519 key", p)
	}
	return s, nil
}

func create(dir, p string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	blk, err := ssh.MarshalPrivateKey(priv, "helpbuoy-relay host key")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".hostkey-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(pem.EncodeToMemory(blk)); err != nil {
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
	// Link instead of rename so that two processes starting at once can't
	// both win: the second one simply loads the first one's key.
	if err := os.Link(tmp, p); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		// Hard links may be unsupported (some file systems); fall back to a rename
		// that only happens when the file is still missing.
		if _, serr := os.Stat(p); serr == nil {
			return nil
		}
		return os.Rename(tmp, p)
	}
	return nil
}

// PublicLine returns the host key as an authorized_keys line.
func PublicLine(s ssh.Signer) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey())))
}
