// Package registry is the server's in-memory state machine: online
// connections and their roles, help requests and sessions, and the Presence
// snapshot for techies. It knows nothing about SSH; connections are Peers
// that queue control messages. The server package feeds it decoded control
// messages and opens the session channels it asks for.
//
// All methods are safe for concurrent use. Peer methods are called with the
// registry lock held and must not block.
package registry

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/server/auth"
	"github.com/helpbuoy/helpbuoy/server/splice"
	"github.com/helpbuoy/helpbuoy/server/store"
)

// Peer is one SSH connection as the registry sees it.
type Peer interface {
	// Send queues a control message for the connection's control channel.
	Send(m any)
	// NotifyPresence tells a techie connection that the Presence snapshot
	// changed; the connection fetches it with Registry.Presence.
	NotifyPresence()
	// Kick sends an Error with reason and closes the connection.
	Kick(reason string)
}

// Config configures a Registry. Zero durations and counts take the defaults.
type Config struct {
	Now                  func() time.Time
	SessionStartTimeout  time.Duration // a taken request must get its session channel within this (1 min)
	PendingIdle          time.Duration // pending connections are closed after this (24 h)
	MaxSessionsPerTechie int           // 20
	Version              string        // server version for Hello
	// Mutate persists a change to the stored state. The implementation must
	// call SetIndex with the new state before it returns.
	Mutate func(fn func(*store.State) error) error
	Log    *slog.Logger
}

// Protocol errors returned by Handle; the caller closes the connection.
var (
	ErrNotAllowed = errors.New("message not allowed for this role")
	// ErrKicked is returned by OpenControl when it kicked the connection;
	// the Kick closes it once the Error is sent.
	ErrKicked = errors.New("connection kicked")
)

// Conn is an attached connection.
type Conn struct {
	peer        Peer
	fp          string
	ip          string
	role        control.Role
	name        string
	keyLine     string
	userMachine string
	connectedAt time.Time
	// limited techies only reach the computers of customers (PLAN.md 13.3).
	limited   bool
	customers []string

	control  bool // Hello has been queued; messages may be sent
	detached bool
	release  func() // pending limit slot, nil for known keys
	req      *request
}

// Fingerprint returns the connection's key fingerprint.
func (c *Conn) Fingerprint() string { return c.fp }

type request struct {
	id       string
	computer *Conn
	created  time.Time
	state    control.RequestState
	techie   *Conn
	takenAt  time.Time
	sess     *Session
}

// Session is a session being set up or running for a taken request.
type Session struct {
	r       *Registry
	req     *request
	closeFn func()
	started bool
	ended   bool
	start   time.Time
}

// Registry holds the live state.
type Registry struct {
	cfg Config
	log *slog.Logger

	mu       sync.Mutex
	idx      *store.Index
	conns    map[string]*Conn // fingerprint -> current connection
	requests map[string]*request
	lastSeen map[string]time.Time

	gen      uint64
	cacheGen uint64
	cache    map[string][]byte // encoded Presence per view, see viewKey
}

