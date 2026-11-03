// Package registry is the relay's in-memory state machine: online
// connections and their roles, help requests and sessions, and the Presence
// snapshot for agents. It knows nothing about SSH; connections are Peers
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

	"github.com/mevdschee/helpbuoy/proto/control"
	"github.com/mevdschee/helpbuoy/relay/auth"
	"github.com/mevdschee/helpbuoy/relay/splice"
	"github.com/mevdschee/helpbuoy/relay/store"
)

// Peer is one SSH connection as the registry sees it.
type Peer interface {
	// Send queues a control message for the connection's control channel.
	Send(m any)
	// NotifyPresence tells an agent connection that the Presence snapshot
	// changed; the connection fetches it with Registry.Presence.
	NotifyPresence()
	// Kick sends an Error with reason and closes the connection.
	Kick(reason string)
}

// Config configures a Registry. Zero durations and counts take the defaults.
type Config struct {
	Now                 func() time.Time
	SessionStartTimeout time.Duration // a taken request must get its session channel within this (1 min)
	PendingIdle         time.Duration // pending connections are closed after this (24 h)
	MaxSessionsPerAgent int           // 20
	Version             string        // relay version for Hello
	// Mutate persists a change to the stored state. The implementation must
	// call SetIndex with the new state before it returns.
	Mutate func(fn func(*store.State) error) error
	Log    *slog.Logger
}

// Protocol errors returned by Handle; the caller closes the connection.
var (
	ErrNotAllowed = errors.New("message not allowed for this role")
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

	control  bool // Hello has been queued; messages may be sent
	detached bool
	release  func() // pending limit slot, nil for known keys
	req      *request
}

// Fingerprint returns the connection's key fingerprint.
func (c *Conn) Fingerprint() string { return c.fp }

type request struct {
	id      string
	client  *Conn
	created time.Time
	state   control.RequestState
	agent   *Conn
	takenAt time.Time
	sess    *Session
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

	gen       uint64
	cacheGen  uint64
	cache     []byte
	cacheTime time.Time
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
	if cfg.MaxSessionsPerAgent == 0 {
		cfg.MaxSessionsPerAgent = 20
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
		keyLine: id.KeyLine, connectedAt: r.cfg.Now()}
	if id.Role == control.RolePending {
		rel, err := admit()
		if err != nil {
			return nil, err
		}
		c.release = rel
	}
	if old := r.conns[fp]; old != nil {
		r.detachLocked(old)
		old.peer.Kick("replaced by a newer connection with the same key")
		r.log.Info("conn replaced", "fp", fp, "role", string(old.role), "old_ip", old.ip, "ip", ip)
	}
	r.conns[fp] = c
	if c.role == control.RoleClient {
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
	case control.RoleClient:
		r.lastSeen[c.fp] = now
		if c.req != nil {
			r.endRequestLocked(c.req, control.RequestCancelled, false)
		}
	case control.RoleAgent:
		for _, req := range r.requests {
			if req.agent == c {
				r.endRequestLocked(req, control.RequestEnded, true)
			}
		}
	}
	r.changedLocked()
}

// OpenControl is called when the connection's control channel is open. It
// queues Hello and, for agents, the first Presence.
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
	h := &control.Hello{
		Role:         c.role,
		RelayVersion: r.cfg.Version,
		ServerTime:   r.cfg.Now().UTC(),
	}
	if c.role == control.RoleAgent {
		h.Name = c.name // clients are not told the name agents gave them
	}
	c.peer.Send(h)
	if c.role == control.RoleAgent {
		c.peer.NotifyPresence()
	}
	return nil
}

// send queues m if the connection can receive messages.
func send(c *Conn, m any) {
	if c.control && !c.detached {
		c.peer.Send(m)
	}
}

// changedLocked marks the Presence snapshot stale and tells the agents.
func (r *Registry) changedLocked() {
	r.gen++
	for _, c := range r.conns {
		if c.role == control.RoleAgent && c.control {
			c.peer.NotifyPresence()
		}
	}
}

