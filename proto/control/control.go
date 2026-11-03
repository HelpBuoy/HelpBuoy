// Package control defines the sv-control protocol between the relay and the
// helper and client apps, plus the SSH channel names and extra data used to
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
//	app   -> relay  ClientHello (clients and pending) or nothing (helpers)
//	relay -> app    Hello{Role}
//
// Pending (unknown key): may only send ClientHello. When a helper registers its
// key the relay sends Registered and from then on treats the connection as a
// client (no reconnect needed).
//
// Helper commands carry a Seq; the relay answers each with Reply{Seq, Error}.
// Presence is a full snapshot, sent after Hello and after every change.
//
// Helper keys that send ClientHello anyway get Hello{helper} and the message is ignored.
// Every connection answers the global request keepalive@openssh.com (want-reply true);
// the relay replies true, the apps drop a connection after 3 unanswered keepalives.
//
// Session setup (client is the inner SSH server, helper the inner client). A
// session always starts from a helper and always needs the user's consent:
//
//	helper -> relay   Connect{Seq, Fingerprint, RequestID}  (RequestID chosen by the helper)
//	relay  -> helper  Reply{Seq}  or Reply{Seq, Error} when the client is offline or busy
//	relay  -> helpers Presence (client's Request.State = taken, TakenBy = helper)
//	relay  -> client  SessionReady{RequestID, Helper...}
//	client asks its user whether this helper may take control. If not:
//	client -> relay   CancelRequest{RequestID}
//	relay  -> client  RequestStateMsg{RequestID, cancelled}; helpers see it in Presence
//	If so:
//	client -> relay   opens channel ChannelSession, extra data = SessionChannelData
//	relay  -> helper  opens channel ChannelSession, extra data = HelperSessionData
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
	ChannelSession = "helpbuoy-session@helpbuoy" // opened by the client, and by the relay towards the helper
)

// Role of a connection, decided by the relay from the key alone.
type Role string

const (
	RoleHelper  Role = "helper"
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
	// relay -> helpers
	MsgPresence MsgType = 3
	// helper -> relay
	MsgRegisterClient   MsgType = 4
	MsgUnregisterClient MsgType = 5
	// 6 was OpenRequest
	// relay -> helper
	MsgReply MsgType = 7
	// relay -> client
	MsgRegistered   MsgType = 8
	MsgRequestState MsgType = 9
	MsgSessionReady MsgType = 10
	// client -> relay (11 was RequestHelp)
	MsgCancelRequest MsgType = 12
	// relay -> any: fatal error before the relay closes the connection
	MsgError MsgType = 13
	// helper -> relay
	MsgRenameClient   MsgType = 14
	MsgConnect        MsgType = 15
	MsgRegisterHelper MsgType = 16
)

// Hello is the relay's first message on every control channel.
type Hello struct {
	Role         Role      `json:"role"`
	RelayVersion string    `json:"relay_version"`
	ServerTime   time.Time `json:"server_time"`
	// Name is the helper's name, helpers only. Clients are not told the name
	// helpers gave them; that name is only for helpers.
	Name string `json:"name,omitempty"`
}

// ClientHello is sent by clients and pending connections right after opening
// the control channel.
type ClientHello struct {
	AppVersion  string `json:"app_version"`
	UserMachine string `json:"user_machine"` // current %USERNAME%@%COMPUTERNAME%, lower-case user
}

// Presence is a full snapshot for helpers.
type Presence struct {
	Clients []ClientInfo `json:"clients"` // all registered clients, online or not
	Helpers []HelperInfo `json:"helpers"` // all helpers, online or not
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
	InSessionWith string       `json:"in_session_with,omitempty"` // helper name while a session runs
}

// RequestState of the request a helper's Connect creates.
type RequestState string

const (
	RequestTaken     RequestState = "taken"     // a helper connected; waiting for the user, or session running
	RequestCancelled RequestState = "cancelled" // the user refused
	RequestEnded     RequestState = "ended"     // its session ended, or never started in time
)

// RequestInfo is the request of a helper's Connect as helpers see it.
type RequestInfo struct {
	ID      string       `json:"id"`
	Created time.Time    `json:"created"`
	State   RequestState `json:"state"`
	TakenBy string       `json:"taken_by,omitempty"` // helper name
}

// HelperInfo describes one helper.
type HelperInfo struct {
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

// RegisterHelper adds another helper key, like helpbuoy-relay helper add. Name
// is required (1 to 64 characters, no spaces).
type RegisterHelper struct {
	Seq       uint32 `json:"seq"`
	PublicKey string `json:"public_key"`
	Name      string `json:"name"`
}

// RenameClient changes the name helpers see for a registered client (and the
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
// RequestID is chosen by the helper (16 random bytes in hex) so it can
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

// Reply answers a helper command. Error is empty on success.
type Reply struct {
	Seq   uint32 `json:"seq"`
	Error string `json:"error,omitempty"`
}

// Registered tells a pending connection that it is now a registered client.
// It carries no name: the name a client is registered under is only for
// helpers.
type Registered struct{}

// CancelRequest refuses a helper's Connect (or ends it before the session
// channel is opened).
type CancelRequest struct {
	RequestID string `json:"request_id"`
}

// RequestStateMsg tells the client that a request ended.
type RequestStateMsg struct {
	RequestID string       `json:"request_id"`
	State     RequestState `json:"state"`
}

// SessionReady tells the client that a helper wants to connect. The client
// asks its user and then opens the session channel, or sends CancelRequest.
// HelperKey is the helper's authorized_keys line; the client accepts only this
// key in the inner SSH handshake (and warns if a known helper name shows a
// different key).
type SessionReady struct {
	RequestID  string `json:"request_id"`
	HelperName string `json:"helper_name"`
	HelperKey  string `json:"helper_key"`
}

// Error is sent before the relay closes a connection on purpose.
type Error struct {
	Message string `json:"message"`
}

// SessionChannelData is the extra data of the client's ChannelSession open.
type SessionChannelData struct {
	RequestID string
}

// HelperSessionData is the extra data of the relay's ChannelSession open
// towards the helper. ClientKey is the client's authorized_keys line; the
// helper pins it in the inner handshake and also checks it against its own
// view of the registered client.
type HelperSessionData struct {
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

// MarshalHelperSessionData encodes the relay's channel extra data.
func MarshalHelperSessionData(d HelperSessionData) []byte { return ssh.Marshal(&d) }

// UnmarshalHelperSessionData parses the relay's channel extra data.
func UnmarshalHelperSessionData(b []byte) (HelperSessionData, error) {
	var d HelperSessionData
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
	case *RegisterHelper, RegisterHelper:
		return MsgRegisterHelper, nil
	case *Connect, Connect:
		return MsgConnect, nil
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
	case MsgRegisterHelper:
		return &RegisterHelper{}, nil
	case MsgConnect:
		return &Connect{}, nil
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