// New returns a registry over the stored state idx.
func New(cfg Config, idx *store.Index) *Registry {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.SessionStartTimeout == 0 {
		cfg.SessionStartTimeout = time.Minute
	}
	if cfg.PendingIdle == 0 {
		cfg.PendingIdle = 24 * time.Hour
	}
	if cfg.MaxSessionsPerTechie == 0 {
		cfg.MaxSessionsPerTechie = 20
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	return &Registry{
		cfg:      cfg,
		log:      cfg.Log,
		idx:      idx,
		conns:    map[string]*Conn{},
		requests: map[string]*request{},
		lastSeen: map[string]time.Time{},
		gen:      1,
	}
}

// Lookup returns the identity of a fingerprint in the current state.
func (r *Registry) Lookup(fp string) store.Identity {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.idx.Lookup(fp)
}

// Attach registers a new authenticated connection for key fp. The role comes
// from the current state. For an unknown key, admit is called to apply the
// pending limits; its release func is called when the connection ends or is
// promoted. A connection with the same key replaces the older one, which is
// kicked.
func (r *Registry) Attach(p Peer, fp, ip string, admit func() (func(), error)) (*Conn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := r.idx.Lookup(fp)
	c := &Conn{peer: p, fp: fp, ip: ip, role: id.Role, name: id.Name,
		keyLine: id.KeyLine, connectedAt: r.cfg.Now(), limited: id.Limited, customers: id.Customers}
	if id.Role == control.RolePending {
		rel, err := admit()
		if err != nil {
			return nil, err
		}
		c.release = rel
	}
	if old := r.conns[fp]; old != nil {
		r.detachLocked(old)
		old.peer.Kick(control.ReasonReplaced)
		r.log.Info("conn replaced", "fp", fp, "role", string(old.role), "old_ip", old.ip, "ip", ip)
	}
	r.conns[fp] = c
	if c.role == control.RoleComputer {
		r.lastSeen[fp] = c.connectedAt
	}
	r.changedLocked()
	return c, nil
}

// Role returns the connection's current role.
func (r *Registry) Role(c *Conn) control.Role {
	r.mu.Lock()
	defer r.mu.Unlock()
	return c.role
}

// Detach removes a closed connection. It ends its request and the sessions
// it takes part in. Calling it more than once is harmless.
func (r *Registry) Detach(c *Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.detachLocked(c)
}

func (r *Registry) detachLocked(c *Conn) {
	if c.detached {
		return
	}
	c.detached = true
	if r.conns[c.fp] == c {
		delete(r.conns, c.fp)
	}
	if c.release != nil {
		c.release()
		c.release = nil
	}
	now := r.cfg.Now()
	switch c.role {
	case control.RoleComputer:
		r.lastSeen[c.fp] = now
		if c.req != nil {
			r.endRequestLocked(c.req, control.RequestCancelled, false)
		}
	case control.RoleTechie:
		for _, req := range r.requests {
			if req.techie == c {
				r.endRequestLocked(req, control.RequestEnded, true)
			}
		}
	}
	r.changedLocked()
}

// OpenControl is called when the connection's control channel is open. It
// queues Hello, ServerInfo and, for techies, the first Presence.
func (r *Registry) OpenControl(c *Conn) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.detached {
		return errors.New("connection is closed")
	}
	if c.control {
		return errors.New("control channel already open")
	}
	c.control = true
	// Pending keys get in: a techie not added yet needs to connect to see
	// the key line to send, and a pending key only becomes a computer below.
	if c.role == control.RoleComputer && !r.idx.State.Info().Configured() {
		r.log.Info("computer refused: no company name and phone number set", "fp", c.fp, "ip", c.ip)
		r.detachLocked(c)
		c.peer.Kick(control.ReasonNotSetUp)
		return ErrKicked
	}
	h := &control.Hello{
		Role:          c.role,
		ServerVersion: r.cfg.Version,
		ServerTime:    r.cfg.Now().UTC(),
	}
	if c.role == control.RoleTechie {
		h.Name = c.name // computers are not told the name techies gave them
	}
	c.peer.Send(h)
	c.peer.Send(r.serverInfoLocked())
	switch c.role {
	case control.RoleTechie:
		c.peer.NotifyPresence()
	case control.RoleComputer:
		c.peer.Send(r.approvedLocked(c.fp))
	}
	return nil
}

// approvedLocked returns the ApprovedServers message for computer fp.
func (r *Registry) approvedLocked(fp string) *control.ApprovedServers {
	m := &control.ApprovedServers{Servers: []control.ApprovedServer{}}
	if sc, ok := r.idx.Computer(fp); ok {
		m.Servers = append(m.Servers, sc.Approved...)
	}
	return m
}

// send queues m if the connection can receive messages.
func send(c *Conn, m any) {
	if c.control && !c.detached {
		c.peer.Send(m)
	}
}

func (r *Registry) serverInfoLocked() *control.ServerInfo {
	info := r.idx.State.Info()
	return &info
}

// changedLocked marks the Presence snapshot stale and tells the techies.
func (r *Registry) changedLocked() {
	r.gen++
	for _, c := range r.conns {
		if c.role == control.RoleTechie && c.control {
			c.peer.NotifyPresence()
		}
	}
}

// view is what one techie may see: everything, or only some customers.
type view struct {
	limited   bool
	customers []string
}

// key identifies the view in the Presence cache.
func (v view) key() string {
	if !v.limited {
		return ""
	}
	ids := slices.Clone(v.customers)
	slices.Sort(ids)
	return "limited:" + strings.Join(ids, ",")
}

// has reports whether the view includes the customer with id cu ("" for
// computers without a customer).
func (v view) has(cu string) bool {
	return !v.limited || cu != "" && slices.Contains(v.customers, cu)
}

// shares reports whether the view includes a techie limited to customers,
// or not limited: one that shares a customer with it.
func (v view) shares(limited bool, customers []string) bool {
	if !v.limited || !limited {
		return true
	}
	return slices.ContainsFunc(customers, func(id string) bool { return slices.Contains(v.customers, id) })
}

func viewOf(c *Conn) view { return view{limited: c.limited, customers: c.customers} }

// Presence returns the encoded current Presence snapshot for techie c and
// its generation. Callers that already sent this generation can skip it.
func (r *Registry) Presence(c *Conn) ([]byte, uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache == nil || r.cacheGen != r.gen {
		r.cache, r.cacheGen = map[string][]byte{}, r.gen
	}
	v := viewOf(c)
	k := v.key()
	b, ok := r.cache[k]
	if !ok {
		var err error
		b, err = json.Marshal(r.presenceLocked(v))
		if err != nil {
			panic(err) // only plain data, can't fail
		}
		r.cache[k] = b
	}
	return b, r.cacheGen
}

