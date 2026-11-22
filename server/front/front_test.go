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

// TestVirtualHosts routes two names over one plain HTTP listener, as behind
// a reverse proxy.
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
	echo("acme")
	echo("beta")

	// A TLS-terminating reverse proxy in front, as in the default setup.
	target, _ := url.Parse("http://" + ln.Addr().String())
	proxy := httptest.NewTLSServer(&httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(target)
		r.Out.Host = r.In.Host
		r.SetXForwarded()
	}})
	defer proxy.Close()
	read := func(addr string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, err := transport.Dial(ctx, addr, transport.DialOptions{
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
	for _, name := range []string{"acme", "beta"} {
		// The host name plays no part in picking the virtual host.
		for _, host := range []string{"relay.example.com", name + ".example.com"} {
			if got, err := read(host + ":443/" + name); err != nil || got != name[:4] {
				t.Errorf("%s/%s: got %q, %v", host, name, got, err)
			}
		}
	}
	for _, addr := range []string{"relay.example.com:443/other", "relay.example.com:443", "acme.example.com:443"} {
		if _, err := read(addr); err == nil {
			t.Errorf("%s accepted", addr)
		}
	}
	f.RemoveHost("beta")
	if _, err := read("relay.example.com:443/beta"); err == nil {
		t.Error("removed virtual host accepted")
	}

	for path, want := range map[string]int{
		"/":               http.StatusOK,
		"/helpbuoy":       http.StatusNotFound,
		"/helpbuoy/":      http.StatusNotFound,
		"/helpbuoy/other": http.StatusNotFound,
		"/helpbuoyacme":   http.StatusNotFound,
		"/acme":           http.StatusNotFound,
	} {
		rec := httptest.NewRecorder()
		f.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != want {
			t.Errorf("%s answered %d, want %d", path, rec.Code, want)
		}
	}
}

// TestNoVirtualHosts: a server without virtual hosts has no names.
func TestNoVirtualHosts(t *testing.T) {
	f := New(transport.AddrOf("127.0.0.1:1"), nil)
	f.Listener()
	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, httptest.NewRequest("GET", "/helpbuoy/acme", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("/helpbuoy/acme answered %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	f.ServeHTTP(rec, httptest.NewRequest("GET", "/helpbuoy", nil))
	if rec.Code == http.StatusNotFound {
		t.Error("/helpbuoy not found")
	}
}
