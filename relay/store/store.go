// Package store keeps the relay's persistent state, state.json in the state
// directory: the helper keys and the registered client keys.
//
// Writes go to a temporary file that is synced and then renamed over
// state.json, so readers always see a complete file. A lock file serializes
// writers across processes (the running relay and the admin CLI); every
// update reads the file fresh under that lock, so neither side overwrites a
// change made by the other.
package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mevdschee/helpbuoy/proto/control"
	"github.com/mevdschee/helpbuoy/relay/auth"
)

// File names inside the state directory.
const (
	FileName = "state.json"
	lockName = "state.json.lock"
)

// Name length limits.
const (
	MaxHelperName = 64
	MaxClientName = 128
)

// Helper is a helper key added by the relay admin.
type Helper struct {
	Name      string `json:"name"`
	PublicKey string `json:"public_key"` // authorized_keys line
}

// Client is a registered client key.
type Client struct {
	PublicKey    string    `json:"public_key"` // authorized_keys line
	Name         string    `json:"name"`       // user@machine
	RegisteredBy string    `json:"registered_by"`
	RegisteredAt time.Time `json:"registered_at"`
	LastSeen     time.Time `json:"last_seen,omitzero"`
}

// State is the content of state.json.
type State struct {
	Helpers []Helper `json:"helpers"`
	Clients []Client `json:"clients"`
}

// Identity is what a fingerprint maps to.
type Identity struct {
	Role    control.Role
	Name    string // helper name or client name; empty for pending
	KeyLine string // authorized_keys line; empty for pending
}

// Index is a read-only lookup view of a State by key fingerprint. It is
// immutable once built and safe for concurrent use.
type Index struct {
	State   *State
	helpers map[string]int // fingerprint -> index in State.Helpers
	clients map[string]int
	fps     []string // fingerprint of State.Clients[i]
	hfps    []string // fingerprint of State.Helpers[i]
}

// NewIndex validates st and builds its index. st must not be changed afterwards.
func NewIndex(st *State) (*Index, error) {
	idx := &Index{State: st, helpers: map[string]int{}, clients: map[string]int{}}
	names := map[string]bool{}
	for i, h := range st.Helpers {
		if _, err := auth.CleanName(h.Name, MaxHelperName); err != nil {
			return nil, fmt.Errorf("helper %q: %w", h.Name, err)
		}
		if names[h.Name] {
			return nil, fmt.Errorf("helper %q is listed twice", h.Name)
		}
		names[h.Name] = true
		k, _, err := auth.ParseKey(h.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("helper %q: %w", h.Name, err)
		}
		fp := auth.Fingerprint(k)
		if _, dup := idx.helpers[fp]; dup {
			return nil, fmt.Errorf("helper %q: key %s is used by another helper", h.Name, fp)
		}
		idx.helpers[fp] = i
		idx.hfps = append(idx.hfps, fp)
	}
	for i, c := range st.Clients {
		k, _, err := auth.ParseKey(c.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("client %q: %w", c.Name, err)
		}
		fp := auth.Fingerprint(k)
		if _, dup := idx.helpers[fp]; dup {
			return nil, fmt.Errorf("client %q: key %s is a helper key", c.Name, fp)
		}
		if _, dup := idx.clients[fp]; dup {
			return nil, fmt.Errorf("client %q: key %s is registered twice", c.Name, fp)
		}
		idx.clients[fp] = i
		idx.fps = append(idx.fps, fp)
	}
	return idx, nil
}

// Lookup returns the identity for a fingerprint. Unknown keys are pending.
func (x *Index) Lookup(fp string) Identity {
	if i, ok := x.helpers[fp]; ok {
		h := x.State.Helpers[i]
		return Identity{Role: control.RoleHelper, Name: h.Name, KeyLine: h.PublicKey}
	}
	if i, ok := x.clients[fp]; ok {
		c := x.State.Clients[i]
		return Identity{Role: control.RoleClient, Name: c.Name, KeyLine: c.PublicKey}
	}
	return Identity{Role: control.RolePending}
}