// PresenceSnapshot returns the current Presence, with everything in it, as a
// value.
func (r *Registry) PresenceSnapshot() control.Presence {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.presenceLocked(view{})
}

func (r *Registry) presenceLocked(v view) control.Presence {
	now := r.cfg.Now().UTC()
	st := r.idx.State
	p := control.Presence{Computers: []control.ComputerInfo{}, Techies: []control.TechieInfo{}}
	for _, cu := range st.Customers {
		if !v.limited || slices.Contains(v.customers, cu.ID) {
			p.Customers = append(p.Customers, control.CustomerInfo{ID: cu.ID, Name: cu.Name})
		}
	}
	for i, sc := range st.Computers {
		if !v.has(sc.Customer) {
			continue
		}
		fp := r.idx.ComputerFingerprint(i)
		ci := control.ComputerInfo{
			Fingerprint:  fp,
			Name:         sc.Name,
			RegisteredBy: sc.RegisteredBy,
			RegisteredAt: sc.RegisteredAt,
			LastSeen:     sc.LastSeen,
			Approved:     slices.Clone(sc.Approved),
			Listings:     slices.Clone(sc.Listings),
			Customer:     sc.Customer,
		}
		if t, ok := r.lastSeen[fp]; ok && t.After(ci.LastSeen) {
			ci.LastSeen = t.UTC()
		}
		if c := r.conns[fp]; c != nil && c.role == control.RoleComputer {
			ci.Online = true
			ci.LastSeen = now
			ci.UserMachine = c.userMachine
			if q := c.req; q != nil {
				ri := &control.RequestInfo{ID: q.id, Created: q.created.UTC(), State: q.state}
				if q.techie != nil {
					ri.TakenBy = q.techie.name
					if q.sess != nil && q.sess.started {
						ci.InSessionWith = q.techie.name
					}
				}
				ci.Request = ri
			}
		}
		p.Computers = append(p.Computers, ci)
	}
	for i, h := range st.Techies {
		if !v.shares(h.Limited, h.Customers) {
			continue
		}
		fp := r.idx.TechieFingerprint(i)
		c := r.conns[fp]
		ti := control.TechieInfo{
			Name: h.Name, Fingerprint: fp,
			Online:  c != nil && c.role == control.RoleTechie,
			Limited: h.Limited,
		}
		if h.Limited {
			// A limited viewer only learns the customers it shares.
			for _, id := range h.Customers {
				if !v.limited || slices.Contains(v.customers, id) {
					ti.Customers = append(ti.Customers, id)
				}
			}
		}
		p.Techies = append(p.Techies, ti)
	}
	slices.SortFunc(p.Computers, func(a, b control.ComputerInfo) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Fingerprint, b.Fingerprint))
	})
	slices.SortFunc(p.Techies, func(a, b control.TechieInfo) int { return cmp.Compare(a.Name, b.Name) })
	return p
}

// SetIndex installs a new stored state. Connections whose key was removed
// or changed role are kicked; pending connections whose key is now a
// registered computer are promoted and told so with Registered.
func (r *Registry) SetIndex(idx *store.Index) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.idx.State
	oldIdx := r.idx
	r.idx = idx
	if old.Info() != idx.State.Info() {
		info := r.serverInfoLocked()
		for _, c := range r.conns {
			send(c, info)
		}
	}
	for fp, c := range r.conns {
		id := idx.Lookup(fp)
		switch {
		case id.Role == c.role:
			c.name, c.keyLine = id.Name, id.KeyLine
			c.limited, c.customers = id.Limited, id.Customers
			if c.role == control.RoleComputer {
				was, _ := oldIdx.Computer(fp)
				now, _ := idx.Computer(fp)
				if !slices.Equal(was.Approved, now.Approved) {
					send(c, r.approvedLocked(fp))
				}
			}
		case c.role == control.RolePending && id.Role == control.RoleComputer && !idx.State.Info().Configured():
			r.log.Info("computer refused: no company name and phone number set", "fp", fp, "ip", c.ip)
			r.detachLocked(c)
			c.peer.Kick(control.ReasonNotSetUp)
		case c.role == control.RolePending && id.Role == control.RoleComputer:
			c.role = control.RoleComputer
			c.name, c.keyLine = id.Name, id.KeyLine
			if c.release != nil {
				c.release()
				c.release = nil
			}
			r.lastSeen[fp] = r.cfg.Now()
			send(c, &control.Registered{})
			send(c, r.approvedLocked(fp))
			r.log.Info("pending promoted", "fp", fp, "name", id.Name, "ip", c.ip)
		default:
			reason := "key removed from the server"
			if id.Role != control.RolePending {
				reason = "key changed role"
			}
			r.log.Info("conn revoked", "fp", fp, "role", string(c.role), "new_role", string(id.Role), "ip", c.ip)
			r.detachLocked(c)
			c.peer.Kick(reason)
		}
	}
	// A techie whose limit changed, or a computer that moved to another
	// customer, ends the sessions the techie may no longer have.
	for _, q := range r.requests {
		if q.techie != nil && !r.allowedLocked(q.techie, q.computer.fp) {
			r.log.Info("request", "event", "not allowed anymore", "id", q.id, "computer", q.computer.fp, "techie", q.techie.name)
			r.endRequestLocked(q, control.RequestEnded, true)
		}
	}
	r.changedLocked()
}

