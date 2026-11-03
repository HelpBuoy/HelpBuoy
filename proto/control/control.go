// Package control defines the sv-control protocol between the relay and the
// agent and client apps, plus the SSH channel names and extra data used to
// set up sessions. This file is the contract shared by relay/, desk/ and
// client-windows/; change it only together with all three.
//
// # Transport
//
// Every app connects to the relay over SSH with its own Ed25519 key (user
// name "helpbuoy", ignored). After auth it opens exactly one channel of type
// ChannelControl. Messages on it are wire frames (proto/wire) whose type byte
// is a MsgType and whose payload is the JSON encoding of the matching struct.
//
// # Flows
//
// Any role, right after opening the control channel:
//
//	app   -> relay  ClientHello (clients and pending) or nothing (agents)
//	relay -> app    Hello{Role}
//
// Pending (unknown key): may only send ClientHello. When an agent registers its
// key the relay sends Registered and from then on treats the connection as a
// client (no reconnect needed).
//
// Right after Hello the relay sends RelayInfo (title and instruction line set
// by the agents), and again to every connection whenever it changes.
//
// Agent commands carry a Seq; the relay answers each with Reply{Seq, Error}.
// Presence is a full snapshot, sent after Hello and after every change.
//
// Agent keys that send ClientHello anyway get Hello{agent} and the message is ignored.
// Every connection answers the global request keepalive@openssh.com (want-reply true);
// the relay replies true, the apps drop a connection after 3 unanswered keepalives.
//
// Session setup (client is the inner SSH server, agent the inner client). A
// session always starts from an agent and always needs the user's consent:
//
//	agent -> relay   Connect{Seq, Fingerprint, RequestID}  (RequestID chosen by the agent)
//	relay  -> agent  Reply{Seq}  or Reply{Seq, Error} when the client is offline or busy
//	relay  -> agents Presence (client's Request.State = taken, TakenBy = agent)
//	relay  -> client  SessionReady{RequestID, Agent...}
//	client asks its user whether this agent may take control. If not:
//	client -> relay   CancelRequest{RequestID}
//	relay  -> client  RequestStateMsg{RequestID, cancelled}; agents see it in Presence
//	If so:
//	client -> relay   opens channel ChannelSession, extra data = SessionChannelData
//	relay  -> agent  opens channel ChannelSession, extra data = AgentSessionData
//	relay splices the two channels; inner SSH runs end to end.
//
// The relay ends a request whose session channel did not come within its
// session start timeout (1 min) and tells the client RequestStateMsg{ended}.
// A session ends when either side closes its session channel; the relay then
// closes the other one and updates Presence.
package control

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/helpbuoy/proto/wire"
)

// SSH names.
const (
	SSHUser        = "helpbuoy"                  // user name sent by apps; the relay ignores it
	ChannelControl = "helpbuoy-control@helpbuoy" // one per connection, opened by the app
	ChannelSession = "helpbuoy-session@helpbuoy" // opened by the client, and by the relay towards the agent
)

// Role of a connection, decided by the relay from the key alone.
type Role string

const (
	RoleAgent   Role = "agent"
	RoleClient  Role = "client"
	RolePending Role = "pending"
)

// MsgType is the wire frame type byte.
type MsgType uint8

const (
	// relay -> any
	MsgHello MsgType = 1
	// client/pending -> relay
	MsgClientHello MsgType = 2
	// relay -> agents
	MsgPresence MsgType = 3
	// agent -> relay
	MsgRegisterClient   MsgType = 4
	MsgUnregisterClient MsgType = 5
	// 6 was OpenRequest
	// relay -> agent
	MsgReply MsgType = 7
	// relay -> client
	MsgRegistered   MsgType = 8
	MsgRequestState MsgType = 9
	MsgSessionReady MsgType = 10
	// client -> relay (11 was RequestHelp)
	MsgCancelRequest MsgType = 12
	// relay -> any: fatal error before the relay closes the connection
	MsgError MsgType = 13
	// agent -> relay
	MsgRenameClient  MsgType = 14
	MsgConnect       MsgType = 15
	MsgRegisterAgent MsgType = 16
	MsgSetRelayInfo  MsgType = 18
	// relay -> any
	MsgRelayInfo MsgType = 17
)

// Hello is the relay's first message on every control channel.
type Hello struct {
	Role         Role      `json:"role"`
	RelayVersion string    `json:"relay_version"`
	ServerTime   time.Time `json:"server_time"`
	// Name is the agent's name, agents only. Clients are not told the name
	// agents gave them; that name is only for agents.
	Name string `json:"name,omitempty"`
}

// RelayInfo is the relay's title and instruction line, e.g. "Example Inc" and
// "Call +1 234 567 890 for support". Both may be empty. Clients show them to
// their user; agents show and edit them on the Relay tab.
type RelayInfo struct {
	Title       string `json:"title"`
	Instruction string `json:"instruction"`
}

