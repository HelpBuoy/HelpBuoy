// Package store keeps the server's persistent state, state.json in the state
// directory: who runs the server, the techie keys and the registered computer
// keys with the servers approved for each (PLAN.md 12), and the customers the
// computers are grouped in (PLAN.md 13).
//
// Writes go to a temporary file that is synced and then renamed over
// state.json, so readers always see a complete file. A lock file serializes
// writers across processes (the running server and the admin CLI); every
// update reads the file fresh under that lock, so neither side overwrites a
// change made by the other.
package store

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/proto/serveraddr"
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
)

// Techie is a techie key added by the server admin.
type Techie struct {
	Name      string `json:"name"`
	PublicKey string `json:"public_key"` // authorized_keys line
	// Limited techies only reach the computers of Customers, a list of
	// customer ids that may be empty (PLAN.md 13.3). They are stored in
	// limited_techies instead of techies, so an older server that doesn't
	// know about limits treats their keys as unknown instead of letting them
	// see every computer.
	Limited   bool     `json:"-"`
	Customers []string `json:"customers,omitempty"`
	Extra     Extra    `json:"-"`
}

// Customer is a group of computers on the server (PLAN.md 13).
type Customer struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Extra Extra  `json:"-"`
}

// Computer is a registered computer key.
type Computer struct {
	PublicKey    string    `json:"public_key"` // authorized_keys line
	Name         string    `json:"name"`       // user@machine
	RegisteredBy string    `json:"registered_by"`
	RegisteredAt time.Time `json:"registered_at"`
	LastSeen     time.Time `json:"last_seen,omitzero"`
	// Approved are the servers this server approved for the computer, and
	// Listings its open requests to approve one (PLAN.md 12.3).
	Approved []control.ApprovedServer `json:"approved,omitempty"`
	Listings []control.ListingRequest `json:"listings,omitempty"`
	// Customer is the id of the computer's customer, empty for none.
	Customer string `json:"customer,omitempty"`
	Extra    Extra  `json:"-"`
}

// State is the content of state.json.
type State struct {
	// Who runs the server, see control.ServerInfo.
	Address     string     `json:"address,omitempty"`
	Company     string     `json:"company,omitempty"`
	Phone       string     `json:"phone,omitempty"`
	Description string     `json:"description,omitempty"`
	Techies     []Techie   `json:"techies"`
	Computers   []Computer `json:"computers"`
	Customers   []Customer `json:"customers,omitempty"`
	// LimitedTechies is only used to read and write state.json: the limited
	// techies are in Techies with Limited set (see Techie).
	LimitedTechies []Techie `json:"limited_techies,omitempty"`
	Extra          Extra    `json:"-"`
}

// Identity is what a fingerprint maps to.
type Identity struct {
	Role    control.Role
	Name    string // techie name or computer name; empty for pending
	KeyLine string // authorized_keys line; empty for pending
	// Limited techies only reach the computers of these customer ids.
	Limited   bool
	Customers []string
}

// Index is a read-only lookup view of a State by key fingerprint. It is
// immutable once built and safe for concurrent use.
type Index struct {
	State     *State
	techies   map[string]int // fingerprint -> index in State.Techies
	computers map[string]int
	fps       []string       // fingerprint of State.Computers[i]
	hfps      []string       // fingerprint of State.Techies[i]
	customers map[string]int // customer id -> index in State.Customers
}

