// Package config stores what the computer learns about its server in
// config.json in the app's data directory (PLAN.md section 0): the server
// address, the pinned server host key, the pinned key
// of every techie met on that server, and the hand-set performance mode. The app key lives next to it but is managed by
// proto/appkey.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/mevdschee/helpbuoy/proto/appkey"
)

// FileName is the configuration file inside the data directory.
const FileName = "config.json"

// Config is the stored computer configuration.
type Config struct {
	// ServerAddress is the normalized hostname:port of the server.
	ServerAddress string `json:"server_address,omitempty"`
	// ServerHostKey is the pinned server host key as an authorized_keys line.
	ServerHostKey string `json:"server_host_key,omitempty"`
	// KnownTechies maps a techie name to its pinned authorized_keys line.
	// The pins belong to ServerAddress: techies are vouched for by the server
	// and names are only unique within one server, so changing the server
	// address clears them.
	KnownTechies map[string]string `json:"known_techies,omitempty"`
	// LastAddress is the last address that reached a server, used to
	// prefill the address field on the next start.
	LastAddress string `json:"last_address,omitempty"`
	// PerformanceMode turns on solid wallpaper and no window animations
	// while a session runs (PLAN.md 5.2). Off by default; set it by hand in
	// config.json.
	PerformanceMode bool `json:"performance_mode,omitempty"`
}

// Registered reports whether the first-run flow has been completed.
func (c *Config) Registered() bool {
	return c != nil && c.ServerAddress != "" && c.ServerHostKey != ""
}

// Clone returns a deep copy.
func (c *Config) Clone() *Config {
	if c == nil {
		return &Config{}
	}
	n := *c
	if c.KnownTechies != nil {
		n.KnownTechies = make(map[string]string, len(c.KnownTechies))
		for k, v := range c.KnownTechies {
			n.KnownTechies[k] = v
		}
	}
	return &n
}

// Store reads and writes the configuration in one directory.
type Store struct {
	Dir string
}

// Path returns the full path of config.json.
func (s Store) Path() string { return filepath.Join(s.Dir, FileName) }

// Load reads the configuration. A missing file is not an error: it returns an
// empty Config. A damaged file is reported, so the caller can offer Reset.
func (s Store) Load() (*Config, error) {
	b, err := os.ReadFile(s.Path())
	if errors.Is(err, fs.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("config: %s: %w", s.Path(), err)
	}
	return &c, nil
}

// Save writes the configuration atomically: it writes a temporary file in the
// same directory, syncs it and renames it over config.json.
func (s Store) Save(c *Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.Dir, FileName+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	// os.CreateTemp creates the file with mode 0600.
	if _, err := f.Write(b); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.Path()); err != nil {
		return err
	}
	ok = true
	return nil
}

// Reset forgets the configuration by removing config.json. The app key is
// kept. A missing file is fine.
func (s Store) Reset() error {
	err := os.Remove(s.Path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// removeKey is appkey.Remove; a variable so tests don't depend on appkey.
var removeKey = appkey.Remove

// RemoveKey deletes the app key files and config.json. Only call it after the
// user confirmed it.
func (s Store) RemoveKey() error {
	if err := removeKey(s.Dir); err != nil {
		return err
	}
	return s.Reset()
}
