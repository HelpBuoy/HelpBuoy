package inner

import (
	"io"
	"net"
	"time"
)

// rwConn adapts an io.ReadWriteCloser (an ssh.Channel from the relay) to the
// net.Conn that ssh.NewServerConn and ssh.NewClientConn expect. Deadlines are
// not supported and are silently ignored; timeouts are handled by closing.
type rwConn struct {
	io.ReadWriteCloser
}

func (c rwConn) LocalAddr() net.Addr                { return dummyAddr{} }
func (c rwConn) RemoteAddr() net.Addr               { return dummyAddr{} }
func (c rwConn) SetDeadline(t time.Time) error      { return nil }
func (c rwConn) SetReadDeadline(t time.Time) error  { return nil }
func (c rwConn) SetWriteDeadline(t time.Time) error { return nil }

type dummyAddr struct{}

func (dummyAddr) Network() string { return "hmw-session" }
func (dummyAddr) String() string  { return "hmw-session" }
