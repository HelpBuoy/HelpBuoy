package main

import (
	"bytes"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/helpbuoy/proto/control"
	"github.com/mevdschee/helpbuoy/server/fake"
	"github.com/mevdschee/helpbuoy/server/server"
	"github.com/mevdschee/helpbuoy/server/store"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestFakeTechieEndToEnd(t *testing.T) {
	dir := t.TempDir()
	keyDir := t.TempDir()
	hk, err := fake.LoadOrCreateKey(keyDir, "faketechie")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open(dir)
	st.Update(func(s *store.State) error { return s.AddTechie("alice", fake.KeyLine(hk.PublicKey(), "")) })
	srv, err := server.New(server.Config{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go srv.Serve(ln)

	ck := fake.NewKey()
	c, err := fake.Dial(ln.Addr().String(), ck, fake.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Send(&control.ComputerHello{UserMachine: "anna@PC"})

	var out syncBuf
	done := make(chan error, 1)
	go func() {
		done <- run(&out, options{server: ln.Addr().String(), keyDir: keyDir, register: fake.KeyLine(ck.PublicKey(), "anna@PC"),
			connectFirst: true, message: "hello from faketechie", once: true})
	}()
	if _, err := fake.WaitFor[control.Registered](c, 5*time.Second, nil); err != nil {
		t.Fatal(err)
	}
	ready, err := fake.WaitFor[control.SessionReady](c, 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	ch, reqs, err := c.Client.OpenChannel(control.ChannelSession,
		control.MarshalSessionChannelData(control.SessionChannelData{RequestID: ready.RequestID}))
	if err != nil {
		t.Fatal(err)
	}
	go ssh.DiscardRequests(reqs)
	line := make([]byte, len("hello from faketechie\n"))
	if _, err := io.ReadFull(ch, line); err != nil || string(line) != "hello from faketechie\n" {
		t.Fatalf("got %q %v", line, err)
	}
	io.WriteString(ch, "hi there\n")
	time.Sleep(50 * time.Millisecond)
	ch.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("faketechie did not finish")
	}
	for _, want := range []string{"hello: role=techie", "reply: seq=1 ok", "connect: seq=2", "session: open", "recv: hi there", "session: closed"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}