// allowedLocked reports whether techie c may reach the computer with
// fingerprint fp: always when it isn't limited, otherwise only when the
// computer belongs to one of its customers.
func (r *Registry) allowedLocked(c *Conn, fp string) bool {
	if !c.limited {
		return true
	}
	sc, ok := r.idx.Computer(fp)
	return ok && viewOf(c).has(sc.Customer)
}

// errLimited is the Reply error for a command a limited techie may not use.
var errLimited = errors.New("not allowed for a techie limited to some customers")

// errNoComputer is the Reply error for a computer a limited techie may not
// reach, the same as for one that doesn't exist.
func errNoComputer(fp string) error { return fmt.Errorf("no computer with fingerprint %s", fp) }

// Handle processes one control message from c. A returned error is a
// protocol violation; the caller sends it as Error and closes the connection.
func (r *Registry) Handle(c *Conn, m any) error {
	switch m := m.(type) {
	case *control.ComputerHello:
		return r.computerHello(c, m)
	case *control.CancelRequest:
		return r.cancelRequest(c, m)
	case *control.Connect:
		return r.connect(c, m)
	case *control.RegisterComputer:
		return r.registerComputer(c, m)
	case *control.UnregisterComputer:
		return r.unregisterComputer(c, m)
	case *control.RegisterTechie:
		return r.registerTechie(c, m)
	case *control.UnregisterTechie:
		return r.unregisterTechie(c, m)
	case *control.RenameComputer:
		return r.renameComputer(c, m)
	case *control.SetServerInfo:
		return r.setServerInfo(c, m)
	case *control.RequestListing:
		return r.requestListing(c, m)
	case *control.DecideListing:
		return r.decideListing(c, m)
	case *control.AddCustomer:
		return r.addCustomer(c, m)
	case *control.RenameCustomer:
		return r.renameCustomer(c, m)
	case *control.RemoveCustomer:
		return r.removeCustomer(c, m)
	case *control.MoveComputer:
		return r.moveComputer(c, m)
	case *control.LimitTechie:
		return r.limitTechie(c, m)
	}
	return fmt.Errorf("%w: %T", ErrNotAllowed, m)
}

func (r *Registry) computerHello(c *Conn, m *control.ComputerHello) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.role == control.RoleTechie {
		return nil // techies have no ComputerHello; ignored per the contract
	}
	um := auth.Truncate(m.UserMachine, 256)
	if um != c.userMachine {
		c.userMachine = um
		if c.role == control.RoleComputer {
			r.changedLocked()
		}
	}
	return nil
}

func (r *Registry) cancelRequest(c *Conn, m *control.CancelRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch c.role {
	case control.RoleComputer:
		// This also closes the session channels if they are being opened or open.
		if q := c.req; q != nil && q.id == m.RequestID {
			r.endRequestLocked(q, control.RequestCancelled, true)
		}
	case control.RoleTechie:
		// The techie who connected gives up waiting for the user; the
		// computer is told so it closes its question.
		if q := r.requests[m.RequestID]; q != nil && q.techie == c {
			r.endRequestLocked(q, control.RequestCancelled, true)
		}
	default:
		return fmt.Errorf("%w: CancelRequest from %s", ErrNotAllowed, c.role)
	}
	return nil
}

// endRequestLocked finishes a request: it leaves the computer, the computer is
// told (if notify), and a session for it is closed.
func (r *Registry) endRequestLocked(q *request, state control.RequestState, notify bool) {
	if r.requests[q.id] != q {
		return
	}
	delete(r.requests, q.id)
	q.state = state
	if q.computer.req == q {
		q.computer.req = nil
	}
	if notify {
		send(q.computer, &control.RequestStateMsg{RequestID: q.id, State: state})
	}
	if s := q.sess; s != nil && !s.ended {
		s.ended = true
		if s.closeFn != nil {
			go s.closeFn()
		}
	}
	attrs := []any{"event", string(state), "id", q.id, "computer", q.computer.fp}
	if q.techie != nil {
		attrs = append(attrs, "techie", q.techie.name)
	}
	r.log.Info("request", attrs...)
	r.changedLocked()
}