// Presence returns the encoded current Presence snapshot and its generation.
// Callers that already sent this generation can skip it.
func (r *Registry) Presence() ([]byte, uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache == nil || r.cacheGen != r.gen {
		b, err := json.Marshal(r.presenceLocked())
		if err != nil {
			panic(err) // only plain data, can't fail
		}
		r.cache, r.cacheGen = b, r.gen
	}
	return r.cache, r.cacheGen
}

// PresenceSnapshot returns the current Presence as a value.
func (r *Registry) PresenceSnapshot() control.Presence {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.presenceLocked()
}

func (r *Registry) presenceLocked() control.Presence {
	now := r.cfg.Now().UTC()
	st := r.idx.State
	p := control.Presence{Clients: []control.ClientInfo{}, Agents: []control.AgentInfo{}}
	for i, sc := range st.Clients {
		fp := r.idx.ClientFingerprint(i)
		ci := control.ClientInfo{
			Fingerprint:  fp,
			Name:         sc.Name,
			RegisteredBy: sc.RegisteredBy,
			RegisteredAt: sc.RegisteredAt,
			LastSeen:     sc.LastSeen,
		}
		if t, ok := r.lastSeen[fp]; ok && t.After(ci.LastSeen) {
			ci.LastSeen = t.UTC()
		}
		if c := r.conns[fp]; c != nil && c.role == control.RoleClient {
			ci.Online = true
			ci.LastSeen = now
			ci.UserMachine = c.userMachine
			if q := c.req; q != nil {
				ri := &control.RequestInfo{ID: q.id, Created: q.created.UTC(), State: q.state}
				if q.agent != nil {
					ri.TakenBy = q.agent.name
					if q.sess != nil && q.sess.started {
						ci.InSessionWith = q.agent.name
					}
				}
				ci.Request = ri
			}
		}
		p.Clients = append(p.Clients, ci)
	}
	for i, h := range st.Agents {
		fp := r.idx.AgentFingerprint(i)
		c := r.conns[fp]
		p.Agents = append(p.Agents, control.AgentInfo{
			Name: h.Name, Fingerprint: fp,
			Online: c != nil && c.role == control.RoleAgent,
		})
	}
	slices.SortFunc(p.Clients, func(a, b control.ClientInfo) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Fingerprint, b.Fingerprint))
	})
	slices.SortFunc(p.Agents, func(a, b control.AgentInfo) int { return cmp.Compare(a.Name, b.Name) })
	return p
}

// SetIndex installs a new stored state. Connections whose key was removed
// or changed role are kicked; pending connections whose key is now a
// registered client are promoted and told so with Registered.
func (r *Registry) SetIndex(idx *store.Index) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.idx = idx
	for fp, c := range r.conns {
		id := idx.Lookup(fp)
		switch {
		case id.Role == c.role:
			c.name, c.keyLine = id.Name, id.KeyLine
		case c.role == control.RolePending && id.Role == control.RoleClient:
			c.role = control.RoleClient
			c.name, c.keyLine = id.Name, id.KeyLine
			if c.release != nil {
				c.release()
				c.release = nil
			}
			r.lastSeen[fp] = r.cfg.Now()
			send(c, &control.Registered{})
			r.log.Info("pending promoted", "fp", fp, "name", id.Name, "ip", c.ip)
		default:
			reason := "key removed from the relay"
			if id.Role != control.RolePending {
				reason = "key changed role"
			}
			r.log.Info("conn revoked", "fp", fp, "role", string(c.role), "new_role", string(id.Role), "ip", c.ip)
			r.detachLocked(c)
			c.peer.Kick(reason)
		}
	}
	r.changedLocked()
}

