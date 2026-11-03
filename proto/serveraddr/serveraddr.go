// Package serveraddr parses and normalizes the server address that users type
// on first start, in the form hostname:port.
package serveraddr

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// DefaultPort is used when the user leaves the port out.
const DefaultPort = 5959

// ErrEmpty is returned for an empty address.
var ErrEmpty = errors.New("server address is empty")

// Addr is a parsed server address.
type Addr struct {
	Host string // lower-cased host name or IP literal, without brackets
	Port int
}

// String returns the normalized "host:port" form, with brackets for IPv6.
func (a Addr) String() string {
	return net.JoinHostPort(a.Host, strconv.Itoa(a.Port))
}

// Parse accepts "host", "host:port", "[v6]", "[v6]:port" and a bare IPv6
// literal. Surrounding whitespace is ignored. The port defaults to DefaultPort.
func Parse(s string) (Addr, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Addr{}, ErrEmpty
	}
	if strings.ContainsAny(s, " \t\r\n/\\@?#") {
		return Addr{}, fmt.Errorf("server address %q: use hostname:port, without spaces, scheme or path", s)
	}

	if strings.HasSuffix(s, ":") {
		return Addr{}, fmt.Errorf("server address %q: port is empty", s)
	}

	host, portStr := s, ""
	switch {
	case strings.HasPrefix(s, "["):
		end := strings.Index(s, "]")
		if end < 0 {
			return Addr{}, fmt.Errorf("server address %q: missing ]", s)
		}
		host = s[1:end]
		rest := s[end+1:]
		if rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return Addr{}, fmt.Errorf("server address %q: unexpected text after ]", s)
			}
			portStr = rest[1:]
		}
		if ip := net.ParseIP(host); ip == nil || ip.To4() != nil {
			return Addr{}, fmt.Errorf("server address %q: brackets are only for IPv6 addresses", s)
		}
	case strings.Count(s, ":") > 1:
		// Bare IPv6 literal without port.
		if net.ParseIP(s) == nil {
			return Addr{}, fmt.Errorf("server address %q: put IPv6 addresses in brackets, like [2001:db8::1]:%d", s, DefaultPort)
		}
	case strings.Contains(s, ":"):
		i := strings.LastIndex(s, ":")
		host, portStr = s[:i], s[i+1:]
	}

	if host == "" {
		return Addr{}, fmt.Errorf("server address %q: missing host name", s)
	}
	if net.ParseIP(host) == nil && !validHostname(host) {
		return Addr{}, fmt.Errorf("server address %q: %q is not a valid host name", s, host)
	}

	port := DefaultPort
	if portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err != nil || p < 1 || p > 65535 {
			return Addr{}, fmt.Errorf("server address %q: port must be a number from 1 to 65535", s)
		}
		port = p
	}

	return Addr{Host: strings.ToLower(host), Port: port}, nil
}

// validHostname checks RFC 1123 host name syntax. A trailing dot is allowed.
func validHostname(h string) bool {
	h = strings.TrimSuffix(h, ".")
	if h == "" || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			ok := c == '-' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
			if !ok {
				return false
			}
		}
	}
	return true
}