// sessionsLocked counts the requests a techie took: the sessions that run
// and the ones waiting for the computer's user.
func (r *Registry) sessionsLocked() int {
	n := 0
	for _, q := range r.requests {
		if q.techie != nil {
			n++
		}
	}
	return n
}

func (r *Registry) techiesessionsLocked(h *Conn) int {
	n := 0
	for _, q := range r.requests {
		if q.techie == h {
			n++
		}
	}
	return n
}

// connect starts a session from techie c: a request, taken by c, that waits
// for the computer's user to allow it.
func (r *Registry) connect(c *Conn, m *control.Connect) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.role != control.RoleTechie {
		return fmt.Errorf("%w: Connect from %s", ErrNotAllowed, c.role)
	}
	fail := func(msg string) error {
		send(c, &control.Reply{Seq: m.Seq, Error: msg})
		return nil
	}
	if !control.ValidRequestID(m.RequestID) || r.requests[m.RequestID] != nil {
		return fail("bad request id")
	}
	cl := r.conns[m.Fingerprint]
	switch {
	case cl == nil || cl.role != control.RoleComputer || cl.detached || !r.allowedLocked(c, m.Fingerprint):
		return fail("computer is not online")
	case cl.req != nil:
		return fail("computer is busy")
	case r.techiesessionsLocked(c) >= r.cfg.MaxSessionsPerTechie:
		return fail(fmt.Sprintf("too many sessions (max %d)", r.cfg.MaxSessionsPerTechie))
	case r.idx.State.MaxSessions > 0 && r.sessionsLocked() >= r.idx.State.MaxSessions:
		r.log.Info("request", "event", "refused", "computer", cl.fp, "techie", c.name, "reason", "max sessions")
		if r.idx.State.MaxSessions == 1 {
			return fail("this server allows one session at a time; wait until the other session ends")
		}
		return fail(fmt.Sprintf("this server allows %d sessions at the same time; wait until another session ends", r.idx.State.MaxSessions))
	}
	now := r.cfg.Now()
	q := &request{id: m.RequestID, computer: cl, created: now, state: control.RequestTaken, techie: c, takenAt: now}
	r.requests[q.id] = q
	cl.req = q
	send(c, &control.Reply{Seq: m.Seq})
	send(cl, &control.SessionReady{
		RequestID:  q.id,
		TechieName: c.name,
		TechieKey:  c.keyLine,
	})
	r.log.Info("request", "event", "connect", "id", q.id, "computer", cl.fp, "techie", c.name)
	r.changedLocked()
	return nil
}

// techieName returns c's techie name, or an error if c is not a techie.
func (r *Registry) techieName(c *Conn, what string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.role != control.RoleTechie {
		return "", fmt.Errorf("%w: %s from %s", ErrNotAllowed, what, c.role)
	}
	return c.name, nil
}

// denied returns the Reply error for a command of techie c about the
// computer with fingerprint fp that c may not reach, or nil.
func (r *Registry) denied(c *Conn, fp string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.allowedLocked(c, fp) {
		return errNoComputer(fp)
	}
	return nil
}

// deniedLimited returns the Reply error for a command that affects every
// customer when techie c is limited, or nil.
func (r *Registry) deniedLimited(c *Conn) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.limited {
		return errLimited
	}
	return nil
}

func (r *Registry) reply(c *Conn, seq uint32, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rep := &control.Reply{Seq: seq}
	if err != nil {
		rep.Error = err.Error()
	}
	send(c, rep)
}

func (r *Registry) mutate(fn func(*store.State) error) error {
	if r.cfg.Mutate == nil {
		return errors.New("state is read-only")
	}
	return r.cfg.Mutate(fn)
}

// registerTechie adds a techie key on behalf of techie c.
func (r *Registry) registerTechie(c *Conn, m *control.RegisterTechie) error {
	by, err := r.techieName(c, "RegisterTechie")
	if err != nil {
		return err
	}
	err = r.deniedLimited(c)
	if err == nil {
		err = r.mutate(func(st *store.State) error {
			if err := st.AddTechie(m.Name, m.PublicKey); err != nil {
				return err
			}
			if !m.Limited && len(m.Customers) == 0 {
				return nil
			}
			return st.LimitTechie(strings.TrimSpace(m.Name), m.Limited, m.Customers)
		})
	}
	if err == nil {
		r.log.Info("techie added", "name", strings.TrimSpace(m.Name), "limited", m.Limited, "customers", m.Customers, "by", by)
	}
	r.reply(c, m.Seq, err)
	return nil
}

