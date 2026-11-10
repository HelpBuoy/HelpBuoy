// Package transport carries the SSH connection between the apps and
// helpbuoy-server over a WebSocket over HTTPS: the apps open
// wss://host:port/helpbuoy and run SSH inside it, one binary WebSocket
// message per write.
//
// A server can also run in ssh mode: plain SSH without the WebSocket and TLS
// (direct connect). Dial tries HTTPS first and falls back to direct connect
// when the port answers with an SSH banner.
//
// The TLS certificate is not used to identify the server. The SSH host key
// inside stays the trust anchor, pinned on first use, so a self-signed
// certificate, a reverse proxy's certificate or a TLS-inspecting proxy that
// re-signs traffic all work, and none of them can pose as the server without
// its host key. TLS is there to get through networks that only let web
// traffic out.
package transport

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Path is the URL path the server accepts the WebSocket on.
const Path = "/helpbuoy"

// Subprotocol is the WebSocket subprotocol both ends require, so that an
// unrelated WebSocket service is recognized as not being a HelpBuoy server.
const Subprotocol = "helpbuoy-ssh"

// sshPrefix starts the version line an SSH server sends first.
var sshPrefix = []byte("SSH-")

// ErrNotServer is returned by Dial when the address answers HTTPS but is not
// a HelpBuoy server, or no server for that host name runs there.
var ErrNotServer = errors.New("not a HelpBuoy server")

// DialOptions tune Dial. The zero value is what the apps use.
type DialOptions struct {
	// Dial opens the TCP connection to the server or proxy; nil uses a
	// net.Dialer.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Proxy picks the HTTP proxy for a request; nil uses the HTTPS_PROXY and
	// NO_PROXY environment variables.
	Proxy func(*http.Request) (*url.URL, error)
}

// URL returns the WebSocket URL for a "host:port" address.
func URL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return "", fmt.Errorf("server address %q: bad port", addr)
	}
	u := url.URL{Scheme: "wss", Host: net.JoinHostPort(host, port), Path: Path}
	if port == "443" {
		u.Host = host
		if net.ParseIP(host) != nil && net.ParseIP(host).To4() == nil {
			u.Host = "[" + host + "]"
		}
	}
	return u.String(), nil
}

// Dial connects to the server at addr ("host:port") and returns the
// connection to run SSH over. ctx bounds the connect, the TLS handshake and
// the WebSocket upgrade, not the life of the returned connection.
func Dial(ctx context.Context, addr string, o DialOptions) (net.Conn, error) {
	u, err := URL(addr)
	if err != nil {
		return nil, err
	}
	host, _, _ := net.SplitHostPort(addr)
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
		// The certificate is not the server's identity: the pinned SSH host
		// key is (see the package comment). Checking it would only break
		// self-signed servers and TLS-inspecting proxies.
		InsecureSkipVerify: true, //nolint:gosec
	}
	if net.ParseIP(host) == nil {
		tlsCfg.ServerName = host // SNI picks the virtual host and certificate
	}
	tr := &http.Transport{
		Proxy:             o.Proxy,
		DialContext:       o.Dial,
		TLSClientConfig:   tlsCfg,
		ForceAttemptHTTP2: false,
	}
	if tr.Proxy == nil {
		tr.Proxy = http.ProxyFromEnvironment
	}
	if tr.DialContext == nil {
		tr.DialContext = (&net.Dialer{}).DialContext
	}
	defer tr.CloseIdleConnections()
	c, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{
		HTTPClient:      &http.Client{Transport: tr},
		Subprotocols:    []string{Subprotocol},
		CompressionMode: websocket.CompressionDisabled, // SSH traffic is encrypted
	})
	if err != nil {
		var rec tls.RecordHeaderError
		var alert tls.AlertError
		switch {
		case ctx.Err() != nil:
			return nil, err
		case resp != nil:
			// An HTTP server answered: it is no SSH server either.
			return nil, fmt.Errorf("%w: %s answered %s", ErrNotServer, addr, resp.Status)
		case strings.Contains(err.Error(), "server gave HTTP response to HTTPS client"):
			// Plain HTTP, such as a helpbuoy-server in http mode, meant to
			// sit behind a reverse proxy.
			return nil, fmt.Errorf("%w: %s does not speak HTTPS", ErrNotServer, addr)
		}
		// No HTTPS here: try plain SSH (a server in ssh mode), and keep it
		// only when the port answers with an SSH banner.
		if nc, derr := dialDirect(ctx, addr, o); derr == nil {
			return nc, nil
		}
		switch {
		case errors.As(err, &rec):
			return nil, fmt.Errorf("%w: %s does not speak HTTPS or SSH", ErrNotServer, addr)
		case errors.As(err, &alert):
			return nil, fmt.Errorf("%w: %s refused the TLS handshake: %v", ErrNotServer, addr, err)
		}
		return nil, err
	}
	if c.Subprotocol() != Subprotocol {
		c.Close(websocket.StatusProtocolError, "")
		return nil, fmt.Errorf("%w: %s speaks another WebSocket protocol", ErrNotServer, addr)
	}
	return websocket.NetConn(context.Background(), c, websocket.MessageBinary), nil
}

// bannerWait is how long dialDirect waits for the SSH banner; SSH servers
// send it right away.
const bannerWait = 3 * time.Second

