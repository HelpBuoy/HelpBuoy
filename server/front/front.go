// Package front is the HTTP side of helpbuoy-server: it accepts the apps'
// WebSocket connections (proto/transport), picks the virtual host from the
// Host header, takes the client address from X-Forwarded-For when the
// request comes from a trusted reverse proxy, and hands the connection to
// that host's SSH server through a net.Listener.
package front

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"

	"github.com/helpbuoy/helpbuoy/proto/transport"
)

// DefaultTrustedProxies are the proxies trusted when none are configured: a
// reverse proxy on the same machine.
var DefaultTrustedProxies = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.1/32"),
	netip.MustParsePrefix("::1/128"),
}

// Front routes WebSocket requests to the listeners of the virtual hosts.
type Front struct {
	addr    net.Addr
	trusted []netip.Prefix

	mu    sync.Mutex
	any   *transport.Listener // without virtual hosts: every host name
	hosts map[string]*transport.Listener
}

// New returns a Front whose listeners report addr. Requests from trusted
// proxies may name the client in X-Forwarded-For.
func New(addr net.Addr, trusted []netip.Prefix) *Front {
	return &Front{addr: addr, trusted: trusted, hosts: map[string]*transport.Listener{}}
}

// Listener returns the listener for every host name, for a server without
// virtual hosts.
func (f *Front) Listener() net.Listener {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.any == nil {
		f.any = transport.NewListener(f.addr)
	}
	return f.any
}

// AddHost returns the listener for one virtual host. name is a lower-case
// host name.
func (f *Front) AddHost(name string) net.Listener {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := transport.NewListener(f.addr)
	f.hosts[name] = l
	return l
}

// RemoveHost stops handing out connections for name and closes its
// listener.
func (f *Front) RemoveHost(name string) {
	f.mu.Lock()
	l := f.hosts[name]
	delete(f.hosts, name)
	f.mu.Unlock()
	if l != nil {
		l.Close()
	}
}

// HasHost reports whether name is a virtual host.
func (f *Front) HasHost(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hosts[name] != nil
}

// ServeHTTP answers the WebSocket upgrade on transport.Path and a short
// text on / (for health checks of a proxy or load balancer).
func (f *Front) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := HostOf(r)
	f.mu.Lock()
	l := f.any
	if l == nil {
		l = f.hosts[host]
	}
	f.mu.Unlock()
	if l == nil {
		http.Error(w, "no HelpBuoy server for "+host, http.StatusNotFound)
		return
	}
	switch r.URL.Path {
	case transport.Path:
		l.Hand(w, r, ClientAddr(r, f.trusted))
	case "/":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "HelpBuoy server. Enter this address in the HelpBuoy app.")
	default:
		http.NotFound(w, r)
	}
}

// HostOf returns the host name the client asked for: the Host header without
// port and trailing dot, in lower case.
func HostOf(r *http.Request) string {
	h := r.Host
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.TrimSuffix(strings.Trim(h, "[]"), ".")
	return strings.ToLower(h)
}

// ClientAddr returns the client's address: the peer of the HTTP connection,
// or, when that peer is a trusted proxy, the last address in
// X-Forwarded-For that is not a trusted proxy itself.
func ClientAddr(r *http.Request, trusted []netip.Prefix) net.Addr {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return transport.AddrOf(r.RemoteAddr)
	}
	if !isTrusted(peer.Addr(), trusted) {
		return net.TCPAddrFromAddrPort(peer)
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break // a damaged header: don't guess past it
		}
		a = a.Unmap()
		if !isTrusted(a, trusted) || i == 0 {
			return net.TCPAddrFromAddrPort(netip.AddrPortFrom(a, 0))
		}
	}
	return net.TCPAddrFromAddrPort(peer)
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	a = a.Unmap()
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ParsePrefixes parses a comma-separated list of IP addresses and CIDR
// prefixes, as given to --trusted-proxy.
func ParsePrefixes(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if p, err := netip.ParsePrefix(f); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(f)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: not an IP address or CIDR prefix", f)
		}
		out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
	}
	return out, nil
}
