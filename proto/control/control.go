// Package control defines the sv-control protocol between the server and the
// techie and client apps, plus the SSH channel names and extra data used to
// set up sessions. This file is the contract shared by server/, desk/ and
// client-windows/; change it only together with all three.
//
// # Transport
//
// Every app connects to the server over SSH with its own Ed25519 key (user
// name "helpbuoy", ignored). After auth it opens exactly one channel of type
// ChannelControl. Messages on it are wire frames (proto/wire) whose type byte
// is a MsgType and whose payload is the JSON encoding of the matching struct.
//
// # Flows
//
// Any role, right after opening the control channel:
//
//	app   -> server  ComputerHello (computers and pending) or nothing (techies)
//	server -> app    Hello{Role}
//
// Pending (unknown key): may only send ComputerHello. When a techie registers its
// key the server sends Registered and from then on treats the connection as a
// computer (no reconnect needed).
//
// Right after Hello the server sends ServerInfo (title and instruction line set
// by the techies), and again to every connection whenever it changes.
//
// Techie commands carry a Seq; the server answers each with Reply{Seq, Error}.
// Presence is a full snapshot, sent after Hello and after every change.
//
// Techie keys that send ComputerHello anyway get Hello{techie} and the message is ignored.
// Every connection answers the global request keepalive@openssh.com (want-reply true);
// the server replies true, the apps drop a connection after 3 unanswered keepalives.
//
// Session setup (computer is the inner SSH server, techie the inner client). A
// session always starts from a techie and always needs the user's consent:
//
//	techie -> server   Connect{Seq, Fingerprint, RequestID}  (RequestID chosen by the techie)
//	server  -> techie  Reply{Seq}  or Reply{Seq, Error} when the computer is offline or busy
//	server  -> techies Presence (computer's Request.State = taken, TakenBy = techie)
//	server  -> computer  SessionReady{RequestID, Techie...}
//	computer asks its user whether this techie may take control. If not:
//	computer -> server   CancelRequest{RequestID}
//	server  -> computer  RequestStateMsg{RequestID, cancelled}; techies see it in Presence
//	The techie may also give up waiting:
//	techie -> server   CancelRequest{RequestID}
//	server  -> computer  RequestStateMsg{RequestID, cancelled}
//	If so:
//	computer -> server   opens channel ChannelSession, extra data = SessionChannelData
//	server  -> techie  opens channel ChannelSession, extra data = TechiesessionData
//	server splices the two channels; inner SSH runs end to end.
//
// The server ends a request whose session channel did not come within its
// session start timeout (1 min) and tells the computer RequestStateMsg{ended}.
// A session ends when either side closes its session channel; the server then
// closes the other one and updates Presence.
package control

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/proto/wire"
)

// SSH names.
const (
	SSHUser        = "helpbuoy"                  // user name sent by apps; the server ignores it
	ChannelControl = "helpbuoy-control@helpbuoy" // one per connection, opened by the app
	ChannelSession = "helpbuoy-session@helpbuoy" // opened by the computer, and by the server towards the techie
)

// Role of a connection, decided by the server from the key alone.
type Role string

const (
	RoleTechie   Role = "techie"
	RoleComputer Role = "computer"
	RolePending  Role = "pending"
)

// MsgType is the wire frame type byte.
type MsgType uint8

const (
	// server -> any
	MsgHello MsgType = 1
	// computer/pending -> server
	MsgComputerHello MsgType = 2
	// server -> techies
	MsgPresence MsgType = 3
	// techie -> server
	MsgRegisterComputer   MsgType = 4
	MsgUnregisterComputer MsgType = 5
	// 6 was OpenRequest
	// server -> techie
	MsgReply MsgType = 7
	// server -> computer
	MsgRegistered   MsgType = 8
	MsgRequestState MsgType = 9
	MsgSessionReady MsgType = 10
	// computer -> server (11 was RequestHelp)
	MsgCancelRequest MsgType = 12
	// server -> any: fatal error before the server closes the connection
	MsgError MsgType = 13
	// techie -> server
	MsgRenameComputer   MsgType = 14
	MsgConnect          MsgType = 15
	MsgRegisterTechie   MsgType = 16
	MsgSetServerInfo    MsgType = 18
	MsgUnregisterTechie MsgType = 19
	// server -> any
	MsgServerInfo MsgType = 17
)

// Hello is the server's first message on every control channel.
type Hello struct {
	Role          Role      `json:"role"`
	ServerVersion string    `json:"server_version"`
	ServerTime    time.Time `json:"server_time"`
	// Name is the techie's name, techies only. Computers are not told the name
	// techies gave them; that name is only for techies.
	Name string `json:"name,omitempty"`
	// Salt is the server's verification salt in hex, for the verified server
	// check (PLAN.md 12.4). Only registered computers and techies get it,
	// never pending keys.
	Salt string `json:"salt,omitempty"`
}