// Handle processes one control message from c. A returned error is a
// protocol violation; the caller sends it as Error and closes the connection.
func (r *Registry) Handle(c *Conn, m any) error {
	switch m := m.(type) {
	case *control.ClientHello:
		return r.clientHello(c, m)
	case *control.CancelRequest:
		return r.cancelRequest(c, m)
	case *control.Connect:
		return r.connect(c, m)
	case *control.RegisterClient:
		return r.registerClient(c, m)
	case *control.UnregisterClient:
		return r.unregisterClient(c, m)
	case *control.RegisterAgent:
		return r.registerAgent(c, m)
	case *control.RenameClient:
		return r.renameClient(c, m)
	}
	return fmt.Errorf("%w: %T", ErrNotAllowed, m)
}

func (r *Registry) clientHello(c *Conn, m *control.ClientHello) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.role == control.RoleAgent {
		return nil // agents have no ClientHello; ignored per the contract
	}
	um := auth.Truncate(m.UserMachine, 256)
	if um != c.userMachine {
		c.userMachine = um
		if c.role == control.RoleClient {
			r.changedLocked()
		}
	}
	return nil
}

func (r *Registry) cancelRequest(c *Conn, m *control.CancelRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.role != control.RoleClient {
		return fmt.Errorf("%w: CancelRequest from %s", ErrNotAllowed, c.role)
	}
	// This also closes the session channels if they are being opened or open.
	if q := c.req; q != nil && q.id == m.RequestID {
		r.endRequestLocked(q, control.RequestCancelled, true)
	}
	return nil
}

// endRequestLocked finishes a request: it leaves the client, the client is
// told (if notify), and a session for it is closed.
func (r *Registry) endRequestLocked(q *request, state control.RequestState, notify bool) {
	if r.requests[q.id] != q {
		return
	}
	delete(r.requests, q.id)
	q.state = state
	if q.client.req == q {
		q.client.req = nil
	}
	if notify {
		send(q.client, &control.RequestStateMsg{RequestID: q.id, State: state})
	}
	if s := q.sess; s != nil && !s.ended {
		s.ended = true
		if s.closeFn != nil {
			go s.closeFn()
		}
	}
	attrs := []any{"event", string(state), "id", q.id, "client", q.client.fp}
	if q.agent != nil {
		attrs = append(attrs, "agent", q.agent.name)
	}
	r.log.Info("request", attrs...)
	r.changedLocked()
}

func (r *Registry) agentSessionsLocked(h *Conn) int {
	n := 0
	for _, q := range r.requests {
		if q.agent == h {
			n++
		}
	}
	return n
}

// connect starts a session from agent c: a request, taken by c, that waits
// for the client's user to allow it.
func (r *Registry) connect(c *Conn, m *control.Connect) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.role != control.RoleAgent {
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
	case cl == nil || cl.role != control.RoleClient || cl.detached:
		return fail("client is not online")
	case cl.req != nil:
		return fail("client is busy")
	case r.agentSessionsLocked(c) >= r.cfg.MaxSessionsPerAgent:
		return fail(fmt.Sprintf("too many sessions (max %d)", r.cfg.MaxSessionsPerAgent))
	}
	now := r.cfg.Now()
	q := &request{id: m.RequestID, client: cl, created: now, state: control.RequestTaken, agent: c, takenAt: now}
	r.requests[q.id] = q
	cl.req = q
	send(c, &control.Reply{Seq: m.Seq})
	send(cl, &control.SessionReady{
		RequestID: q.id,
		AgentName: c.name,
		AgentKey:  c.keyLine,
	})
	r.log.Info("request", "event", "connect", "id", q.id, "client", cl.fp, "agent", c.name)
	r.changedLocked()
	return nil
}