// unregisterTechie removes another techie on behalf of techie c; the removed
// techie is disconnected when the new state is installed.
func (r *Registry) unregisterTechie(c *Conn, m *control.UnregisterTechie) error {
	by, err := r.techieName(c, "UnregisterTechie")
	if err != nil {
		return err
	}
	err = r.deniedLimited(c)
	if err == nil && m.Name == by {
		err = errors.New("you cannot remove yourself")
	}
	if err == nil {
		err = r.mutate(func(st *store.State) error { return st.RemoveTechie(m.Name) })
	}
	if err == nil {
		r.log.Info("techie removed", "name", m.Name, "by", by)
	}
	r.reply(c, m.Seq, err)
	return nil
}

func (r *Registry) registerComputer(c *Conn, m *control.RegisterComputer) error {
	by, err := r.techieName(c, "RegisterComputer")
	if err != nil {
		return err
	}
	r.mu.Lock()
	v := viewOf(c)
	r.mu.Unlock()
	var fp, name string
	err = r.mutate(func(st *store.State) error {
		var err error
		fp, name, err = st.AddComputer(m.PublicKey, m.Name, by, r.cfg.Now())
		if err != nil {
			return err
		}
		if m.Customer == "" && !v.limited {
			return nil
		}
		if !v.has(m.Customer) {
			if m.Customer == "" {
				return errors.New("choose one of your customers for the computer")
			}
			return fmt.Errorf("no customer %q", m.Customer)
		}
		_, err = st.MoveComputer(fp, m.Customer)
		return err
	})
	if err == nil {
		r.log.Info("computer registered", "fp", fp, "name", name, "customer", m.Customer, "by", by)
	}
	r.reply(c, m.Seq, err)
	return nil
}

func (r *Registry) renameComputer(c *Conn, m *control.RenameComputer) error {
	by, err := r.techieName(c, "RenameComputer")
	if err != nil {
		return err
	}
	err = r.denied(c, m.Fingerprint)
	var old string
	if err == nil {
		err = r.mutateFor(c, m.Fingerprint, func(st *store.State) error {
			var err error
			old, err = st.RenameComputer(m.Fingerprint, m.Name)
			return err
		})
	}
	if err == nil {
		r.log.Info("computer renamed", "fp", m.Fingerprint, "from", old, "to", strings.TrimSpace(m.Name), "by", by)
	}
	r.reply(c, m.Seq, err)
	return nil
}

func (r *Registry) setServerInfo(c *Conn, m *control.SetServerInfo) error {
	by, err := r.techieName(c, "SetServerInfo")
	if err != nil {
		return err
	}
	err = r.deniedLimited(c)
	info := control.ServerInfo{Address: m.Address, Company: m.Company, Phone: m.Phone, Description: m.Description}
	if err == nil {
		err = r.mutate(func(st *store.State) error { return st.SetInfo(info) })
	}
	if err == nil {
		r.log.Info("server info set", "address", strings.TrimSpace(m.Address), "company", strings.TrimSpace(m.Company),
			"phone", strings.TrimSpace(m.Phone), "description", strings.TrimSpace(m.Description), "by", by)
	}
	r.reply(c, m.Seq, err)
	return nil
}

// requestListing stores a locked computer's request to approve another
// server (PLAN.md 12.3). A server that is already approved gets the
// computer its list again, so it goes on.
func (r *Registry) requestListing(c *Conn, m *control.RequestListing) error {
	r.mu.Lock()
	role := c.role
	r.mu.Unlock()
	if role != control.RoleComputer {
		return fmt.Errorf("%w: RequestListing from %s", ErrNotAllowed, role)
	}
	var res store.Listing
	err := r.mutate(func(st *store.State) error {
		var err error
		res, err = st.RequestListing(c.fp, m.Address, m.Fingerprint, r.cfg.Now())
		return err
	})
	switch {
	case err != nil:
		r.log.Info("listing request refused", "fp", c.fp, "address", m.Address, "server", m.Fingerprint, "err", err)
	case res == store.ListingAdded:
		r.log.Info("listing requested", "fp", c.fp, "address", m.Address, "server", m.Fingerprint)
	case res == store.ListingApproved:
		r.mu.Lock()
		send(c, r.approvedLocked(c.fp))
		r.mu.Unlock()
	}
	return nil
}

// decideListing allows or refuses a computer's listing request on behalf of
// techie c. The computer gets its new approved list, or ListingRefused.
func (r *Registry) decideListing(c *Conn, m *control.DecideListing) error {
	by, err := r.techieName(c, "DecideListing")
	if err != nil {
		return err
	}
	err = r.denied(c, m.Computer)
	var name string
	if err == nil {
		err = r.mutateFor(c, m.Computer, func(st *store.State) error {
			var err error
			name, err = st.DecideListing(m.Computer, m.Address, m.Fingerprint, m.Allow)
			return err
		})
	}
	if err == nil {
		r.log.Info("listing decided", "fp", m.Computer, "name", name, "address", m.Address, "server", m.Fingerprint,
			"allow", m.Allow, "by", by)
		if !m.Allow {
			r.mu.Lock()
			if cl := r.conns[m.Computer]; cl != nil && cl.role == control.RoleComputer {
				send(cl, &control.ListingRefused{Address: m.Address, Fingerprint: m.Fingerprint})
			}
			r.mu.Unlock()
		}
	}
	r.reply(c, m.Seq, err)
	return nil
}