// ServerInfo is the server's title and instruction line, e.g. "Example Inc" and
// "Call +1 234 567 890 for support". Both may be empty. Computers show them to
// their user; techies show and edit them on the Server tab.
type ServerInfo struct {
	Title       string `json:"title"`
	Instruction string `json:"instruction"`
}

// DefaultInstruction is what computers show when the server's instruction line
// is empty. An empty title shows nothing. The apps show the same text through
// i18n.Trans so it is translated; keep them in sync.
const DefaultInstruction = "A techie can now ask to connect. You will be asked first, " +
	"and pressing Esc always takes back control."

// SetServerInfo replaces the server's title and instruction line (each one line
// of printable text, title at most 64 and instruction at most 160 characters,
// empty to clear). Every connection gets the new ServerInfo.
type SetServerInfo struct {
	Seq         uint32 `json:"seq"`
	Title       string `json:"title"`
	Instruction string `json:"instruction"`
}

// ComputerHello is sent by computers and pending connections right after opening
// the control channel.
type ComputerHello struct {
	AppVersion  string `json:"app_version"`
	UserMachine string `json:"user_machine"` // current %USERNAME%@%COMPUTERNAME%, lower-case user
}

// Presence is a full snapshot for techies.
type Presence struct {
	Computers []ComputerInfo `json:"computers"` // all registered computers, online or not
	Techies   []TechieInfo   `json:"techies"`   // all techies, online or not
}

// ComputerInfo describes one registered computer.
type ComputerInfo struct {
	Fingerprint   string       `json:"fingerprint"` // SHA256:...
	Name          string       `json:"name"`        // user@machine from the key comment
	Online        bool         `json:"online"`
	UserMachine   string       `json:"user_machine,omitempty"` // as reported in ComputerHello while online
	RegisteredBy  string       `json:"registered_by"`
	RegisteredAt  time.Time    `json:"registered_at"`
	LastSeen      time.Time    `json:"last_seen"`
	Request       *RequestInfo `json:"request,omitempty"`         // request of a Connect, nil if none
	InSessionWith string       `json:"in_session_with,omitempty"` // techie name while a session runs
}

// RequestState of the request a techie's Connect creates.
type RequestState string

const (
	RequestTaken     RequestState = "taken"     // a techie connected; waiting for the user, or session running
	RequestCancelled RequestState = "cancelled" // the user refused
	RequestEnded     RequestState = "ended"     // its session ended, or never started in time
)

// RequestInfo is the request of a techie's Connect as techies see it.
type RequestInfo struct {
	ID      string       `json:"id"`
	Created time.Time    `json:"created"`
	State   RequestState `json:"state"`
	TakenBy string       `json:"taken_by,omitempty"` // techie name
}

// TechieInfo describes one techie.
type TechieInfo struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	Online      bool   `json:"online"`
}

// RegisterComputer registers a computer key. PublicKey is the authorized_keys line
// as pasted (already cleaned by proto/identity); Name overrides the key
// comment and is required when the comment is empty.
type RegisterComputer struct {
	Seq       uint32 `json:"seq"`
	PublicKey string `json:"public_key"`
	Name      string `json:"name,omitempty"`
}

// RegisterTechie adds another techie key, like helpbuoy-server techie add. Name
// is required (1 to 64 characters, no spaces).
type RegisterTechie struct {
	Seq       uint32 `json:"seq"`
	PublicKey string `json:"public_key"`
	Name      string `json:"name"`
}

// RenameComputer changes the name techies see for a registered computer (and the
// comment of its stored key line). Name may contain spaces, e.g. "Anna laptop".
type RenameComputer struct {
	Seq         uint32 `json:"seq"`
	Fingerprint string `json:"fingerprint"`
	Name        string `json:"name"`
}

// UnregisterComputer removes a computer; an online computer is disconnected.
type UnregisterComputer struct {
	Seq         uint32 `json:"seq"`
	Fingerprint string `json:"fingerprint"`
}

// UnregisterTechie removes another techie, like helpbuoy-server techie remove;
// an online techie is disconnected. A techie cannot remove itself.
type UnregisterTechie struct {
	Seq  uint32 `json:"seq"`
	Name string `json:"name"`
}

// Connect starts a session with an online computer, once its user allows it.
// RequestID is chosen by the techie (16 random bytes in hex) so it can
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

// Reply answers a techie command. Error is empty on success.
type Reply struct {
	Seq   uint32 `json:"seq"`
	Error string `json:"error,omitempty"`
}

// Registered tells a pending connection that it is now a registered computer.
// It carries no name: the name a computer is registered under is only for
// techies.
type Registered struct {
	// Salt is the server's verification salt in hex, like Hello.Salt.
	Salt string `json:"salt,omitempty"`
}

// CancelRequest refuses a techie's Connect (or ends it before the session
// channel is opened). The techie who sent the Connect may send it too, to
// stop waiting for the user.
type CancelRequest struct {
	RequestID string `json:"request_id"`
}