// HelperFingerprint returns the fingerprint of State.Helpers[i].
func (x *Index) HelperFingerprint(i int) string { return x.hfps[i] }

// ClientFingerprint returns the fingerprint of State.Clients[i].
func (x *Index) ClientFingerprint(i int) string { return x.fps[i] }

// Client returns the registered client with the fingerprint.
func (x *Index) Client(fp string) (Client, bool) {
	i, ok := x.clients[fp]
	if !ok {
		return Client{}, false
	}
	return x.State.Clients[i], true
}

// Clone returns a deep copy.
func (st *State) Clone() *State {
	return &State{Helpers: slices.Clone(st.Helpers), Clients: slices.Clone(st.Clients)}
}

// Store reads and writes state.json in a directory.
type Store struct {
	dir string
}

// Open returns the store for a state directory, creating the directory
// (mode 0700) if needed.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// Dir returns the state directory.
func (s *Store) Dir() string { return s.dir }

// Path returns the path of state.json.
func (s *Store) Path() string { return filepath.Join(s.dir, FileName) }

// ReadRaw returns the bytes of state.json, or nil if it does not exist.
func (s *Store) ReadRaw() ([]byte, error) {
	b, err := os.ReadFile(s.Path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// Parse decodes and validates state.json content. Empty content is an empty state.
func Parse(b []byte) (*Index, error) {
	st := &State{}
	if len(bytes.TrimSpace(b)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(st); err != nil {
			return nil, fmt.Errorf("%s: %w", FileName, err)
		}
	}
	idx, err := NewIndex(st)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	return idx, nil
}

// Load reads and validates state.json.
func (s *Store) Load() (*Index, []byte, error) {
	b, err := s.ReadRaw()
	if err != nil {
		return nil, nil, err
	}
	idx, err := Parse(b)
	return idx, b, err
}

// Update locks the store, reads the current state, applies fn to a copy,
// validates the result and writes it. It returns the new index and the bytes
// written. If fn returns an error nothing is written.
func (s *Store) Update(fn func(*State) error) (*Index, []byte, error) {
	unlock, err := s.lock()
	if err != nil {
		return nil, nil, err
	}
	defer unlock()
	idx, _, err := s.Load()
	if err != nil {
		return nil, nil, err
	}
	st := idx.State.Clone()
	if err := fn(st); err != nil {
		return nil, nil, err
	}
	nidx, err := NewIndex(st)
	if err != nil {
		return nil, nil, err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	b = append(b, '\n')
	if err := writeAtomic(s.Path(), b); err != nil {
		return nil, nil, err
	}
	return nidx, b, nil
}

// lock takes the cross-process lock file. A lock older than staleLock is
// assumed to be left over from a crashed process and is broken.
func (s *Store) lock() (func(), error) {
	const staleLock = 10 * time.Second
	p := filepath.Join(s.dir, lockName)
	deadline := time.Now().Add(15 * time.Second)
	for {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			return func() { os.Remove(p) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if fi, err := os.Stat(p); err == nil && time.Since(fi.ModTime()) > staleLock {
			os.Remove(p)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("state is locked by another process (%s)", p)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// writeAtomic writes b to path via a synced temporary file and a rename.
func writeAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
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
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	// On Windows a rename can fail briefly while another process has the
	// target open, so retry a few times.
	for i := 0; ; i++ {
		err = os.Rename(tmp, path)
		if err == nil || i == 20 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync() // best effort; not supported on every OS
		d.Close()
	}
	return nil
}

// AddHelper adds a helper. The key line is normalized.
func (st *State) AddHelper(name, keyLine string) error {
	name, err := auth.CleanName(name, MaxHelperName)
	if err != nil {
		return err
	}
	if strings.ContainsAny(name, " \t") {
		return errors.New("helper name must not contain spaces")
	}
	k, comment, err := auth.ParseKey(keyLine)
	if err != nil {
		return err
	}
	fp := auth.Fingerprint(k)
	for _, h := range st.Helpers {
		if h.Name == name {
			return fmt.Errorf("helper %q already exists", name)
		}
		if sameKey(h.PublicKey, fp) {
			return fmt.Errorf("key %s is already the key of helper %q", fp, h.Name)
		}
	}
	for _, c := range st.Clients {
		if sameKey(c.PublicKey, fp) {
			return fmt.Errorf("key %s is registered as client %q; a key can't be both", fp, c.Name)
		}
	}
	st.Helpers = append(st.Helpers, Helper{Name: name, PublicKey: auth.KeyLine(k, comment)})
	return nil
}

// RemoveHelper removes a helper by name.
func (st *State) RemoveHelper(name string) error {
	i := slices.IndexFunc(st.Helpers, func(h Helper) bool { return h.Name == name })
	if i < 0 {
		return fmt.Errorf("no helper %q", name)
	}
	st.Helpers = slices.Delete(st.Helpers, i, i+1)
	return nil
}

// AddClient registers a client key. name overrides the key comment and is
// required when the comment is empty. It returns the fingerprint and name.
func (st *State) AddClient(keyLine, name, by string, at time.Time) (fp, regName string, err error) {
	k, comment, err := auth.ParseKey(keyLine)
	if err != nil {
		return "", "", err
	}
	if name == "" {
		name = comment
	}
	if name == "" {
		return "", "", errors.New("the key has no comment, so a name is required")
	}
	if name, err = auth.CleanName(name, MaxClientName); err != nil {
		return "", "", err
	}
	fp = auth.Fingerprint(k)
	for _, h := range st.Helpers {
		if sameKey(h.PublicKey, fp) {
			return "", "", fmt.Errorf("key %s is the key of helper %q; a key can't be both", fp, h.Name)
		}
	}
	for _, c := range st.Clients {
		if sameKey(c.PublicKey, fp) {
			return "", "", fmt.Errorf("key %s is already registered as %q", fp, c.Name)
		}
	}
	st.Clients = append(st.Clients, Client{
		PublicKey:    auth.KeyLine(k, name),
		Name:         name,
		RegisteredBy: by,
		RegisteredAt: at.UTC(),
	})
	return fp, name, nil
}

// RemoveClient removes a client by fingerprint and returns its name.
// RenameClient sets the name of the client with fingerprint fp, also as the
// comment of its stored key line. It returns the old name.
func (st *State) RenameClient(fp, name string) (string, error) {
	i := slices.IndexFunc(st.Clients, func(c Client) bool { return sameKey(c.PublicKey, fp) })
	if i < 0 {
		return "", fmt.Errorf("no client with fingerprint %s", fp)
	}
	name, err := auth.CleanName(name, MaxClientName)
	if err != nil {
		return "", err
	}
	k, _, err := auth.ParseKey(st.Clients[i].PublicKey)
	if err != nil {
		return "", err
	}
	old := st.Clients[i].Name
	st.Clients[i].Name = name
	st.Clients[i].PublicKey = auth.KeyLine(k, name)
	return old, nil
}

func (st *State) RemoveClient(fp string) (string, error) {
	i := slices.IndexFunc(st.Clients, func(c Client) bool { return sameKey(c.PublicKey, fp) })
	if i < 0 {
		return "", fmt.Errorf("no client with fingerprint %s", fp)
	}
	name := st.Clients[i].Name
	st.Clients = slices.Delete(st.Clients, i, i+1)
	return name, nil
}

// SetLastSeen updates last-seen times of clients by fingerprint, keeping the later time.
func (st *State) SetLastSeen(seen map[string]time.Time) {
	for i, c := range st.Clients {
		k, _, err := auth.ParseKey(c.PublicKey)
		if err != nil {
			continue
		}
		if t, ok := seen[auth.Fingerprint(k)]; ok && t.After(c.LastSeen) {
			st.Clients[i].LastSeen = t.UTC()
		}
	}
}

func sameKey(line, fp string) bool {
	k, _, err := auth.ParseKey(line)
	return err == nil && auth.Fingerprint(k) == fp
}
