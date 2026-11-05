// Package store keeps the server's persistent state, state.json in the state
// directory: the techie keys and the registered computer keys.
//
// Writes go to a temporary file that is synced and then renamed over
// state.json, so readers always see a complete file. A lock file serializes
// writers across processes (the running server and the admin CLI); every
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

	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/server/auth"
)

// File names inside the state directory.
const (
	FileName = "state.json"
	lockName = "state.json.lock"
)

// Name length limits.
const (
	MaxTechieName   = 64
	MaxComputerName = 128
	MaxTitle        = 64
	MaxInstruction  = 160
)

// Techie is a techie key added by the server admin.
type Techie struct {
	Name      string `json:"name"`
	PublicKey string `json:"public_key"` // authorized_keys line
}

// Computer is a registered computer key.
type Computer struct {
	PublicKey    string    `json:"public_key"` // authorized_keys line
	Name         string    `json:"name"`       // user@machine
	RegisteredBy string    `json:"registered_by"`
	RegisteredAt time.Time `json:"registered_at"`
	LastSeen     time.Time `json:"last_seen,omitzero"`
}

// State is the content of state.json.
type State struct {
	Title       string     `json:"title,omitempty"`       // shown by the apps, see control.ServerInfo
	Instruction string     `json:"instruction,omitempty"` // shown by the apps, see control.ServerInfo
	Techies     []Techie   `json:"techies"`
	Computers   []Computer `json:"computers"`
}

// Identity is what a fingerprint maps to.
type Identity struct {
	Role    control.Role
	Name    string // techie name or computer name; empty for pending
	KeyLine string // authorized_keys line; empty for pending
}

// Index is a read-only lookup view of a State by key fingerprint. It is
// immutable once built and safe for concurrent use.
type Index struct {
	State     *State
	techies   map[string]int // fingerprint -> index in State.Techies
	computers map[string]int
	fps       []string // fingerprint of State.Computers[i]
	hfps      []string // fingerprint of State.Techies[i]
}

// NewIndex validates st and builds its index. st must not be changed afterwards.
func NewIndex(st *State) (*Index, error) {
	idx := &Index{State: st, techies: map[string]int{}, computers: map[string]int{}}
	if _, err := cleanLine("title", st.Title, MaxTitle); err != nil {
		return nil, err
	}
	if _, err := cleanLine("instruction", st.Instruction, MaxInstruction); err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for i, h := range st.Techies {
		if _, err := auth.CleanName(h.Name, MaxTechieName); err != nil {
			return nil, fmt.Errorf("techie %q: %w", h.Name, err)
		}
		if names[h.Name] {
			return nil, fmt.Errorf("techie %q is listed twice", h.Name)
		}
		names[h.Name] = true
		k, _, err := auth.ParseKey(h.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("techie %q: %w", h.Name, err)
		}
		fp := auth.Fingerprint(k)
		if _, dup := idx.techies[fp]; dup {
			return nil, fmt.Errorf("techie %q: key %s is used by another techie", h.Name, fp)
		}
		idx.techies[fp] = i
		idx.hfps = append(idx.hfps, fp)
	}
	for i, c := range st.Computers {
		k, _, err := auth.ParseKey(c.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("computer %q: %w", c.Name, err)
		}
		fp := auth.Fingerprint(k)
		if _, dup := idx.techies[fp]; dup {
			return nil, fmt.Errorf("computer %q: key %s is a techie key", c.Name, fp)
		}
		if _, dup := idx.computers[fp]; dup {
			return nil, fmt.Errorf("computer %q: key %s is registered twice", c.Name, fp)
		}
		idx.computers[fp] = i
		idx.fps = append(idx.fps, fp)
	}
	return idx, nil
}

// Lookup returns the identity for a fingerprint. Unknown keys are pending.
func (x *Index) Lookup(fp string) Identity {
	if i, ok := x.techies[fp]; ok {
		h := x.State.Techies[i]
		return Identity{Role: control.RoleTechie, Name: h.Name, KeyLine: h.PublicKey}
	}
	if i, ok := x.computers[fp]; ok {
		c := x.State.Computers[i]
		return Identity{Role: control.RoleComputer, Name: c.Name, KeyLine: c.PublicKey}
	}
	return Identity{Role: control.RolePending}
}

// TechieFingerprint returns the fingerprint of State.Techies[i].
func (x *Index) TechieFingerprint(i int) string { return x.hfps[i] }

// ComputerFingerprint returns the fingerprint of State.Computers[i].
func (x *Index) ComputerFingerprint(i int) string { return x.fps[i] }

// Computer returns the registered computer with the fingerprint.
func (x *Index) Computer(fp string) (Computer, bool) {
	i, ok := x.computers[fp]
	if !ok {
		return Computer{}, false
	}
	return x.State.Computers[i], true
}