// RequestStateMsg tells the computer that a request ended.
type RequestStateMsg struct {
	RequestID string       `json:"request_id"`
	State     RequestState `json:"state"`
}

// SessionReady tells the computer that a techie wants to connect. The computer
// asks its user and then opens the session channel, or sends CancelRequest.
// TechieKey is the techie's authorized_keys line; the computer accepts only this
// key in the inner SSH handshake (and warns if a known techie name shows a
// different key).
type SessionReady struct {
	RequestID  string `json:"request_id"`
	TechieName string `json:"techie_name"`
	TechieKey  string `json:"techie_key"`
}

// Error is sent before the server closes a connection on purpose.
type Error struct {
	Message string `json:"message"`
}

// ReasonReplaced is the Error message for a connection that was replaced by a
// newer one with the same key. Apps recognize it by this exact text, so it
// must not change.
const ReasonReplaced = "replaced by a newer connection with the same key"

// SessionChannelData is the extra data of the computer's ChannelSession open.
type SessionChannelData struct {
	RequestID string
}

// TechiesessionData is the extra data of the server's ChannelSession open
// towards the techie. ComputerKey is the computer's authorized_keys line; the
// techie pins it in the inner handshake and also checks it against its own
// view of the registered computer.
type TechiesessionData struct {
	RequestID    string
	ComputerName string
	ComputerKey  string
	UserMachine  string
}

// MarshalSessionChannelData encodes the computer's channel extra data in SSH wire format.
func MarshalSessionChannelData(d SessionChannelData) []byte { return ssh.Marshal(&d) }

// UnmarshalSessionChannelData parses the computer's channel extra data.
func UnmarshalSessionChannelData(b []byte) (SessionChannelData, error) {
	var d SessionChannelData
	err := ssh.Unmarshal(b, &d)
	return d, err
}

// MarshalTechiesessionData encodes the server's channel extra data.
func MarshalTechiesessionData(d TechiesessionData) []byte { return ssh.Marshal(&d) }

// UnmarshalTechiesessionData parses the server's channel extra data.
func UnmarshalTechiesessionData(b []byte) (TechiesessionData, error) {
	var d TechiesessionData
	err := ssh.Unmarshal(b, &d)
	return d, err
}

// typeOf maps message structs to their type byte.
func typeOf(m any) (MsgType, error) {
	switch m.(type) {
	case *Hello, Hello:
		return MsgHello, nil
	case *ComputerHello, ComputerHello:
		return MsgComputerHello, nil
	case *Presence, Presence:
		return MsgPresence, nil
	case *RegisterComputer, RegisterComputer:
		return MsgRegisterComputer, nil
	case *UnregisterComputer, UnregisterComputer:
		return MsgUnregisterComputer, nil
	case *RenameComputer, RenameComputer:
		return MsgRenameComputer, nil
	case *RegisterTechie, RegisterTechie:
		return MsgRegisterTechie, nil
	case *UnregisterTechie, UnregisterTechie:
		return MsgUnregisterTechie, nil
	case *Connect, Connect:
		return MsgConnect, nil
	case *ServerInfo, ServerInfo:
		return MsgServerInfo, nil
	case *SetServerInfo, SetServerInfo:
		return MsgSetServerInfo, nil
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
	case MsgComputerHello:
		return &ComputerHello{}, nil
	case MsgPresence:
		return &Presence{}, nil
	case MsgRegisterComputer:
		return &RegisterComputer{}, nil
	case MsgUnregisterComputer:
		return &UnregisterComputer{}, nil
	case MsgRenameComputer:
		return &RenameComputer{}, nil
	case MsgRegisterTechie:
		return &RegisterTechie{}, nil
	case MsgUnregisterTechie:
		return &UnregisterTechie{}, nil
	case MsgConnect:
		return &Connect{}, nil
	case MsgServerInfo:
		return &ServerInfo{}, nil
	case MsgSetServerInfo:
		return &SetServerInfo{}, nil
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

// MaxMessage bounds a control message payload. Presence for many computers is
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
// Messages of a type this version doesn't know are skipped, so a newer peer
// can add message types without breaking older apps.
func (c *Conn) Receive() (any, error) {
	for {
		t, b, err := c.r.ReadFrame()
		if err != nil {
			return nil, err
		}
		if len(b) > MaxMessage {
			return nil, fmt.Errorf("control: message of %d bytes too large", len(b))
		}
		if _, err := newOf(MsgType(t)); err != nil {
			continue
		}
		return Decode(t, b)
	}
}

// Decode turns one frame's type byte and payload into a pointer to its
// message. Unlike Receive it fails on a type it doesn't know, for the server,
// which accepts only the messages it knows.
func Decode(t uint8, b []byte) (any, error) {
	m, err := newOf(MsgType(t))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, fmt.Errorf("control: decode %T: %w", m, err)
	}
	return m, nil
}