func (r *Registry) unregisterComputer(c *Conn, m *control.UnregisterComputer) error {
	by, err := r.techieName(c, "UnregisterComputer")
	if err != nil {
		return err
	}
	err = r.denied(c, m.Fingerprint)
	var name string
	if err == nil {
		err = r.mutateFor(c, m.Fingerprint, func(st *store.State) error {
			var err error
			name, err = st.RemoveComputer(m.Fingerprint)
			return err
		})
	}
	if err == nil {
		r.log.Info("computer unregistered", "fp", m.Fingerprint, "name", name, "by", by)
	}
	r.reply(c, m.Seq, err)
	return nil
}

// mutateFor runs a change for techie c about the computer with fingerprint
// fp, checking again inside the change that a limited c may reach the
// computer, since the state may have changed since denied.
func (r *Registry) mutateFor(c *Conn, fp string, fn func(*store.State) error) error {
	r.mu.Lock()
	v := viewOf(c)
	r.mu.Unlock()
	return r.mutate(func(st *store.State) error {
		if v.limited {
			i := slices.IndexFunc(st.Computers, func(sc store.Computer) bool {
				k, _, err := auth.ParseKey(sc.PublicKey)
				return err == nil && auth.Fingerprint(k) == fp
			})
			if i < 0 || !v.has(st.Computers[i].Customer) {
				return errNoComputer(fp)
			}
		}
		return fn(st)
	})
}

func (r *Registry) addCustomer(c *Conn, m *control.AddCustomer) error {
	by, err := r.techieName(c, "AddCustomer")
	if err != nil {
		return err
	}
	err = r.deniedLimited(c)
	var id string
	if err == nil {
		err = r.mutate(func(st *store.State) error {
			var err error
			id, err = st.AddCustomer(m.Name)
			return err
		})
	}
	if err == nil {
		r.log.Info("customer added", "id", id, "name", strings.TrimSpace(m.Name), "by", by)
	}
	r.reply(c, m.Seq, err)
	return nil
}

func (r *Registry) renameCustomer(c *Conn, m *control.RenameCustomer) error {
	by, err := r.techieName(c, "RenameCustomer")
	if err != nil {
		return err
	}
	err = r.deniedLimited(c)
	var old string
	if err == nil {
		err = r.mutate(func(st *store.State) error {
			if _, err := customerByID(st, m.ID); err != nil {
				return err
			}
			var err error
			old, err = st.RenameCustomer(m.ID, m.Name)
			return err
		})
	}
	if err == nil {
		r.log.Info("customer renamed", "id", m.ID, "from", old, "to", strings.TrimSpace(m.Name), "by", by)
	}
	r.reply(c, m.Seq, err)
	return nil
}

func (r *Registry) removeCustomer(c *Conn, m *control.RemoveCustomer) error {
	by, err := r.techieName(c, "RemoveCustomer")
	if err != nil {
		return err
	}
	err = r.deniedLimited(c)
	var name string
	if err == nil {
		err = r.mutate(func(st *store.State) error {
			if _, err := customerByID(st, m.ID); err != nil {
				return err
			}
			var err error
			name, err = st.RemoveCustomer(m.ID)
			return err
		})
	}
	if err == nil {
		r.log.Info("customer removed", "id", m.ID, "name", name, "by", by)
	}
	r.reply(c, m.Seq, err)
	return nil
}

// moveComputer moves a computer to another customer. A limited techie may
// only move computers of its customers, and only to its customers.
func (r *Registry) moveComputer(c *Conn, m *control.MoveComputer) error {
	by, err := r.techieName(c, "MoveComputer")
	if err != nil {
		return err
	}
	err = r.denied(c, m.Fingerprint)
	r.mu.Lock()
	v := viewOf(c)
	r.mu.Unlock()
	var name string
	if err == nil {
		err = r.mutateFor(c, m.Fingerprint, func(st *store.State) error {
			if m.Customer != "" {
				if _, err := customerByID(st, m.Customer); err != nil {
					return err
				}
			}
			if !v.has(m.Customer) {
				if m.Customer == "" {
					return errLimited
				}
				return fmt.Errorf("no customer %q", m.Customer)
			}
			var err error
			name, err = st.MoveComputer(m.Fingerprint, m.Customer)
			return err
		})
	}
	if err == nil {
		r.log.Info("computer moved", "fp", m.Fingerprint, "name", name, "customer", m.Customer, "by", by)
	}
	r.reply(c, m.Seq, err)
	return nil
}