// dialDirect opens a plain TCP connection for SSH without the WebSocket
// (direct connect, a server in ssh mode) and checks that the server sends an
// SSH banner. It bypasses any HTTP proxy.
func dialDirect(ctx context.Context, addr string, o DialOptions) (net.Conn, error) {
	dial := o.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	c, err := dial(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(bannerWait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	c.SetReadDeadline(deadline)
	br := bufio.NewReaderSize(c, 64)
	head, err := br.Peek(len(sshPrefix))
	c.SetReadDeadline(time.Time{})
	if err != nil || !bytes.Equal(head, sshPrefix) {
		c.Close()
		return nil, errors.New("no SSH banner")
	}
	return &peekedConn{Conn: c, r: br}, nil
}

// peekedConn reads the bytes dialDirect looked at before the rest.
type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// Upgrade accepts the WebSocket request r on the server side and returns the
// connection to run SSH over. remote is the client's address as the server
// should see it (after any trusted reverse proxy); nil uses r.RemoteAddr.
// The caller writes the HTTP error itself when Upgrade fails.
func Upgrade(w http.ResponseWriter, r *http.Request, remote net.Addr) (net.Conn, error) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:    []string{Subprotocol},
		CompressionMode: websocket.CompressionDisabled,
		// The apps send no Origin; a browser page can't open this anyway
		// without the SSH keys.
		InsecureSkipVerify: true,
	})
	if err != nil {
		return nil, err
	}
	if c.Subprotocol() != Subprotocol {
		c.Close(websocket.StatusPolicyViolation, "expected subprotocol "+Subprotocol)
		return nil, errors.New("client did not ask for subprotocol " + Subprotocol)
	}
	nc := websocket.NetConn(context.Background(), c, websocket.MessageBinary)
	if remote == nil {
		remote = AddrOf(r.RemoteAddr)
	}
	return &addrConn{Conn: nc, remote: remote}, nil
}

// AddrOf turns an "ip:port" string, as in http.Request.RemoteAddr, into a
// net.Addr.
func AddrOf(s string) net.Addr {
	if ap, err := netAddrPort(s); err == nil {
		return ap
	}
	return stringAddr(s)
}

func netAddrPort(s string) (*net.TCPAddr, error) {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil, fmt.Errorf("%q is not an IP address", host)
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		return nil, err
	}
	return &net.TCPAddr{IP: ip, Port: p}, nil
}

type stringAddr string

func (a stringAddr) Network() string { return "tcp" }
func (a stringAddr) String() string  { return string(a) }

// addrConn reports the client address the server should use.
type addrConn struct {
	net.Conn
	remote net.Addr
}

func (c *addrConn) RemoteAddr() net.Addr { return c.remote }

// Listener is an http.Handler that upgrades every request on Path to a
// WebSocket and hands it out through Accept, so a server that serves a
// net.Listener can serve WebSocket connections unchanged.
type Listener struct {
	addr  net.Addr
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

// NewListener returns a Listener that reports addr as its address.
func NewListener(addr net.Addr) *Listener {
	return &Listener{addr: addr, conns: make(chan net.Conn), done: make(chan struct{})}
}

// ServeHTTP upgrades r and queues the connection for Accept.
func (l *Listener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != Path {
		http.NotFound(w, r)
		return
	}
	l.Hand(w, r, nil)
}

// Hand upgrades r and queues the connection for Accept, with remote as the
// client address (nil: r.RemoteAddr). It answers 503 once l is closed.
func (l *Listener) Hand(w http.ResponseWriter, r *http.Request, remote net.Addr) {
	select {
	case <-l.done:
		http.Error(w, "server stopped", http.StatusServiceUnavailable)
		return
	default:
	}
	nc, err := Upgrade(w, r, remote)
	if err != nil {
		return // websocket.Accept already answered
	}
	select {
	case l.conns <- nc:
	case <-l.done:
		nc.Close()
	}
}

// Accept waits for the next connection.
func (l *Listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

// Close makes Accept return net.ErrClosed. Connections already handed out
// stay open.
func (l *Listener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

// Addr returns the address given to NewListener.
func (l *Listener) Addr() net.Addr { return l.addr }

// NewHTTPServer returns an http.Server for h with the limits for the
// HTTP layer, which apply before the SSH limits: 10 s to send the request
// headers, 16 KiB of headers at most.
func NewHTTPServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
}

// ServeTLS serves l (as a net.Listener of WebSocket connections) over HTTPS
// on ln with a new self-signed certificate, until ln is closed. The desk
// app's local server, the fakes and tests use it.
func ServeTLS(ln net.Listener, l *Listener) error {
	cert, err := SelfSigned()
	if err != nil {
		return err
	}
	srv := NewHTTPServer(l)
	srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}}
	return srv.ServeTLS(ln, "", "")
}

// Listen listens on addr ("host:port") and serves WebSocket connections over
// HTTPS with a self-signed certificate. It returns the net.Listener to hand
// to the server; closing it also stops the HTTPS listener.
func Listen(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	l := NewListener(ln.Addr())
	go func() {
		ServeTLS(ln, l)
		l.Close()
	}()
	return &tcpOwner{Listener: l, tcp: ln}, nil
}

type tcpOwner struct {
	*Listener
	tcp net.Listener
}

func (o *tcpOwner) Close() error {
	o.Listener.Close()
	return o.tcp.Close()
}
