// Package reach finds the address in the local network of the server that
// runs inside the desk app (PLAN.md 7.1): the address a port forward on the
// router points to, and that others on the same network can type. It
// contacts nobody.
package reach

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"
)

// ErrNoLAN is returned by LANAddr when this computer has no IPv4 address
// in a local network.
var ErrNoLAN = errors.New("no IPv4 address in a local network")

// cgnat is the shared address space of carrier-grade NAT (RFC 6598).
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// IsCGNAT reports whether ip is in the shared address space that internet
// providers use behind carrier-grade NAT, where a port forward on the
// customer's router can't work.
func IsCGNAT(ip netip.Addr) bool { return cgnat.Contains(ip.Unmap()) }

// Iface is a network interface as pickLAN sees it.
type Iface struct {
	Name  string
	Flags net.Flags
	Addrs []netip.Addr
}

// virtualPrefixes start the names of interfaces that are no way into the
// local network: container and VM bridges and VPN tunnels (Linux, BSD and
// macOS names).
var virtualPrefixes = []string{
	"docker", "br-", "veth", "virbr", "vmnet", "vboxnet", "lxcbr", "lxdbr", "cni",
	"flannel", "kube", "podman", "tun", "tap", "wg", "utun", "tailscale", "zt",
	"bridge", "ipsec", "ppp", "awdl", "llw", "anpi",
}

// virtualWords appear in the names of such interfaces on Windows, which uses
// friendly names such as "vEthernet (WSL)" or "VirtualBox Host-Only Network".
var virtualWords = []string{
	"vethernet", "virtualbox", "vmware", "hyper-v", "tailscale", "zerotier",
	"wireguard", "openvpn", "nordlynx", "vpn", "docker", "loopback",
}

// isVirtual reports whether an interface name looks like a container or VM
// bridge or a VPN tunnel.
func isVirtual(name string) bool {
	n := strings.ToLower(name)
	for _, p := range virtualPrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	for _, w := range virtualWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// rank orders candidate addresses: a private network address first, then a
// public address on the interface itself, then carrier-grade NAT.
func rank(ip netip.Addr) int {
	switch {
	case ip.IsPrivate():
		return 2
	case IsCGNAT(ip):
		return 0
	}
	return 1
}

// pickLAN chooses the IPv4 address in the local network that others would
// connect to: on an interface that is up, not a loopback, tunnel, container
// or VM interface, and not link-local. The address of the default route
// (route) wins when it is one of them; otherwise the best ranked address in
// interface order. It returns the zero Addr when there is none.
func pickLAN(route netip.Addr, ifaces []Iface) netip.Addr {
	route = route.Unmap()
	var best netip.Addr
	bestRank := -1
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 || isVirtual(ifc.Name) {
			continue
		}
		for _, ip := range ifc.Addrs {
			ip = ip.Unmap()
			if !ip.Is4() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
				continue
			}
			if ip == route {
				return ip
			}
			if r := rank(ip); r > bestRank {
				best, bestRank = ip, r
			}
		}
	}
	return best
}

// DefaultRouteIP returns this computer's IPv4 address on the default route,
// or the zero Addr when there is no IPv4 route to the internet. It "dials" a
// public address over UDP, which only picks the route and sends nothing.
func DefaultRouteIP() netip.Addr {
	c, err := net.Dial("udp4", "1.1.1.1:53")
	if err != nil {
		return netip.Addr{}
	}
	defer c.Close()
	ua, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok {
		return netip.Addr{}
	}
	ip, _ := netip.AddrFromSlice(ua.IP)
	ip = ip.Unmap()
	if !ip.Is4() || ip.IsUnspecified() || ip.IsLoopback() {
		return netip.Addr{}
	}
	return ip
}

// systemIfaces lists this computer's interfaces with their addresses.
func systemIfaces() []Iface {
	list, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []Iface
	for _, ni := range list {
		addrs, err := ni.Addrs()
		if err != nil {
			continue
		}
		ifc := Iface{Name: ni.Name, Flags: ni.Flags}
		for _, a := range addrs {
			if pn, ok := a.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(pn.IP); ok {
					ifc.Addrs = append(ifc.Addrs, ip.Unmap())
				}
			}
		}
		out = append(out, ifc)
	}
	return out
}

// LANAddr returns the address in the local network that others reach a
// server on this computer's port at, such as 192.168.1.20:5959. It checks
// that something accepts connections there, so the address it returns
// works.
func LANAddr(port int) (netip.AddrPort, error) {
	ip := pickLAN(DefaultRouteIP(), systemIfaces())
	if !ip.IsValid() {
		return netip.AddrPort{}, ErrNoLAN
	}
	ap := netip.AddrPortFrom(ip, uint16(port))
	c, err := net.DialTimeout("tcp", ap.String(), 2*time.Second)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("the server does not answer on %s: %w", ap, err)
	}
	c.Close()
	return ap, nil
}
