// Package config stores the desk app's settings in its data directory:
// the last server address and its pinned server host key.
//
// The server host key is trust on first use (PLAN.md 3.5): it is pinned on the
// first successful connection and any later change is a hard error, never a
// silent update. Choosing a different server address forgets the pin.
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

	"github.com/helpbuoy/helpbuoy/proto/serveraddr"
)

// FileName is the settings file inside the data directory. The client app
// uses config.json in the same kind of directory, so the techie uses its own
// name to never read or overwrite the computer's file.
const FileName = "techie.json"

// ErrServerIdentityChanged is returned when the server presents a host key that
// differs from the pinned one.
var ErrServerIdentityChanged = errors.New("server identity changed")

// Config is the stored techie configuration.
type Config struct {
	// ServerAddr is the server address last connected to, as typed on the
	// connect screen (normalized hostname:port). localhost means the server
	// that runs inside this app.
	ServerAddr string `json:"server_addr,omitempty"`
	// ServerHostKey is the pinned host key of ServerAddr as an authorized_keys line.
	ServerHostKey string `json:"server_host_key,omitempty"`
}

// LocalPort is the port of the server inside this app when the address
// leaves it out. It is not 443 (the default for other servers), since a
// normal user can't listen on ports below 1024 on Linux and macOS.
const LocalPort = 5959

// ParseAddr parses a server address as typed in the desk app: like
// serveraddr.Parse, except that "localhost" without a port means LocalPort.
func ParseAddr(s string) (serveraddr.Addr, error) {
	if a, err := serveraddr.ParseDefault(s, LocalPort); err != nil || IsLocal(a) {
		return a, err
	}
	return serveraddr.Parse(s)
}

// IsLocal reports whether a names the server that runs inside this app.
func IsLocal(a serveraddr.Addr) bool { return a.Host == "localhost" }

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
	if c.ServerAddr != "" {
		if _, err := serveraddr.Parse(c.ServerAddr); err != nil {
			c.ServerAddr = ""
		}
	}
	if c.ServerHostKey != "" {
		if _, err := ParseHostKey(c.ServerHostKey); err != nil {
			return c, fmt.Errorf("%s: pinned server host key: %w", FileName, err)
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

// SetServerAddr stores a new server address. If it names a different server than
// before, the pinned host key is forgotten. It reports whether anything changed.
func (c *Config) SetServerAddr(a serveraddr.Addr) bool {
	s := a.String()
	if s == c.ServerAddr {
		return false
	}
	c.ServerAddr = s
	c.ServerHostKey = ""
	return true
}

// Pin applies trust on first use to key: with nothing pinned it pins key and
// reports true; with the same key pinned it reports false; with a different
// key pinned it returns ErrServerIdentityChanged and keeps the old pin.
func (c *Config) Pin(key ssh.PublicKey) (bool, error) {
	if err := CheckPinned(c.ServerHostKey, key); err != nil {
		return false, err
	}
	if c.ServerHostKey != "" {
		return false, nil
	}
	c.ServerHostKey = HostKeyLine(key)
	return true, nil
}

// ForgetHostKey drops the pinned server key. Only for an explicit user action.
func (c *Config) ForgetHostKey() { c.ServerHostKey = "" }

// CheckPinned compares key with a pinned authorized_keys line. An empty pin
// accepts any key; a damaged pin accepts none.
func CheckPinned(pinned string, key ssh.PublicKey) error {
	if pinned == "" {
		return nil
	}
	want, err := ParseHostKey(pinned)
	if err != nil {
		return fmt.Errorf("pinned server host key: %w", err)
	}
	if !bytes.Equal(want.Marshal(), key.Marshal()) {
		return ErrServerIdentityChanged
	}
	return nil
}

// ParseHostKey parses an authorized_keys line holding a server host key.
func ParseHostKey(line string) (ssh.PublicKey, error) {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	return key, err
}

// HostKeyLine formats key as an authorized_keys line without comment.
func HostKeyLine(key ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}
