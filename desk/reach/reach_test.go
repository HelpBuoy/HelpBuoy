package reach

import (
	"errors"
	"net"
	"net/netip"
	"testing"
)

func ips(s ...string) []netip.Addr {
	var out []netip.Addr
	for _, x := range s {
		out = append(out, netip.MustParseAddr(x))
	}
	return out
}

const up = net.FlagUp | net.FlagBroadcast | net.FlagMulticast

func TestPickLAN(t *testing.T) {
	lo := Iface{Name: "lo", Flags: net.FlagUp | net.FlagLoopback, Addrs: ips("127.0.0.1", "::1")}
	docker := Iface{Name: "docker0", Flags: up, Addrs: ips("172.17.0.1")}
	wifi := Iface{Name: "wlp2s0", Flags: up, Addrs: ips("fe80::1", "192.168.1.20", "2001:db8::20")}
	eth := Iface{Name: "enp3s0", Flags: up, Addrs: ips("10.0.0.5")}
	for _, tc := range []struct {
		name   string
		route  string
		ifaces []Iface
		want   string
	}{
		{"default route wins", "10.0.0.5", []Iface{lo, docker, wifi, eth}, "10.0.0.5"},
		{"route on a skipped interface", "172.17.0.1", []Iface{lo, docker, wifi}, "192.168.1.20"},
		{"no route: first private", "", []Iface{lo, docker, wifi, eth}, "192.168.1.20"},
		{"only loopback", "", []Iface{lo}, ""},
		{"down", "", []Iface{{Name: "eth0", Flags: net.FlagBroadcast, Addrs: ips("192.168.1.2")}}, ""},
		{"link-local only", "", []Iface{{Name: "eth0", Flags: up, Addrs: ips("169.254.10.1", "fe80::2")}}, ""},
		{"IPv6 only", "", []Iface{{Name: "eth0", Flags: up, Addrs: ips("2001:db8::5")}}, ""},
		{"VPN tunnel is point-to-point", "10.8.0.2", []Iface{{Name: "office", Flags: up | net.FlagPointToPoint, Addrs: ips("10.8.0.2")}, wifi}, "192.168.1.20"},
		{"full-tunnel WireGuard", "10.66.0.2", []Iface{{Name: "wg0", Flags: up, Addrs: ips("10.66.0.2")}, wifi}, "192.168.1.20"},
		{"Tailscale", "", []Iface{{Name: "tailscale0", Flags: up, Addrs: ips("100.101.102.103")}, wifi}, "192.168.1.20"},
		{"Windows Hyper-V switch", "", []Iface{{Name: "vEthernet (WSL)", Flags: up, Addrs: ips("172.28.0.1")}, {Name: "Wi-Fi", Flags: up, Addrs: ips("192.168.178.30")}}, "192.168.178.30"},
		{"VirtualBox host-only", "", []Iface{{Name: "VirtualBox Host-Only Network", Flags: up, Addrs: ips("192.168.56.1")}, {Name: "Ethernet", Flags: up, Addrs: ips("192.168.0.12")}}, "192.168.0.12"},
		{"private before public", "", []Iface{{Name: "eth1", Flags: up, Addrs: ips("198.51.100.4")}, eth}, "10.0.0.5"},
		{"public on the interface", "198.51.100.4", []Iface{{Name: "eth0", Flags: up, Addrs: ips("198.51.100.4")}}, "198.51.100.4"},
		{"carrier-grade NAT last", "", []Iface{{Name: "usb0", Flags: up, Addrs: ips("100.70.1.2")}, {Name: "eth0", Flags: up, Addrs: ips("198.51.100.4")}}, "198.51.100.4"},
		{"IPv4-mapped", "::ffff:192.168.1.20", []Iface{eth, {Name: "eth1", Flags: up, Addrs: ips("::ffff:192.168.1.20")}}, "192.168.1.20"},
	} {
		var route netip.Addr
		if tc.route != "" {
			route = netip.MustParseAddr(tc.route)
		}
		got := pickLAN(route, tc.ifaces)
		if (tc.want == "" && got.IsValid()) || (tc.want != "" && got.String() != tc.want) {
			t.Errorf("%s: got %v, want %q", tc.name, got, tc.want)
		}
	}
}

func TestIsVirtual(t *testing.T) {
	for name, want := range map[string]bool{
		"eth0": false, "enp3s0": false, "wlan0": false, "en0": false, "br0": false,
		"Ethernet": false, "Wi-Fi": false, "WLAN": false,
		"docker0": true, "br-1a2b3c": true, "veth12ab": true, "virbr0": true, "vmnet8": true,
		"tun0": true, "wg0": true, "utun3": true, "bridge100": true, "zt7nnig26": true,
		"vEthernet (Default Switch)": true, "VMware Network Adapter VMnet1": true, "OpenVPN TAP-Windows6": true,
	} {
		if got := isVirtual(name); got != want {
			t.Errorf("isVirtual(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestIsCGNAT(t *testing.T) {
	for s, want := range map[string]bool{
		"100.64.0.1": true, "100.127.255.254": true, "100.63.255.255": false, "100.128.0.0": false,
		"192.168.1.1": false, "::ffff:100.100.1.1": true,
	} {
		if got := IsCGNAT(netip.MustParseAddr(s)); got != want {
			t.Errorf("IsCGNAT(%s) = %v", s, got)
		}
	}
}

// TestLANAddr: the chosen address reaches a listener on all interfaces, as
// the local server listens.
func TestLANAddr(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	ap, err := LANAddr(port)
	if errors.Is(err, ErrNoLAN) {
		t.Skip("no local network on this machine")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("LAN address %v", ap)
	if !ap.Addr().Is4() || ap.Addr().IsLoopback() || int(ap.Port()) != port {
		t.Fatalf("LANAddr = %v", ap)
	}

	// Nothing listens on a closed port.
	ln2, _ := net.Listen("tcp", ":0")
	closed := ln2.Addr().(*net.TCPAddr).Port
	ln2.Close()
	if _, err := LANAddr(closed); err == nil {
		t.Fatal("want an error for a port nothing listens on")
	}
}