// limitTechie limits another techie to some customers or lifts its limit.
func (r *Registry) limitTechie(c *Conn, m *control.LimitTechie) error {
	by, err := r.techieName(c, "LimitTechie")
	if err != nil {
		return err
	}
	err = r.deniedLimited(c)
	if err == nil && m.Name == by {
		err = errors.New("you cannot change your own limit")
	}
	if err == nil {
		err = r.mutate(func(st *store.State) error {
			for _, id := range m.Customers {
				if _, err := customerByID(st, id); err != nil {
					return err
				}
			}
			return st.LimitTechie(m.Name, m.Limited, m.Customers)
		})
	}
	if err == nil {
		r.log.Info("techie limit set", "name", m.Name, "limited", m.Limited, "customers", m.Customers, "by", by)
	}
	r.reply(c, m.Seq, err)
	return nil
}

// customerByID finds a customer by id only: the protocol names customers by
// id, so a name that happens to look like one is not taken.
func customerByID(st *store.State, id string) (int, error) {
	i := slices.IndexFunc(st.Customers, func(cu store.Customer) bool { return cu.ID == id })
	if i < 0 {
		return -1, fmt.Errorf("no customer %q", id)
	}
	return i, nil
}

// BeginSession checks that computer connection c may open the session channel
// for requestID: its current request, taken by a techie, with no session yet.
// It returns the session, the techie's peer and the extra data for the
// channel to the techie. The caller opens that channel and then calls
// Started, or End if it could not.
func (r *Registry) BeginSession(c *Conn, requestID string) (*Session, Peer, control.TechiesessionData, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var none control.TechiesessionData
	if c.detached || c.role != control.RoleComputer {
		return nil, nil, none, fmt.Errorf("only registered computers open sessions")
	}
	q := c.req
	if q == nil || q.id != requestID {
		return nil, nil, none, fmt.Errorf("no such request")
	}
	if q.state != control.RequestTaken || q.techie == nil || q.techie.detached {
		return nil, nil, none, fmt.Errorf("request is not taken")
	}
	if q.sess != nil {
		return nil, nil, none, fmt.Errorf("session already open")
	}
	s := &Session{r: r, req: q, start: r.cfg.Now()}
	q.sess = s
	return s, q.techie.peer, control.TechiesessionData{
		RequestID:    q.id,
		ComputerName: c.name,
		ComputerKey:  c.keyLine,
		UserMachine:  c.userMachine,
	}, nil
}

// Started records that both channels are open and spliced. closeFn closes
// them; the registry calls it when the request ends from elsewhere. If the
// request already ended, Started returns false and the caller must close
// the channels itself.
func (s *Session) Started(closeFn func()) bool {
	r := s.r
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.ended {
		return false
	}
	s.started = true
	s.closeFn = closeFn
	r.log.Info("session", "event", "start", "id", s.req.id, "computer", s.req.computer.fp, "techie", s.req.techie.name)
	r.changedLocked()
	return true
}

// Ended reports whether the request ended while the session was being set up.
func (s *Session) Ended() bool {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	return s.ended
}

// End finishes the session and its request, with the bytes copied.
func (s *Session) End(res splice.Result) {
	r := s.r
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log.Info("session", "event", "end", "id", s.req.id, "computer", s.req.computer.fp, "techie", s.req.techie.name,
		"computer_to_techie", res.AToB, "techie_to_computer", res.BToA,
		"duration", r.cfg.Now().Sub(s.start).String())
	s.ended = true
	r.endRequestLocked(s.req, control.RequestEnded, true)
}

// Sweep drops requests whose session never
// started, and closes pending connections past their idle timeout.
func (r *Registry) Sweep() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.cfg.Now()
	for _, q := range r.requests {
		switch {
		case q.state == control.RequestTaken && q.sess == nil && now.Sub(q.takenAt) >= r.cfg.SessionStartTimeout:
			r.endRequestLocked(q, control.RequestEnded, true)
		}
	}
	for _, c := range r.conns {
		if c.role == control.RolePending && now.Sub(c.connectedAt) >= r.cfg.PendingIdle {
			r.log.Info("pending idle timeout", "fp", c.fp, "ip", c.ip)
			r.detachLocked(c)
			c.peer.Kick("pending connection timed out")
		}
	}
}

// LastSeen returns the last-seen times of computers that connected since start.
func (r *Registry) LastSeen() map[string]time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.cfg.Now()
	m := make(map[string]time.Time, len(r.lastSeen))
	for fp, t := range r.lastSeen {
		m[fp] = t
	}
	for fp, c := range r.conns {
		if c.role == control.RoleComputer {
			m[fp] = now
		}
	}
	return m
}

// Counts reports the number of online connections per role.
func (r *Registry) Counts() map[control.Role]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := map[control.Role]int{}
	for _, c := range r.conns {
		m[c.role]++
	}
	return m
}
