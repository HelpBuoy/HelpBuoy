package session

import (
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/proto/inner"
	"github.com/helpbuoy/helpbuoy/proto/screen"
	"github.com/helpbuoy/helpbuoy/proto/wire"
)

type pipeStream struct{ net.Conn }

func (p pipeStream) CloseWrite() error { return p.Close() }

// fakeInner is an inner session whose streams are pipes to the test.
type fakeInner struct {
	screen, input inner.Stream
	closed        chan struct{}
	once          sync.Once
}

func (f *fakeInner) Screen() inner.Stream { return f.screen }
func (f *fakeInner) Input() inner.Stream  { return f.input }
func (f *fakeInner) Close() error {
	f.once.Do(func() {
		close(f.closed)
		f.screen.Close()
		f.input.Close()
	})
	return nil
}
func (f *fakeInner) Wait() error { <-f.closed; return nil }

// fakeCodec makes every frame different and encodes only what the test needs.
type fakeCodec struct {
	mu    sync.Mutex
	n     byte
	fulls int
}

func (c *fakeCodec) Render(dst *image.RGBA, _ float64, _ string) {
	c.mu.Lock()
	c.n++
	dst.Pix[0] = c.n
	c.mu.Unlock()
}

func (c *fakeCodec) Diff(prev, cur *image.RGBA) []screen.Rect {
	if prev == nil {
		c.mu.Lock()
		c.fulls++
		c.mu.Unlock()
		return []screen.Rect{{W: cur.Rect.Dx(), H: cur.Rect.Dy()}}
	}
	if prev.Pix[0] == cur.Pix[0] {
		return nil
	}
	return []screen.Rect{{W: 1, H: 1}}
}

func (c *fakeCodec) EncodeHello(h screen.Hello) []byte { return []byte(h.UserMachine) }

func (c *fakeCodec) EncodeFrame(f screen.Frame) []byte {
	b := binary.BigEndian.AppendUint32(nil, f.Seq)
	return binary.BigEndian.AppendUint32(b, uint32(f.Rects[0].W))
}

func (c *fakeCodec) DecodeAck(b []byte) (screen.Ack, error) {
	if len(b) != 4 {
		return screen.Ack{}, errors.New("bad ack")
	}
	return screen.Ack{Seq: binary.BigEndian.Uint32(b)}, nil
}

type sinkFunc func(uint8, []byte)

func (f sinkFunc) Input(t uint8, b []byte) { f(t, b) }

type streamFixture struct {
	runErr  chan error
	cancel  context.CancelFunc
	screenR *wire.Reader
	screenW *wire.Writer
	inputW  *wire.Writer
	codec   *fakeCodec
	inputs  chan uint8
	inner   *fakeInner
}

