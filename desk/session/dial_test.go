package session

import (
	"crypto/ed25519"
	"crypto/rand"
	"image"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/proto/inner"
	"github.com/helpbuoy/helpbuoy/proto/input"
	"github.com/helpbuoy/helpbuoy/proto/screen"
	"github.com/helpbuoy/helpbuoy/proto/wire"
)

func signer(t *testing.T) ssh.Signer {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// tcpPair returns two ends of a loopback TCP connection; net.Pipe would
// deadlock the SSH version exchange.
func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	acc := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		acc <- c
	}()
	a, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return a, <-acc
}

// TestDialRealInner runs the techie side against the real inner server and
// the real screen and input encodings.
func TestDialRealInner(t *testing.T) {
	techieKey, computerKey := signer(t), signer(t)
	hc, cc := tcpPair(t)
	defer cc.Close()

	type served struct {
		s   *inner.ServerSession
		err error
	}
	srv := make(chan served, 1)
	go func() {
		s, err := inner.Serve(cc, computerKey, techieKey.PublicKey())
		srv <- served{s, err}
	}()

	s := Dial(hc, techieKey, Info{ComputerName: "anna@ANNA-PC", ComputerKey: computerKey.PublicKey()})
	defer s.Close()
	sv := <-srv
	if sv.err != nil {
		t.Fatal(sv.err)
	}
	defer sv.s.Close()

	w := wire.NewWriter(sv.s.Screen())
	r := wire.NewReader(sv.s.Screen())
	w.WriteFrame(screen.MsgHello, screen.EncodeHello(screen.Hello{UserMachine: "anna@ANNA-PC", Width: 64, Height: 32}))
	px := make([]byte, 64*32*4)
	for i := range px {
		px[i] = 0x55
	}
	w.WriteFrame(screen.MsgFrame, screen.EncodeFrame(screen.Frame{Seq: 9, Rects: []screen.Rect{{W: 64, H: 32, RGBA: px}}}))
	typ, b, err := r.ReadFrame()
	if err != nil || typ != screen.MsgAck {
		t.Fatalf("ack: %d %v", typ, err)
	}
	if a, _ := screen.DecodeAck(b); a.Seq != 9 {
		t.Errorf("ack seq %d", a.Seq)
	}
	got := false
	s.Update(func(img *image.RGBA, _ []image.Rectangle, _ bool) { got = img.Pix[0] == 0x55 })
	if !got {
		t.Error("frame not applied")
	}

	s.Send(input.Key{Code: "KeyA", Down: true})
	ir := wire.NewReader(sv.s.Input())
	typ, b, err = ir.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	m, err := input.Decode(typ, b)
	if k, ok := m.(input.Key); err != nil || !ok || k.Code != "KeyA" || !k.Down {
		t.Errorf("input = %#v, %v", m, err)
	}

	// The computer ends the session: the techie side notices.
	sv.s.Close()
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("session did not end")
	}
}

// TestDialWrongComputerKey: a server that splices to the wrong computer is refused.
func TestDialWrongComputerKey(t *testing.T) {
	techieKey, computerKey := signer(t), signer(t)
	hc, cc := tcpPair(t)
	defer cc.Close()
	go inner.Serve(cc, computerKey, techieKey.PublicKey())
	s := Dial(hc, techieKey, Info{ComputerKey: signer(t).PublicKey()})
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("session with the wrong key still running")
	}
	if s.Err() == nil {
		t.Error("no error for the wrong computer key")
	}
}
