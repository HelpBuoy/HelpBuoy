package main

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/help-me-win/proto/control"
	"github.com/mevdschee/help-me-win/relay/fake"
	"github.com/mevdschee/help-me-win/relay/server"
	"github.com/mevdschee/help-me-win/relay/store"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestFakeClientEndToEnd(t *testing.T) {
	dir := t.TempDir()
	hk := fake.NewKey()
	st, _ := store.Open(dir)
	st.Update(func(s *store.State) error { return s.AddHelper("alice", "Alice", fake.KeyLine(hk.PublicKey(), "")) })
	srv, err := server.New(server.Config{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go srv.Serve(ln)
	h, err := fake.Dial(ln.Addr().String(), hk, fake.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	keyDir := t.TempDir()
	var out syncBuf
	done := make(chan error, 1)
	go func() {
		done <- run(&out, ln.Addr().String(), keyDir, "anna@PC", false, "hello from fakeclient", true)
	}()
	// Wait for the client's key file, then register it like a helper would.
	var pub []byte
	for i := 0; i < 500 && len(pub) == 0; i++ {
		pub, _ = os.ReadFile(filepath.Join(keyDir, fake.KeyFile+".pub"))
		time.Sleep(10 * time.Millisecond)
	}
	h.Send(&control.RegisterClient{Seq: 1, PublicKey: string(pub)})
	if rep, err := fake.WaitFor[control.Reply](h, 5*time.Second, nil); err != nil || rep.Error != "" {
		t.Fatalf("register: %v %+v", err, rep)
	}
	p, err := fake.WaitFor(h, 5*time.Second, func(p *control.Presence) bool {
		return len(p.Clients) == 1 && p.Clients[0].Online
	})
	if err != nil {
		t.Fatal(err)
	}
	h.Send(&control.Connect{Seq: 2, Fingerprint: p.Clients[0].Fingerprint, RequestID: control.NewRequestID()})
	var nch ssh.NewChannel
	select {
	case nch = <-h.Sessions:
	case <-time.After(5 * time.Second):
		t.Fatal("no session")
	}
	ch, reqs, err := nch.Accept()
	if err != nil {
		t.Fatal(err)
	}
	go ssh.DiscardRequests(reqs)
	want := "hello from fakeclient\n"
	line := make([]byte, len(want))
	if _, err := io.ReadFull(ch, line); err != nil || string(line) != want {
		t.Fatalf("got %q %v", line, err)
	}
	io.WriteString(ch, "ping\n")
	echo := make([]byte, 5)
	if _, err := io.ReadFull(ch, echo); err != nil || string(echo) != "ping\n" {
		t.Fatalf("echo %q %v", echo, err)
	}
	ch.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fakeclient did not finish")
	}
	for _, want := range []string{"key: ssh-ed25519 ", "hello: role=pending", "registered: anna@PC", "session-ready: ", "recv: ping", "session: closed"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}