// agentName returns c's agent name, or an error if c is not an agent.
func (r *Registry) agentName(c *Conn, what string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.role != control.RoleAgent {
		return "", fmt.Errorf("%w: %s from %s", ErrNotAllowed, what, c.role)
	}
	return c.name, nil
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

// registerAgent adds an agent key on behalf of agent c.
func (r *Registry) registerAgent(c *Conn, m *control.RegisterAgent) error {
	by, err := r.agentName(c, "RegisterAgent")
	if err != nil {
		return err
	}
	err = r.mutate(func(st *store.State) error { return st.AddAgent(m.Name, m.PublicKey) })
	if err == nil {
		r.log.Info("agent added", "name", strings.TrimSpace(m.Name), "by", by)
	}
	r.reply(c, m.Seq, err)
	return nil
}

func (r *Registry) registerClient(c *Conn, m *control.RegisterClient) error {
	by, err := r.agentName(c, "RegisterClient")
	if err != nil {
		return err
	}
	var fp, name string
	err = r.mutate(func(st *store.State) error {
		var err error
		fp, name, err = st.AddClient(m.PublicKey, m.Name, by, r.cfg.Now())
		return err
	})
	if err == nil {
		r.log.Info("client registered", "fp", fp, "name", name, "by", by)
	}
	r.reply(c, m.Seq, err)
	return nil
}

func (r *Registry) renameClient(c *Conn, m *control.RenameClient) error {
	by, err := r.agentName(c, "RenameClient")
	if err != nil {
		return err
	}
	var old string
	err = r.mutate(func(st *store.State) error {
		var err error
		old, err = st.RenameClient(m.Fingerprint, m.Name)
		return err
	})
	if err == nil {
		r.log.Info("client renamed", "fp", m.Fingerprint, "from", old, "to", strings.TrimSpace(m.Name), "by", by)
	}
	r.reply(c, m.Seq, err)
	return nil
}

func (r *Registry) unregisterClient(c *Conn, m *control.UnregisterClient) error {
	by, err := r.agentName(c, "UnregisterClient")
	if err != nil {
		return err
	}
	var name string
	err = r.mutate(func(st *store.State) error {
		var err error
		name, err = st.RemoveClient(m.Fingerprint)
		return err
	})
	if err == nil {
		r.log.Info("client unregistered", "fp", m.Fingerprint, "name", name, "by", by)
	}
	r.reply(c, m.Seq, err)
	return nil
}

// BeginSession checks that client connection c may open the session channel
// for requestID: its current request, taken by an agent, with no session yet.
// It returns the session, the agent's peer and the extra data for the
// channel to the agent. The caller opens that channel and then calls
// Started, or End if it could not.
func (r *Registry) BeginSession(c *Conn, requestID string) (*Session, Peer, control.AgentSessionData, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var none control.AgentSessionData
	if c.detached || c.role != control.RoleClient {
		return nil, nil, none, fmt.Errorf("only registered clients open sessions")
	}
	q := c.req
	if q == nil || q.id != requestID {
		return nil, nil, none, fmt.Errorf("no such request")
	}
	if q.state != control.RequestTaken || q.agent == nil || q.agent.detached {
		return nil, nil, none, fmt.Errorf("request is not taken")
	}
	if q.sess != nil {
		return nil, nil, none, fmt.Errorf("session already open")
	}
	s := &Session{r: r, req: q, start: r.cfg.Now()}
	q.sess = s
	return s, q.agent.peer, control.AgentSessionData{
		RequestID:   q.id,
		ClientName:  c.name,
		ClientKey:   c.keyLine,
		UserMachine: c.userMachine,
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
	r.log.Info("session", "event", "start", "id", s.req.id, "client", s.req.client.fp, "agent", s.req.agent.name)
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
	r.log.Info("session", "event", "end", "id", s.req.id, "client", s.req.client.fp, "agent", s.req.agent.name,
		"client_to_agent", res.AToB, "agent_to_client", res.BToA,
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

// LastSeen returns the last-seen times of clients that connected since start.
func (r *Registry) LastSeen() map[string]time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.cfg.Now()
	m := make(map[string]time.Time, len(r.lastSeen))
	for fp, t := range r.lastSeen {
		m[fp] = t
	}
	for fp, c := range r.conns {
		if c.role == control.RoleClient {
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
