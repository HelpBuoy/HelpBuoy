package session

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"syscall"
)

// ErrHostKeyChanged is returned when the relay presents a host key other than
// the pinned one. It is a hard error: the client stops connecting.
var ErrHostKeyChanged = errors.New("relay identity changed")

// ErrNotRelay is returned when the other end is not a help-me-win relay.
var ErrNotRelay = errors.New("not a help-me-win relay")

// ErrRefusedKey is returned when the relay refuses the app key.
var ErrRefusedKey = errors.New("the relay refused this key")

// ErrHelperKey is returned when the key belongs to a helper on this relay.
var ErrHelperKey = errors.New("this key is a helper key on this relay")

// RelayError is a message the relay sent with control.Error before closing.
type RelayError struct{ Message string }

func (e *RelayError) Error() string { return "relay: " + e.Message }

// Kind classifies connection errors for the first-run window.
type Kind int

// Error kinds.
const (
	KindOther Kind = iota
	KindNameNotFound
	KindRefused
	KindTimeout
	KindNotRelay
	KindRefusedKey
	KindHostKeyChanged
	KindHelperKey
	KindRelay
)

// Classify returns the kind of a connection error.
func Classify(err error) Kind {
	var dnsErr *net.DNSError
	var relayErr *RelayError
	var errno syscall.Errno
	var netErr net.Error
	switch {
	case err == nil:
		return KindOther
	case errors.Is(err, ErrHostKeyChanged):
		return KindHostKeyChanged
	case errors.Is(err, ErrHelperKey):
		return KindHelperKey
	case errors.Is(err, ErrRefusedKey):
		return KindRefusedKey
	case errors.As(err, &relayErr):
		return KindRelay
	case errors.Is(err, ErrNotRelay):
		return KindNotRelay
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
		return "This name was not found. Check the relay address."
	case KindRefused:
		return "Nothing is answering at this address. Check the address and the port, and whether the relay is running."
	case KindTimeout:
		return "The relay did not answer in time. Check the address and your internet connection."
	case KindNotRelay:
		return "This address does not belong to a help-me-win relay."
	case KindRefusedKey:
		return "The relay refused this PC for now. Try again later."
	case KindHostKeyChanged:
		return "The relay's identity changed. Ask your helper before you continue; to connect anyway, use Reset in the settings."
	case KindHelperKey:
		return "This key belongs to a helper on this relay and can't be used here."
	case KindRelay:
		var re *RelayError
		errors.As(err, &re)
		return "The relay closed the connection: " + strings.TrimSuffix(re.Message, ".") + "."
	}
	if err == nil {
		return ""
	}
	return "Could not connect: " + err.Error()
}