func startStream(t *testing.T, serveErr error) *streamFixture {
	t.Helper()
	cs, hs := net.Pipe()
	ci, hi := net.Pipe()
	fi := &fakeInner{screen: pipeStream{cs}, input: pipeStream{ci}, closed: make(chan struct{})}
	f := &streamFixture{
		runErr:  make(chan error, 1),
		screenR: wire.NewReader(hs),
		screenW: wire.NewWriter(hs),
		inputW:  wire.NewWriter(hi),
		codec:   &fakeCodec{},
		inputs:  make(chan uint8, 16),
		inner:   fi,
	}
	t.Cleanup(func() { hs.Close(); hi.Close() })
	r := &PatternRunner{
		Serve: func(rw io.ReadWriteCloser, hostKey ssh.Signer, techieKey ssh.PublicKey) (InnerSession, error) {
			if serveErr != nil {
				return nil, serveErr
			}
			return fi, nil
		},
		Codec: f.codec,
		Sink:  sinkFunc(func(typ uint8, _ []byte) { f.inputs <- typ }),
		Hello: screen.Hello{UserMachine: "anna@PC", Width: 8, Height: 8},
		FPS:   200,
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	ch, other := net.Pipe()
	t.Cleanup(func() { other.Close() })
	go func() { f.runErr <- r.Run(ctx, ch, nil) }()
	return f
}

func (f *streamFixture) ack(seq uint32) {
	f.screenW.WriteFrame(screen.MsgAck, binary.BigEndian.AppendUint32(nil, seq))
}

func TestStreamFlowControl(t *testing.T) {
	f := startStream(t, nil)
	r := f.screenR
	typ, b, err := r.ReadFrame()
	if err != nil || typ != screen.MsgHello || string(b) != "anna@PC" {
		t.Fatalf("hello %d %q %v", typ, b, err)
	}
	readFrame := func() (uint32, bool) {
		typ, b, err := r.ReadFrame()
		if err != nil || typ != screen.MsgFrame {
			t.Fatalf("frame %d %v", typ, err)
		}
		return binary.BigEndian.Uint32(b), binary.BigEndian.Uint32(b[4:]) == 8
	}
	// Two frames go out without acks; the first is a full frame.
	if seq, full := readFrame(); seq != 1 || !full {
		t.Fatalf("first frame %d full=%v", seq, full)
	}
	if seq, full := readFrame(); seq != 2 || full {
		t.Fatalf("second frame %d full=%v", seq, full)
	}
	// No third frame while two are unacked.
	got := make(chan uint32, 8)
	go func() {
		for {
			typ, b, err := r.ReadFrame()
			if err != nil {
				close(got)
				return
			}
			if typ == screen.MsgFrame {
				got <- binary.BigEndian.Uint32(b)
			}
		}
	}()
	select {
	case s := <-got:
		t.Fatalf("frame %d sent with 2 unacked", s)
	case <-time.After(100 * time.Millisecond):
	}
	// Acking frame 1 allows exactly one more.
	f.ack(1)
	if s := <-got; s != 3 {
		t.Fatalf("got frame %d, want 3", s)
	}
	select {
	case s := <-got:
		t.Fatalf("frame %d sent with 2 unacked", s)
	case <-time.After(100 * time.Millisecond):
	}
	// A stale ack changes nothing; a cumulative ack opens two slots.
	f.ack(1)
	f.ack(3)
	if a, b := <-got, <-got; a != 4 || b != 5 {
		t.Fatalf("got %d %d", a, b)
	}
	// Refresh: the next frame is a full one.
	f.codec.mu.Lock()
	fulls := f.codec.fulls
	f.codec.mu.Unlock()
	f.screenW.WriteFrame(screen.MsgRefresh, nil)
	f.ack(5)
	<-got
	<-got
	f.codec.mu.Lock()
	if f.codec.fulls != fulls+1 {
		t.Errorf("full frames %d, want %d", f.codec.fulls, fulls+1)
	}
	f.codec.mu.Unlock()

	// Input frames reach the sink.
	f.inputW.WriteFrame(4, []byte("KeyA"))
	select {
	case typ := <-f.inputs:
		if typ != 4 {
			t.Fatal(typ)
		}
	case <-time.After(time.Second):
		t.Fatal("no input")
	}

	// Ending the session locally returns nil and closes the inner session.
	f.cancel()
	select {
	case err := <-f.runErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
	select {
	case <-f.inner.closed:
	default:
		t.Fatal("inner session not closed")
	}
}

func TestStreamEndsWhenInnerEnds(t *testing.T) {
	f := startStream(t, nil)
	go func() {
		for {
			if _, _, err := f.screenR.ReadFrame(); err != nil {
				return
			}
		}
	}()
	f.inner.Close() // the techie ended the session
	select {
	case <-f.runErr:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestStreamHandshakeError(t *testing.T) {
	f := startStream(t, inner.ErrWrongKey)
	if err := <-f.runErr; !errors.Is(err, inner.ErrWrongKey) {
		t.Fatalf("err %v", err)
	}
}