// Clone returns a deep copy.
func (st *State) Clone() *State {
	return &State{Title: st.Title, Instruction: st.Instruction,
		Techies: slices.Clone(st.Techies), Computers: slices.Clone(st.Computers)}
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

// SetInfo sets the title and instruction line the apps show. Each is one
// line of printable text; empty clears it.
func (st *State) SetInfo(title, instruction string) error {
	t, err := cleanLine("title", title, MaxTitle)
	if err != nil {
		return err
	}
	in, err := cleanLine("instruction", instruction, MaxInstruction)
	if err != nil {
		return err
	}
	st.Title, st.Instruction = t, in
	return nil
}

// cleanLine trims s and checks it like a name, but allows it to be empty.
func cleanLine(what, s string, max int) (string, error) {
	if strings.TrimSpace(s) == "" {
		return "", nil
	}
	s, err := auth.CleanName(s, max)
	if err != nil {
		return "", fmt.Errorf("%s: %s", what, strings.TrimPrefix(err.Error(), "name "))
	}
	return s, nil
}

// AddTechie adds a techie. The key line is normalized.
func (st *State) AddTechie(name, keyLine string) error {
	name, err := auth.CleanName(name, MaxTechieName)
	if err != nil {
		return err
	}
	if strings.ContainsAny(name, " \t") {
		return errors.New("techie name must not contain spaces")
	}
	k, comment, err := auth.ParseKey(keyLine)
	if err != nil {
		return err
	}
	fp := auth.Fingerprint(k)
	for _, h := range st.Techies {
		if h.Name == name {
			return fmt.Errorf("techie %q already exists", name)
		}
		if sameKey(h.PublicKey, fp) {
			return fmt.Errorf("key %s is already the key of techie %q", fp, h.Name)
		}
	}
	for _, c := range st.Computers {
		if sameKey(c.PublicKey, fp) {
			return fmt.Errorf("key %s is registered as computer %q; a key can't be both", fp, c.Name)
		}
	}
	st.Techies = append(st.Techies, Techie{Name: name, PublicKey: auth.KeyLine(k, comment)})
	return nil
}

// RemoveTechie removes a techie by name.
func (st *State) RemoveTechie(name string) error {
	i := slices.IndexFunc(st.Techies, func(h Techie) bool { return h.Name == name })
	if i < 0 {
		return fmt.Errorf("no techie %q", name)
	}
	st.Techies = slices.Delete(st.Techies, i, i+1)
	return nil
}

// AddComputer registers a computer key. name overrides the key comment and is
// required when the comment is empty. It returns the fingerprint and name.
func (st *State) AddComputer(keyLine, name, by string, at time.Time) (fp, regName string, err error) {
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
	if name, err = auth.CleanName(name, MaxComputerName); err != nil {
		return "", "", err
	}
	fp = auth.Fingerprint(k)
	for _, h := range st.Techies {
		if sameKey(h.PublicKey, fp) {
			return "", "", fmt.Errorf("key %s is the key of techie %q; a key can't be both", fp, h.Name)
		}
	}
	for _, c := range st.Computers {
		if sameKey(c.PublicKey, fp) {
			return "", "", fmt.Errorf("key %s is already registered as %q", fp, c.Name)
		}
	}
	st.Computers = append(st.Computers, Computer{
		PublicKey:    auth.KeyLine(k, name),
		Name:         name,
		RegisteredBy: by,
		RegisteredAt: at.UTC(),
	})
	return fp, name, nil
}

// RemoveComputer removes a computer by fingerprint and returns its name.
// RenameComputer sets the name of the computer with fingerprint fp, also as the
// comment of its stored key line. It returns the old name.
func (st *State) RenameComputer(fp, name string) (string, error) {
	i := slices.IndexFunc(st.Computers, func(c Computer) bool { return sameKey(c.PublicKey, fp) })
	if i < 0 {
		return "", fmt.Errorf("no computer with fingerprint %s", fp)
	}
	name, err := auth.CleanName(name, MaxComputerName)
	if err != nil {
		return "", err
	}
	k, _, err := auth.ParseKey(st.Computers[i].PublicKey)
	if err != nil {
		return "", err
	}
	old := st.Computers[i].Name
	st.Computers[i].Name = name
	st.Computers[i].PublicKey = auth.KeyLine(k, name)
	return old, nil
}

func (st *State) RemoveComputer(fp string) (string, error) {
	i := slices.IndexFunc(st.Computers, func(c Computer) bool { return sameKey(c.PublicKey, fp) })
	if i < 0 {
		return "", fmt.Errorf("no computer with fingerprint %s", fp)
	}
	name := st.Computers[i].Name
	st.Computers = slices.Delete(st.Computers, i, i+1)
	return name, nil
}

// SetLastSeen updates last-seen times of computers by fingerprint, keeping the later time.
func (st *State) SetLastSeen(seen map[string]time.Time) {
	for i, c := range st.Computers {
		k, _, err := auth.ParseKey(c.PublicKey)
		if err != nil {
			continue
		}
		if t, ok := seen[auth.Fingerprint(k)]; ok && t.After(c.LastSeen) {
			st.Computers[i].LastSeen = t.UTC()
		}
	}
}

func sameKey(line, fp string) bool {
	k, _, err := auth.ParseKey(line)
	return err == nil && auth.Fingerprint(k) == fp
}
