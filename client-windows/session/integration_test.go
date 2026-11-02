package session

import (
	"context"
	"image"
	"net"
	"testing"
	"time"

	"github.com/mevdschee/help-me-win/client-windows/identity"
	"github.com/mevdschee/help-me-win/client-windows/injector"
	"github.com/mevdschee/help-me-win/proto/inner"
	"github.com/mevdschee/help-me-win/proto/input"
	"github.com/mevdschee/help-me-win/proto/screen"
	"github.com/mevdschee/help-me-win/proto/wire"
)

// TestPatternRunnerRealInner runs the real inner SSH server and the real
// screen codec against an inner client acting as the helper.
func TestPatternRunnerRealInner(t *testing.T) {
	client, err := identity.Ephemeral("anna@PC")
	if err != nil {
		t.Fatal(err)
	}
	helper, _ := identity.Ephemeral("alice")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()
	helperSide, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer helperSide.Close()
	clientSide := <-accepted

	sink := &injector.Counter{}
	r := &PatternRunner{
		HostKey: client.Signer,
		Sink:    sink,
		Hello:   screen.Hello{UserMachine: "anna@PC", OS: "windows", Width: 320, Height: 200},
		Label:   "anna@PC",
		FPS:     60,
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- r.Run(ctx, clientSide, helper.Public) }()

	cs, err := inner.Dial(helperSide, helper.Signer, client.Public)
	if err != nil {
		t.Fatal("inner dial:", err)
	}
	defer cs.Close()

	fr := wire.NewReader(cs.Screen())
	fw := wire.NewWriter(cs.Screen())
	typ, b, err := fr.ReadFrame()
	if err != nil || typ != screen.MsgHello {
		t.Fatalf("hello: %d %v", typ, err)
	}
	h, err := screen.DecodeHello(b)
	if err != nil || h.Width != 320 || h.UserMachine != "anna@PC" {
		t.Fatalf("hello %+v %v", h, err)
	}
	img := image.NewRGBA(image.Rect(0, 0, h.Width, h.Height))
	for i := 1; i <= 5; i++ {
		typ, b, err := fr.ReadFrame()
		if err != nil || typ != screen.MsgFrame {
			t.Fatalf("frame: %d %v", typ, err)
		}
		f, err := screen.DecodeFrame(b)
		if err != nil {
			t.Fatal(err)
		}
		if f.Seq != uint32(i) {
			t.Fatalf("seq %d, want %d", f.Seq, i)
		}
		if err := screen.Apply(img, f); err != nil {
			t.Fatal(err)
		}
		fw.WriteFrame(screen.MsgAck, screen.EncodeAck(screen.Ack{Seq: f.Seq}))
	}

	typ, payload, err := input.Encode(input.Key{Code: "KeyA", Down: true})
	if err != nil {
		t.Fatal(err)
	}
	wire.NewWriter(cs.Input()).WriteFrame(typ, payload)
	deadline := time.Now().Add(2 * time.Second)
	for {
		by, bad := sink.Counts()
		if by[input.MsgKey] == 1 && bad == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("input counts %v, undecoded %d", by, bad)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestPatternSizeCapped(t *testing.T) {
	if MaxWidth*MaxHeight*4 >= wire.MaxFrame {
		t.Fatal("a full frame at the cap does not fit a wire frame")
	}
}

func TestCapSize(t *testing.T) {
	for _, c := range []struct{ w, h, ww, wh int }{
		{1280, 720, 1280, 720},
		{3840, 2160, 1920, 1080},
		{5120, 1440, 1920, 540},
		{1440, 2560, 607, 1080},
	} {
		if w, h := capSize(c.w, c.h); w != c.ww || h != c.wh {
			t.Errorf("capSize(%d, %d) = %d, %d, want %d, %d", c.w, c.h, w, h, c.ww, c.wh)
		}
	}
}