// NewIndex validates st and builds its index. st must not be changed afterwards.
func NewIndex(st *State) (*Index, error) {
	idx := &Index{State: st, techies: map[string]int{}, computers: map[string]int{}, customers: map[string]int{}}
	if err := checkInfo(st.Info(), false); err != nil {
		return nil, err
	}
	cnames := map[string]bool{}
	for i, cu := range st.Customers {
		if !validCustomerID(cu.ID) {
			return nil, fmt.Errorf("customer %q: bad id %q", cu.Name, cu.ID)
		}
		if _, dup := idx.customers[cu.ID]; dup {
			return nil, fmt.Errorf("customer id %q is used twice", cu.ID)
		}
		if _, err := auth.CleanName(cu.Name, control.MaxCustomerName); err != nil {
			return nil, fmt.Errorf("customer %q: %w", cu.Name, err)
		}
		if cnames[strings.ToLower(cu.Name)] {
			return nil, fmt.Errorf("customer %q is listed twice", cu.Name)
		}
		cnames[strings.ToLower(cu.Name)] = true
		idx.customers[cu.ID] = i
	}
	names := map[string]bool{}
	for i, h := range st.Techies {
		if _, err := auth.CleanName(h.Name, MaxTechieName); err != nil {
			return nil, fmt.Errorf("techie %q: %w", h.Name, err)
		}
		if names[h.Name] {
			return nil, fmt.Errorf("techie %q is listed twice", h.Name)
		}
		if !h.Limited && len(h.Customers) > 0 {
			return nil, fmt.Errorf("techie %q has customers but is not limited", h.Name)
		}
		for _, id := range h.Customers {
			if _, ok := idx.customers[id]; !ok {
				return nil, fmt.Errorf("techie %q: no customer with id %q", h.Name, id)
			}
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
		if _, ok := idx.customers[c.Customer]; c.Customer != "" && !ok {
			return nil, fmt.Errorf("computer %q: no customer with id %q", c.Name, c.Customer)
		}
		for _, a := range c.Approved {
			if err := checkServer(a.Address, a.Fingerprint); err != nil {
				return nil, fmt.Errorf("computer %q: approved server: %w", c.Name, err)
			}
		}
		for _, l := range c.Listings {
			if err := checkServer(l.Address, l.Fingerprint); err != nil {
				return nil, fmt.Errorf("computer %q: listing request: %w", c.Name, err)
			}
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
		return Identity{Role: control.RoleTechie, Name: h.Name, KeyLine: h.PublicKey,
			Limited: h.Limited, Customers: h.Customers}
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

// Customer returns the customer with the id.
func (x *Index) Customer(id string) (Customer, bool) {
	i, ok := x.customers[id]
	if !ok {
		return Customer{}, false
	}
	return x.State.Customers[i], true
}

// Clone returns a deep copy.
func (st *State) Clone() *State {
	// Extra maps are shared: they are never changed, only replaced.
	// The Approved and Listings slices of computers and the Customers slices
	// of techies are shared too: they are replaced, never changed in place.
	n := *st
	n.Techies, n.Computers = slices.Clone(st.Techies), slices.Clone(st.Computers)
	n.Customers = slices.Clone(st.Customers)
	return &n
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
		if err := json.Unmarshal(b, st); err != nil {
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

// Info returns who runs the server, as the apps get it.
func (st *State) Info() control.ServerInfo {
	return control.ServerInfo{Address: st.Address, Company: st.Company, Phone: st.Phone, Description: st.Description}
}

// SetInfo sets who runs the server. Each field is one line of printable
// text; the company name and phone number are required, the address must be
// a server address or empty, and the description may be empty.
func (st *State) SetInfo(info control.ServerInfo) error {
	var err error
	if info.Address, err = cleanLine("address", info.Address, control.MaxAddress); err != nil {
		return err
	}
	if info.Address != "" {
		a, err := serveraddr.Parse(info.Address)
		if err != nil {
			return fmt.Errorf("address: %w", err)
		}
		info.Address = a.String()
	}
	if info.Company, err = cleanLine("company name", info.Company, control.MaxCompany); err != nil {
		return err
	}
	if info.Phone, err = cleanLine("phone number", info.Phone, control.MaxPhone); err != nil {
		return err
	}
	if info.Description, err = cleanLine("description", info.Description, control.MaxDescription); err != nil {
		return err
	}
	if err := checkInfo(info, true); err != nil {
		return err
	}
	st.Address, st.Company, st.Phone, st.Description = info.Address, info.Company, info.Phone, info.Description
	return nil
}

// checkInfo checks stored server info. With required, the company name and
// phone number must be set.
func checkInfo(info control.ServerInfo, required bool) error {
	for _, f := range []struct {
		what, v string
		max     int
	}{
		{"address", info.Address, control.MaxAddress},
		{"company name", info.Company, control.MaxCompany},
		{"phone number", info.Phone, control.MaxPhone},
		{"description", info.Description, control.MaxDescription},
	} {
		if _, err := cleanLine(f.what, f.v, f.max); err != nil {
			return err
		}
	}
	switch {
	case required && info.Company == "":
		return errors.New("the company name is required")
	case required && info.Phone == "":
		return errors.New("the phone number is required")
	}
	return nil
}

// checkServer checks a server address and host key fingerprint of an
// approved server or a listing request.
func checkServer(addr, fp string) error {
	a, err := serveraddr.Parse(addr)
	if err != nil {
		return err
	}
	if a.String() != addr {
		return fmt.Errorf("address %q is not normalized", addr)
	}
	if !control.ValidFingerprint(fp) {
		return fmt.Errorf("bad fingerprint %q", fp)
	}
	return nil
}

// Listing outcomes of RequestListing.
type Listing int

const (
	ListingAdded    Listing = iota // a new open request
	ListingOpen                    // the same request was already open
	ListingApproved                // the server is already approved
)

// RequestListing adds an open request of computer fp to approve the server
// at addr with host key fingerprint serverFP.
func (st *State) RequestListing(fp, addr, serverFP string, at time.Time) (Listing, error) {
	a, err := serveraddr.Parse(addr)
	if err != nil {
		return 0, err
	}
	addr = a.String()
	if err := checkServer(addr, serverFP); err != nil {
		return 0, err
	}
	i := slices.IndexFunc(st.Computers, func(c Computer) bool { return sameKey(c.PublicKey, fp) })
	if i < 0 {
		return 0, fmt.Errorf("no computer with fingerprint %s", fp)
	}
	c := &st.Computers[i]
	if slices.Contains(c.Approved, control.ApprovedServer{Address: addr, Fingerprint: serverFP}) {
		return ListingApproved, nil
	}
	if slices.ContainsFunc(c.Listings, func(l control.ListingRequest) bool {
		return l.Address == addr && l.Fingerprint == serverFP
	}) {
		return ListingOpen, nil
	}
	if len(c.Listings) >= control.MaxListingRequests {
		return 0, fmt.Errorf("too many open listing requests (max %d)", control.MaxListingRequests)
	}
	c.Listings = append(slices.Clone(c.Listings), control.ListingRequest{Address: addr, Fingerprint: serverFP, RequestedAt: at.UTC()})
	return ListingAdded, nil
}

// DecideListing closes the open request of computer fp for the server at
// addr with fingerprint serverFP. Allowing puts the server on the
// computer's approved list, in place of an older approval for the same
// address. It returns the computer's name.
func (st *State) DecideListing(fp, addr, serverFP string, allow bool) (string, error) {
	i := slices.IndexFunc(st.Computers, func(c Computer) bool { return sameKey(c.PublicKey, fp) })
	if i < 0 {
		return "", fmt.Errorf("no computer with fingerprint %s", fp)
	}
	c := &st.Computers[i]
	j := slices.IndexFunc(c.Listings, func(l control.ListingRequest) bool {
		return l.Address == addr && l.Fingerprint == serverFP
	})
	if j < 0 {
		return "", fmt.Errorf("no open request for %s (%s)", addr, serverFP)
	}
	c.Listings = slices.Delete(slices.Clone(c.Listings), j, j+1)
	if allow {
		approved := slices.DeleteFunc(slices.Clone(c.Approved), func(a control.ApprovedServer) bool { return a.Address == addr })
		if len(approved) >= control.MaxApprovedServers {
			return "", fmt.Errorf("too many approved servers (max %d)", control.MaxApprovedServers)
		}
		c.Approved = append(approved, control.ApprovedServer{Address: addr, Fingerprint: serverFP})
	}
	return c.Name, nil
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

// validCustomerID accepts customer ids as newCustomerID makes them, and any
// other short lower-case id a newer version might use.
func validCustomerID(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for _, ch := range id {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'z') && ch != '-' {
			return false
		}
	}
	return true
}

// newCustomerID returns a random customer id: 16 lower-case hex digits.
func newCustomerID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// FindCustomer returns the index of the customer with id or name ref
// (names match without regard to case).
func (st *State) FindCustomer(ref string) (int, error) {
	ref = strings.TrimSpace(ref)
	if i := slices.IndexFunc(st.Customers, func(c Customer) bool { return c.ID == ref }); i >= 0 {
		return i, nil
	}
	if i := slices.IndexFunc(st.Customers, func(c Customer) bool { return strings.EqualFold(c.Name, ref) }); i >= 0 {
		return i, nil
	}
	return -1, fmt.Errorf("no customer %q", ref)
}

// cleanCustomerName checks a new customer name and that no other customer
// than the one at index self has it.
func (st *State) cleanCustomerName(name string, self int) (string, error) {
	name, err := auth.CleanName(name, control.MaxCustomerName)
	if err != nil {
		return "", err
	}
	for i, c := range st.Customers {
		if i != self && strings.EqualFold(c.Name, name) {
			return "", fmt.Errorf("customer %q already exists", c.Name)
		}
	}
	return name, nil
}

// AddCustomer adds a customer and returns its id.
func (st *State) AddCustomer(name string) (string, error) {
	name, err := st.cleanCustomerName(name, -1)
	if err != nil {
		return "", err
	}
	id := newCustomerID()
	st.Customers = append(st.Customers, Customer{ID: id, Name: name})
	return id, nil
}

// RenameCustomer renames the customer with id or name ref and returns its
// old name.
func (st *State) RenameCustomer(ref, name string) (string, error) {
	i, err := st.FindCustomer(ref)
	if err != nil {
		return "", err
	}
	if name, err = st.cleanCustomerName(name, i); err != nil {
		return "", err
	}
	old := st.Customers[i].Name
	st.Customers[i].Name = name
	return old, nil
}

// RemoveCustomer removes the customer with id or name ref and returns its
// name. Its computers move to no customer, and limited techies lose it.
func (st *State) RemoveCustomer(ref string) (string, error) {
	i, err := st.FindCustomer(ref)
	if err != nil {
		return "", err
	}
	cu := st.Customers[i]
	st.Customers = slices.Delete(st.Customers, i, i+1)
	for j := range st.Computers {
		if st.Computers[j].Customer == cu.ID {
			st.Computers[j].Customer = ""
		}
	}
	for j, h := range st.Techies {
		if slices.Contains(h.Customers, cu.ID) {
			st.Techies[j].Customers = slices.DeleteFunc(slices.Clone(h.Customers), func(id string) bool { return id == cu.ID })
		}
	}
	return cu.Name, nil
}

// MoveComputer puts the computer with fingerprint fp in the customer with id
// or name ref, or in no customer when ref is empty. It returns the
// computer's name.
func (st *State) MoveComputer(fp, ref string) (string, error) {
	i := slices.IndexFunc(st.Computers, func(c Computer) bool { return sameKey(c.PublicKey, fp) })
	if i < 0 {
		return "", fmt.Errorf("no computer with fingerprint %s", fp)
	}
	id := ""
	if strings.TrimSpace(ref) != "" {
		j, err := st.FindCustomer(ref)
		if err != nil {
			return "", err
		}
		id = st.Customers[j].ID
	}
	st.Computers[i].Customer = id
	return st.Computers[i].Name, nil
}

// LimitTechie limits the techie name to the customers with ids or names
// refs, or lifts the limit when limited is false.
func (st *State) LimitTechie(name string, limited bool, refs []string) error {
	i := slices.IndexFunc(st.Techies, func(h Techie) bool { return h.Name == name })
	if i < 0 {
		return fmt.Errorf("no techie %q", name)
	}
	if !limited {
		if len(refs) > 0 {
			return errors.New("customers given for a techie that is not limited")
		}
		st.Techies[i].Limited, st.Techies[i].Customers = false, nil
		return nil
	}
	ids := []string{}
	for _, ref := range refs {
		j, err := st.FindCustomer(ref)
		if err != nil {
			return err
		}
		if !slices.Contains(ids, st.Customers[j].ID) {
			ids = append(ids, st.Customers[j].ID)
		}
	}
	st.Techies[i].Limited, st.Techies[i].Customers = true, ids
	return nil
}

// CustomerNames returns the names of customer ids, for messages and lists.
func (st *State) CustomerNames(ids []string) []string {
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		if i := slices.IndexFunc(st.Customers, func(c Customer) bool { return c.ID == id }); i >= 0 {
			names = append(names, st.Customers[i].Name)
		}
	}
	return names
}
