package front

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"
	"time"

	"github.com/helpbuoy/helpbuoy/proto/transport"
)

func TestClientAddr(t *testing.T) {
	trusted, err := ParsePrefixes("127.0.0.1, 10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		remote string
		xff    []string
		want   string
	}{
		{"198.51.100.7:4000", nil, "198.51.100.7"},
		{"198.51.100.7:4000", []string{"203.0.113.9"}, "198.51.100.7"}, // untrusted peer: header ignored
		{"127.0.0.1:4000", nil, "127.0.0.1"},
		{"127.0.0.1:4000", []string{"203.0.113.9"}, "203.0.113.9"},
		{"127.0.0.1:4000", []string{"1.1.1.1, 203.0.113.9, 10.1.2.3"}, "203.0.113.9"}, // spoofed first hop skipped
		{"127.0.0.1:4000", []string{"1.1.1.1", "203.0.113.9"}, "203.0.113.9"},
		{"127.0.0.1:4000", []string{"10.0.0.1, 10.0.0.2"}, "10.0.0.1"},
		{"127.0.0.1:4000", []string{"garbage"}, "127.0.0.1"},
		{"[::ffff:127.0.0.1]:4000", []string{"2001:db8::5"}, "2001:db8::5"},
	}
	for _, tt := range tests {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tt.remote
		for _, v := range tt.xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		got := ClientAddr(r, trusted).(*net.TCPAddr).IP.String()
		if got != tt.want {
			t.Errorf("%s %q: got %s, want %s", tt.remote, tt.xff, got, tt.want)
		}
	}
}

func TestParsePrefixes(t *testing.T) {
	p, err := ParsePrefixes("::1,192.0.2.0/24")
	if err != nil || len(p) != 2 || p[0].Bits() != 128 || p[1].Bits() != 24 {
		t.Fatalf("got %v, %v", p, err)
	}
	if _, err := ParsePrefixes("proxy.example.com"); err == nil {
		t.Fatal("host name accepted")
	}
	if p, err := ParsePrefixes(""); err != nil || len(p) != 0 {
		t.Fatalf("empty: %v, %v", p, err)
	}
}

func TestHostOf(t *testing.T) {
	for in, want := range map[string]string{
		"Acme.Relay.HelpBuoy.com":     "acme.relay.helpbuoy.com",
		"acme.relay.helpbuoy.com:443": "acme.relay.helpbuoy.com",
		"acme.relay.helpbuoy.com.":    "acme.relay.helpbuoy.com",
		"[::1]:5959":                  "::1",
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = in
		if got := HostOf(r); got != want {
			t.Errorf("HostOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestVirtualHosts routes two host names over one plain HTTP listener, as
// behind a reverse proxy.
func TestVirtualHosts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := New(ln.Addr(), DefaultTrustedProxies)
	srv := transport.NewHTTPServer(f)
	go srv.Serve(ln)
	defer srv.Close()

	echo := func(name string) {
		l := f.AddHost(name)
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				c.Write([]byte(name[:4]))
				c.Close()
			}
		}()
	}
	echo("acme.example.com")
	echo("beta.example.com")

	// A TLS-terminating reverse proxy in front, as in the default setup.
	target, _ := url.Parse("http://" + ln.Addr().String())
	proxy := httptest.NewTLSServer(&httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(target)
		r.Out.Host = r.In.Host
		r.SetXForwarded()
	}})
	defer proxy.Close()
	read := func(host string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, err := transport.Dial(ctx, host+":443", transport.DialOptions{
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, proxy.Listener.Addr().String())
			},
			Proxy: func(*http.Request) (*url.URL, error) { return nil, nil },
		})
		if err != nil {
			return "", err
		}
		defer c.Close()
		b, err := io.ReadAll(c)
		return string(b), err
	}
	for _, h := range []string{"acme.example.com", "beta.example.com"} {
		if got, err := read(h); err != nil || got != h[:4] {
			t.Errorf("%s: got %q, %v", h, got, err)
		}
	}
	if _, err := read("other.example.com"); err == nil {
		t.Error("unknown host accepted")
	}
	f.RemoveHost("beta.example.com")
	if _, err := read("beta.example.com"); err == nil {
		t.Error("removed host accepted")
	}

	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "acme.example.com"
	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Errorf("/ answered %d", rec.Code)
	}
}
