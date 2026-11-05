package session

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"syscall"

	"github.com/mevdschee/helpbuoy/proto/i18n"
)

// ErrHostKeyChanged is returned when the server presents a host key other than
// the pinned one. It is a hard error: the computer stops connecting.
var ErrHostKeyChanged = errors.New("server identity changed")

// HostKeyChangedError is the ErrHostKeyChanged a server caused by showing
// another host key than the pinned one.
type HostKeyChangedError struct {
	OldFingerprint string // pinned
	NewFingerprint string // shown by the server now
	NewKeyLine     string // authorized_keys line of the new key, to pin it if the user trusts it
}

func (e *HostKeyChangedError) Error() string {
	return ErrHostKeyChanged.Error() + ": was " + e.OldFingerprint + ", now " + e.NewFingerprint
}

func (e *HostKeyChangedError) Unwrap() error { return ErrHostKeyChanged }

// ErrNotServer is returned when the other end is not a helpbuoy server.
var ErrNotServer = errors.New("not a HelpBuoy server")

// ErrRefusedKey is returned when the server refuses the app key.
var ErrRefusedKey = errors.New("the server refused this key")

// ErrTechieKey is returned when the key belongs to a techie on this server.
var ErrTechieKey = errors.New("this key is a techie key on this server")

// ServerError is a message the server sent with control.Error before closing.
type ServerError struct{ Message string }

func (e *ServerError) Error() string { return "server: " + e.Message }

// Kind classifies connection errors for the first-run window.
type Kind int

// Error kinds.
const (
	KindOther Kind = iota
	KindNameNotFound
	KindRefused
	KindTimeout
	KindNotServer
	KindRefusedKey
	KindHostKeyChanged
	KindTechieKey
	KindServer
)

// Classify returns the kind of a connection error.
func Classify(err error) Kind {
	var dnsErr *net.DNSError
	var serverErr *ServerError
	var errno syscall.Errno
	var netErr net.Error
	switch {
	case err == nil:
		return KindOther
	case errors.Is(err, ErrHostKeyChanged):
		return KindHostKeyChanged
	case errors.Is(err, ErrTechieKey):
		return KindTechieKey
	case errors.Is(err, ErrRefusedKey):
		return KindRefusedKey
	case errors.As(err, &serverErr):
		return KindServer
	case errors.Is(err, ErrNotServer):
		return KindNotServer
	case errors.As(err, &dnsErr):
		// IsNotFound is not always set (for example on Windows for some
		// resolvers); any DNS failure means the name could not be used.
		return KindNameNotFound
	case errors.As(err, &errno) && isRefused(errno):
		return KindRefused
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return KindTimeout
	case errors.As(err, &netErr) && netErr.Timeout():
		return KindTimeout
	}
	return KindOther
}

// isRefused matches ECONNREFUSED and its Windows sockets value WSAECONNREFUSED.
func isRefused(e syscall.Errno) bool {
	return e == syscall.ECONNREFUSED || e == 10061
}

// Describe returns a short sentence for the user.
func Describe(err error) string {
	switch Classify(err) {
	case KindNameNotFound:
		return i18n.Trans("This name was not found. Check the server address.")
	case KindRefused:
		return i18n.Trans("Nothing is answering at this address. Check the address and the port, and whether the server is running.")
	case KindTimeout:
		return i18n.Trans("The server did not answer in time. Check the address and your internet connection.")
	case KindNotServer:
		return i18n.Trans("This address does not belong to a HelpBuoy server.")
	case KindRefusedKey:
		return i18n.Trans("The server refused this PC for now. Try again later.")
	case KindHostKeyChanged:
		return i18n.Trans("The server's identity changed, so HelpBuoy stopped connecting. Ask a techie before you continue. " +
			"Restart HelpBuoy to answer the warning again.")
	case KindTechieKey:
		return i18n.Trans("This key belongs to a techie on this server and can't be used here.")
	case KindServer:
		var re *ServerError
		errors.As(err, &re)
		return i18n.Trans("The server closed the connection: {0}.", strings.TrimSuffix(re.Message, "."))
	}
	if err == nil {
		return ""
	}
	return i18n.Trans("Could not connect: {0}", err.Error())
}
