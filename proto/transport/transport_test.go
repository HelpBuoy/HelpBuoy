package transport

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestURL(t *testing.T) {
	for in, want := range map[string]string{
		"helpme.example.com:443":  "wss://helpme.example.com/helpbuoy",
		"helpme.example.com:5959": "wss://helpme.example.com:5959/helpbuoy",
		"192.0.2.1:443":           "wss://192.0.2.1/helpbuoy",
		"[2001:db8::1]:443":       "wss://[2001:db8::1]/helpbuoy",
		"[2001:db8::1]:8443":      "wss://[2001:db8::1]:8443/helpbuoy",
	} {
		got, err := URL(in)
		if err != nil || got != want {
			t.Errorf("URL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := URL("nohost"); err == nil {
		t.Error("URL without port: no error")
	}
}

func TestDialRoundTrip(t *testing.T) {
	ln, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- err.Error()
			return
		}
		defer c.Close()
		buf := make([]byte, 5)
		if _, err := io.ReadFull(c, buf); err != nil {
			got <- err.Error()
			return
		}
		c.Write([]byte("pong:" + string(buf)))
		got <- c.RemoteAddr().String()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, ln.Addr().String(), DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("hello"))
	buf := make([]byte, 10)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "pong:hello" {
		t.Fatalf("read %q, %v", buf, err)
	}
	if remote := <-got; remote != c.LocalAddr().String() && net.ParseIP(mustHost(t, remote)) == nil {
		t.Errorf("server saw remote %q", remote)
	}
}

func mustHost(t *testing.T, s string) string {
	h, _, err := net.SplitHostPort(s)
	if err != nil {
		t.Fatalf("remote %q: %v", s, err)
	}
	return h
}

func TestDialNotServer(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Dial(ctx, ts.Listener.Addr().String(), DialOptions{})
	if !errors.Is(err, ErrNotServer) {
		t.Fatalf("err = %v, want ErrNotServer", err)
	}
}

func TestListenerClosed(t *testing.T) {
	l := NewListener(AddrOf("127.0.0.1:1"))
	l.Close()
	if _, err := l.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept after Close: %v", err)
	}
	rec := httptest.NewRecorder()
	l.ServeHTTP(rec, httptest.NewRequest("GET", Path, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d after Close", rec.Code)
	}
}

func TestLoadOrCreateSelfSigned(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadOrCreateSelfSigned(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreateSelfSigned(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(a.Certificate[0]) != string(b.Certificate[0]) {
		t.Fatal("certificate changed between loads")
	}
}

func TestDialPlainHTTP(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := Dial(ctx, ts.Listener.Addr().String(), DialOptions{}); !errors.Is(err, ErrNotServer) {
		t.Fatalf("plain HTTP: err = %v, want ErrNotServer", err)
	}
}

// TestDirectConnect: Dial falls back to plain SSH when the port answers
// with an SSH banner (a server in ssh mode).
func TestDirectConnect(t *testing.T) {
	// It sends its banner first, as SSH servers do.
	sshOnly, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sshOnly.Close()
	go func() {
		for {
			c, err := sshOnly.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.Write([]byte("SSH-2.0-test\r\n"))
				io.Copy(io.Discard, c)
			}()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, sshOnly.Addr().String(), DialOptions{})
	if err != nil {
		t.Fatalf("fallback: %v", err)
	}
	defer c.Close()
	line := make([]byte, 14)
	if _, err := io.ReadFull(c, line); err != nil || string(line) != "SSH-2.0-test\r\n" {
		t.Fatalf("fallback read %q, %v", line, err)
	}
}
