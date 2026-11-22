// Package hostprobe looks up the host key of a HelpBuoy server without
// logging in. A helpdesk uses it to remember another server it allows for
// its computers by the key that server shows at that moment, trust on first
// use (PLAN.md 12.3).
package hostprobe

import (
	"context"
	"errors"
	"net"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/proto/transport"
)

// errSeen ends the handshake once the host key is known.
var errSeen = errors.New("host key seen")

// Probe connects to the server at addr only far enough to see its host key:
// it ends the SSH handshake right after the server showed its key, before
// authenticating, so nothing else happens with that server. A zero timeout
// is 10 seconds.
func Probe(ctx context.Context, addr string, timeout time.Duration, o transport.DialOptions) (ssh.PublicKey, error) {
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	nc, err := transport.Dial(ctx, addr, o)
	if err != nil {
		return nil, err
	}
	defer nc.Close()
	stop := context.AfterFunc(ctx, func() { nc.Close() })
	defer stop()
	nc.SetDeadline(time.Now().Add(timeout))
	var key ssh.PublicKey
	cfg := &ssh.ClientConfig{
		User: control.SSHUser,
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
			key = k
			return errSeen
		},
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		Timeout:           timeout,
	}
	_, _, _, err = ssh.NewClientConn(nc, addr, cfg)
	if key != nil {
		return key, nil
	}
	if err == nil {
		err = errors.New("the server showed no host key")
	}
	return nil, err
}
