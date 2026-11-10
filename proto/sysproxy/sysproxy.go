// Package sysproxy finds the HTTPS proxy the system is set up to use, for
// the apps' connection to the server (proto/transport). The HTTPS_PROXY and
// NO_PROXY environment variables come first; without them it reads the
// system settings: the Internet Options on Windows, the network settings on
// macOS (scutil --proxy) and the GNOME proxy settings on Linux.
//
// Automatic configuration (PAC files, WPAD) is not supported; only a proxy
// entered by hand is used.
package sysproxy

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Settings is a proxy and the hosts that bypass it.
type Settings struct {
	Proxy  string   // host:port, or "" for no proxy
	Bypass []string // host patterns: "*.example.com", "example.com", "10.*", "<local>", CIDRs
	// BypassSimple bypasses the proxy for host names without a dot.
	BypassSimple bool
}

// Func returns the proxy function for http.Transport: HTTPS_PROXY first,
// else the system settings, read anew for every connection so a change
// takes effect without a restart.
func Func() func(*http.Request) (*url.URL, error) {
	return func(r *http.Request) (*url.URL, error) {
		if u, err := http.ProxyFromEnvironment(r); u != nil || err != nil || envSet() {
			return u, err
		}
		return Lookup(System(), r.URL.Hostname())
	}
}

// Off is the proxy function for "don't use a proxy": always direct.
func Off(*http.Request) (*url.URL, error) { return nil, nil }

func envSet() bool {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy"} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

// Lookup returns the proxy URL for host under s, or nil to go direct.
func Lookup(s Settings, host string) (*url.URL, error) {
	if s.Proxy == "" || bypassed(s, host) {
		return nil, nil
	}
	return &url.URL{Scheme: "http", Host: s.Proxy}, nil
}

func bypassed(s Settings, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || isLoopback(host) {
		return true
	}
	simple := !strings.Contains(host, ".") && net.ParseIP(host) == nil
	if s.BypassSimple && simple {
		return true
	}
	for _, p := range s.Bypass {
		p = strings.ToLower(strings.TrimSpace(p))
		switch {
		case p == "":
		case p == "<local>":
			if simple {
				return true
			}
		case strings.Contains(p, "/"):
			if _, n, err := net.ParseCIDR(p); err == nil {
				if ip := net.ParseIP(host); ip != nil && n.Contains(ip) {
					return true
				}
			}
		default:
			if matchHost(p, host) {
				return true
			}
		}
	}
	return false
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// matchHost matches a bypass pattern with * wildcards. A pattern starting
// with a dot ("".example.com") also matches the domain's subdomains.
func matchHost(p, host string) bool {
	if strings.HasPrefix(p, ".") {
		return host == p[1:] || strings.HasSuffix(host, p)
	}
	return glob(p, host)
}

func glob(p, s string) bool {
	for {
		i := strings.IndexByte(p, '*')
		if i < 0 {
			return p == s
		}
		if !strings.HasPrefix(s, p[:i]) {
			return false
		}
		s, p = s[i:], p[i+1:]
		if p == "" {
			return true
		}
		for j := 0; j <= len(s); j++ {
			if glob(p, s[j:]) {
				return true
			}
		}
		return false
	}
}

// ParseWindows reads the Internet Options values: ProxyEnable, ProxyServer
// ("host:port", or per scheme as "http=h:p;https=h:p") and ProxyOverride
// (patterns separated by semicolons, "<local>" for names without a dot).
func ParseWindows(enable uint32, server, override string) Settings {
	if enable == 0 || strings.TrimSpace(server) == "" {
		return Settings{}
	}
	proxy := ""
	var fallback string
	for _, part := range strings.Split(server, ";") {
		part = strings.TrimSpace(part)
		scheme, addr, ok := strings.Cut(part, "=")
		switch {
		case !ok:
			proxy = part // one proxy for every scheme
		case strings.EqualFold(scheme, "https"):
			proxy = addr
		case strings.EqualFold(scheme, "http"):
			fallback = addr
		}
		if proxy != "" {
			break
		}
	}
	if proxy == "" {
		proxy = fallback
	}
	s := Settings{Proxy: hostPort(proxy, 80)}
	for _, p := range strings.Split(override, ";") {
		if p = strings.TrimSpace(p); p != "" {
			s.Bypass = append(s.Bypass, p)
		}
	}
	return s
}

// ParseScutil reads the output of "scutil --proxy" on macOS: the secure web
// proxy (HTTPSEnable, HTTPSProxy, HTTPSPort), ExceptionsList and
// ExcludeSimpleHostnames.
func ParseScutil(out string) Settings {
	vals := map[string]string{}
	var exceptions []string
	inExceptions := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "}" {
			inExceptions = false
			continue
		}
		k, v, ok := strings.Cut(line, " : ")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if inExceptions {
			exceptions = append(exceptions, v)
			continue
		}
		if k == "ExceptionsList" {
			inExceptions = strings.HasSuffix(v, "{")
			continue
		}
		vals[k] = v
	}
	if vals["HTTPSEnable"] != "1" || vals["HTTPSProxy"] == "" {
		return Settings{}
	}
	port, _ := strconv.Atoi(vals["HTTPSPort"])
	if port == 0 {
		port = 443
	}
	return Settings{
		Proxy:        net.JoinHostPort(vals["HTTPSProxy"], strconv.Itoa(port)),
		Bypass:       exceptions,
		BypassSimple: vals["ExcludeSimpleHostnames"] == "1",
	}
}

// ParseGsettings reads the GNOME proxy settings, as printed by gsettings:
// mode ('none', 'manual', 'auto'), the https host and port, and ignore-hosts
// (['localhost', '127.0.0.0/8']).
func ParseGsettings(mode, host, port, ignore string) Settings {
	unq := func(s string) string { return strings.Trim(strings.TrimSpace(s), "'\"") }
	if unq(mode) != "manual" || unq(host) == "" {
		return Settings{}
	}
	p, _ := strconv.Atoi(strings.TrimSpace(port))
	if p == 0 {
		p = 8080
	}
	s := Settings{Proxy: net.JoinHostPort(unq(host), strconv.Itoa(p))}
	ignore = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ignore), "@as"))
	ignore = strings.TrimSuffix(strings.TrimPrefix(ignore, "["), "]")
	for _, h := range strings.Split(ignore, ",") {
		if h = unq(h); h != "" {
			s.Bypass = append(s.Bypass, h)
		}
	}
	return s
}

// hostPort adds defPort to an address without one, and drops a scheme.
func hostPort(s string, defPort int) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	s = strings.TrimSuffix(s, "/")
	if s == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(s); err == nil {
		return s
	}
	return net.JoinHostPort(strings.Trim(s, "[]"), strconv.Itoa(defPort))
}