// SetRelayInfo replaces the relay's title and instruction line (each one line
// of printable text, title at most 64 and instruction at most 160 characters,
// empty to clear). Every connection gets the new RelayInfo.
type SetRelayInfo struct {
	Seq         uint32 `json:"seq"`
	Title       string `json:"title"`
	Instruction string `json:"instruction"`
}

// ClientHello is sent by clients and pending connections right after opening
// the control channel.
type ClientHello struct {
	AppVersion  string `json:"app_version"`
	UserMachine string `json:"user_machine"` // current %USERNAME%@%COMPUTERNAME%, lower-case user
}

// Presence is a full snapshot for agents.
type Presence struct {
	Clients []ClientInfo `json:"clients"` // all registered clients, online or not
	Agents  []AgentInfo  `json:"agents"`  // all agents, online or not
}

// ClientInfo describes one registered client.
type ClientInfo struct {
	Fingerprint   string       `json:"fingerprint"` // SHA256:...
	Name          string       `json:"name"`        // user@machine from the key comment
	Online        bool         `json:"online"`
	UserMachine   string       `json:"user_machine,omitempty"` // as reported in ClientHello while online
	RegisteredBy  string       `json:"registered_by"`
	RegisteredAt  time.Time    `json:"registered_at"`
	LastSeen      time.Time    `json:"last_seen"`
	Request       *RequestInfo `json:"request,omitempty"`         // request of a Connect, nil if none
	InSessionWith string       `json:"in_session_with,omitempty"` // agent name while a session runs
}

// RequestState of the request an agent's Connect creates.
type RequestState string

const (
	RequestTaken     RequestState = "taken"     // an agent connected; waiting for the user, or session running
	RequestCancelled RequestState = "cancelled" // the user refused
	RequestEnded     RequestState = "ended"     // its session ended, or never started in time
)

// RequestInfo is the request of an agent's Connect as agents see it.
type RequestInfo struct {
	ID      string       `json:"id"`
	Created time.Time    `json:"created"`
	State   RequestState `json:"state"`
	TakenBy string       `json:"taken_by,omitempty"` // agent name
}

// AgentInfo describes one agent.
type AgentInfo struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	Online      bool   `json:"online"`
}

// RegisterClient registers a client key. PublicKey is the authorized_keys line
// as pasted (already cleaned by proto/identity); Name overrides the key
// comment and is required when the comment is empty.
type RegisterClient struct {
	Seq       uint32 `json:"seq"`
	PublicKey string `json:"public_key"`
	Name      string `json:"name,omitempty"`
}

// RegisterAgent adds another agent key, like helpbuoy-relay agent add. Name
// is required (1 to 64 characters, no spaces).
type RegisterAgent struct {
	Seq       uint32 `json:"seq"`
	PublicKey string `json:"public_key"`
	Name      string `json:"name"`
}

// RenameClient changes the name agents see for a registered client (and the
// comment of its stored key line). Name may contain spaces, e.g. "Anna laptop".
type RenameClient struct {
	Seq         uint32 `json:"seq"`
	Fingerprint string `json:"fingerprint"`
	Name        string `json:"name"`
}

// UnregisterClient removes a client; an online client is disconnected.
type UnregisterClient struct {
	Seq         uint32 `json:"seq"`
	Fingerprint string `json:"fingerprint"`
}

// Connect starts a session with an online client, once its user allows it.
// RequestID is chosen by the agent (16 random bytes in hex) so it can
// recognize the session channel that follows.
type Connect struct {
	Seq         uint32 `json:"seq"`
	Fingerprint string `json:"fingerprint"`
	RequestID   string `json:"request_id"`
}

