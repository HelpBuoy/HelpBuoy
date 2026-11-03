// Package config stores the helper app's settings in its data directory:
// the last relay address and its pinned relay host key.
//
// The relay host key is trust on first use (PLAN.md 3.5): it is pinned on the
// first successful connection and any later change is a hard error, never a
// silent update. Choosing a different relay address forgets the pin.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/helpbuoy/proto/relayaddr"
)

// FileName is the settings file inside the data directory. The client app
// uses config.json in the same kind of directory, so the helper uses its own
// name to never read or overwrite the client's file.
const FileName = "helper.json"

// ErrRelayIdentityChanged is returned when the relay presents a host key that
// differs from the pinned one.
var ErrRelayIdentityChanged = errors.New("relay identity changed")

// Config is the stored helper configuration.
type Config struct {
	// RelayAddr is the relay address last connected to, as typed on the
	// connect screen (normalized hostname:port). localhost means the relay
	// that runs inside this app.
	RelayAddr string `json:"relay_addr,omitempty"`
	// RelayHostKey is the pinned host key of RelayAddr as an authorized_keys line.
	RelayHostKey string `json:"relay_host_key,omitempty"`
}

// IsLocal reports whether a names the relay that runs inside this app.
func IsLocal(a relayaddr.Addr) bool { return a.Host == "localhost" }

// Load reads the configuration from dir. A missing file gives the defaults
// and no error. A damaged file gives the defaults and an error, so the app can
// show the problem.
func Load(dir string) (*Config, error) {
	c := &Config{}
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return &Config{}, fmt.Errorf("%s is damaged: %w", FileName, err)
	}
	if c.RelayAddr != "" {
		if _, err := relayaddr.Parse(c.RelayAddr); err != nil {
			c.RelayAddr = ""
		}
	}
	if c.RelayHostKey != "" {
		if _, err := ParseHostKey(c.RelayHostKey); err != nil {
			return c, fmt.Errorf("%s: pinned relay host key: %w", FileName, err)
		}
	}
	return c, nil
}

// Save writes the configuration to dir atomically (temp file, fsync, rename).
func (c *Config) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.CreateTemp(dir, FileName+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename
	if err := f.Chmod(0o600); err != nil && !isUnsupported(err) {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
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
	return os.Rename(tmp, filepath.Join(dir, FileName))
}

// isUnsupported reports errors from Chmod on systems without Unix modes.
func isUnsupported(err error) bool {
	return errors.Is(err, errors.ErrUnsupported)
}

// SetRelayAddr stores a new relay address. If it names a different relay than
// before, the pinned host key is forgotten. It reports whether anything changed.
func (c *Config) SetRelayAddr(a relayaddr.Addr) bool {
	s := a.String()
	if s == c.RelayAddr {
		return false
	}
	c.RelayAddr = s
	c.RelayHostKey = ""
	return true
}

// Pin applies trust on first use to key: with nothing pinned it pins key and
// reports true; with the same key pinned it reports false; with a different
// key pinned it returns ErrRelayIdentityChanged and keeps the old pin.
func (c *Config) Pin(key ssh.PublicKey) (bool, error) {
	if err := CheckPinned(c.RelayHostKey, key); err != nil {
		return false, err
	}
	if c.RelayHostKey != "" {
		return false, nil
	}
	c.RelayHostKey = HostKeyLine(key)
	return true, nil
}

// ForgetHostKey drops the pinned relay key. Only for an explicit user action.
func (c *Config) ForgetHostKey() { c.RelayHostKey = "" }

// CheckPinned compares key with a pinned authorized_keys line. An empty pin
// accepts any key; a damaged pin accepts none.
func CheckPinned(pinned string, key ssh.PublicKey) error {
	if pinned == "" {
		return nil
	}
	want, err := ParseHostKey(pinned)
	if err != nil {
		return fmt.Errorf("pinned relay host key: %w", err)
	}
	if !bytes.Equal(want.Marshal(), key.Marshal()) {
		return ErrRelayIdentityChanged
	}
	return nil
}

// ParseHostKey parses an authorized_keys line holding a relay host key.
func ParseHostKey(line string) (ssh.PublicKey, error) {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	return key, err
}

// HostKeyLine formats key as an authorized_keys line without comment.
func HostKeyLine(key ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}