// NewRequestID returns a random request id for Connect.
func NewRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// ValidRequestID accepts ids as NewRequestID makes them: 32 lower-case hex digits.
func ValidRequestID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, ch := range id {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

// Reply answers an agent command. Error is empty on success.
type Reply struct {
	Seq   uint32 `json:"seq"`
	Error string `json:"error,omitempty"`
}

// Registered tells a pending connection that it is now a registered client.
// It carries no name: the name a client is registered under is only for
// agents.
type Registered struct{}

// CancelRequest refuses an agent's Connect (or ends it before the session
// channel is opened).
type CancelRequest struct {
	RequestID string `json:"request_id"`
}

// RequestStateMsg tells the client that a request ended.
type RequestStateMsg struct {
	RequestID string       `json:"request_id"`
	State     RequestState `json:"state"`
}

// SessionReady tells the client that an agent wants to connect. The client
// asks its user and then opens the session channel, or sends CancelRequest.
// AgentKey is the agent's authorized_keys line; the client accepts only this
// key in the inner SSH handshake (and warns if a known agent name shows a
// different key).
type SessionReady struct {
	RequestID string `json:"request_id"`
	AgentName string `json:"agent_name"`
	AgentKey  string `json:"agent_key"`
}

// Error is sent before the relay closes a connection on purpose.
type Error struct {
	Message string `json:"message"`
}

// SessionChannelData is the extra data of the client's ChannelSession open.
type SessionChannelData struct {
	RequestID string
}

// AgentSessionData is the extra data of the relay's ChannelSession open
// towards the agent. ClientKey is the client's authorized_keys line; the
// agent pins it in the inner handshake and also checks it against its own
// view of the registered client.
type AgentSessionData struct {
	RequestID   string
	ClientName  string
	ClientKey   string
	UserMachine string
}

// MarshalSessionChannelData encodes the client's channel extra data in SSH wire format.
func MarshalSessionChannelData(d SessionChannelData) []byte { return ssh.Marshal(&d) }

// UnmarshalSessionChannelData parses the client's channel extra data.
func UnmarshalSessionChannelData(b []byte) (SessionChannelData, error) {
	var d SessionChannelData
	err := ssh.Unmarshal(b, &d)
	return d, err
}

// MarshalAgentSessionData encodes the relay's channel extra data.
func MarshalAgentSessionData(d AgentSessionData) []byte { return ssh.Marshal(&d) }

// UnmarshalAgentSessionData parses the relay's channel extra data.
func UnmarshalAgentSessionData(b []byte) (AgentSessionData, error) {
	var d AgentSessionData
	err := ssh.Unmarshal(b, &d)
	return d, err
}

// typeOf maps message structs to their type byte.
func typeOf(m any) (MsgType, error) {
	switch m.(type) {
	case *Hello, Hello:
		return MsgHello, nil
	case *ClientHello, ClientHello:
		return MsgClientHello, nil
	case *Presence, Presence:
		return MsgPresence, nil
	case *RegisterClient, RegisterClient:
		return MsgRegisterClient, nil
	case *UnregisterClient, UnregisterClient:
		return MsgUnregisterClient, nil
	case *RenameClient, RenameClient:
		return MsgRenameClient, nil
	case *RegisterAgent, RegisterAgent:
		return MsgRegisterAgent, nil
	case *Connect, Connect:
		return MsgConnect, nil
	case *RelayInfo, RelayInfo:
		return MsgRelayInfo, nil
	case *SetRelayInfo, SetRelayInfo:
		return MsgSetRelayInfo, nil
	case *Reply, Reply:
		return MsgReply, nil
	case *Registered, Registered:
		return MsgRegistered, nil
	case *RequestStateMsg, RequestStateMsg:
		return MsgRequestState, nil
	case *SessionReady, SessionReady:
		return MsgSessionReady, nil
	case *CancelRequest, CancelRequest:
		return MsgCancelRequest, nil
	case *Error, Error:
		return MsgError, nil
	}
	return 0, fmt.Errorf("control: unknown message type %T", m)
}

// newOf returns a pointer to a zero message for a type byte.
func newOf(t MsgType) (any, error) {
	switch t {
	case MsgHello:
		return &Hello{}, nil
	case MsgClientHello:
		return &ClientHello{}, nil
	case MsgPresence:
		return &Presence{}, nil
	case MsgRegisterClient:
		return &RegisterClient{}, nil
	case MsgUnregisterClient:
		return &UnregisterClient{}, nil
	case MsgRenameClient:
		return &RenameClient{}, nil
	case MsgRegisterAgent:
		return &RegisterAgent{}, nil
	case MsgConnect:
		return &Connect{}, nil
	case MsgRelayInfo:
		return &RelayInfo{}, nil
	case MsgSetRelayInfo:
		return &SetRelayInfo{}, nil
	case MsgReply:
		return &Reply{}, nil
	case MsgRegistered:
		return &Registered{}, nil
	case MsgRequestState:
		return &RequestStateMsg{}, nil
	case MsgSessionReady:
		return &SessionReady{}, nil
	case MsgCancelRequest:
		return &CancelRequest{}, nil
	case MsgError:
		return &Error{}, nil
	}
	return nil, fmt.Errorf("control: unknown message type byte %d", t)
}

// MaxMessage bounds a control message payload. Presence for many clients is
// the largest message.
const MaxMessage = 4 << 20

// Conn reads and writes control messages on a channel. Send is safe for
// concurrent use; Receive is not.
type Conn struct {
	w *wire.Writer
	r *wire.Reader
}

// NewConn wraps a control channel (an ssh.Channel or any stream).
func NewConn(rw interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
}) *Conn {
	return &Conn{w: wire.NewWriter(rw), r: wire.NewReader(rw)}
}

// Send writes one message (a struct or pointer from this package).
func (c *Conn) Send(m any) error {
	t, err := typeOf(m)
	if err != nil {
		return err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return c.w.WriteFrame(uint8(t), b)
}

// Receive reads one message and returns a pointer to it, e.g. *Hello.
func (c *Conn) Receive() (any, error) {
	t, b, err := c.r.ReadFrame()
	if err != nil {
		return nil, err
	}
	if len(b) > MaxMessage {
		return nil, fmt.Errorf("control: message of %d bytes too large", len(b))
	}
	m, err := newOf(MsgType(t))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, fmt.Errorf("control: decode %T: %w", m, err)
	}
	return m, nil
}
