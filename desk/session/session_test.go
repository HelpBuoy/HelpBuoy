package session

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/helpbuoy/proto/control"
	"github.com/mevdschee/helpbuoy/proto/input"
	"github.com/mevdschee/helpbuoy/proto/screen"
	"github.com/mevdschee/helpbuoy/proto/wire"
)

// fakeCodec encodes everything as JSON, standing in for proto/screen and
// proto/input, with fakeDecoder for frames.
type fakeCodec struct{}

func (fakeCodec) DecodeHello(b []byte) (h screen.Hello, err error) {
	err = json.Unmarshal(b, &h)
	return
}
func (fakeCodec) DecodeFrame(b []byte) (f screen.Frame, err error) {
	err = json.Unmarshal(b, &f)
	return
}
func (fakeCodec) DecodeCursorShape(b []byte) (c screen.CursorShape, err error) {
	err = json.Unmarshal(b, &c)
	return
}
func (fakeCodec) DecodeCursorPos(b []byte) (c screen.CursorPos, err error) {
	err = json.Unmarshal(b, &c)
	return
}
func (fakeCodec) EncodeAck(a screen.Ack) []byte { b, _ := json.Marshal(a); return b }
func (fakeCodec) EncodeSelectMonitor(m screen.SelectMonitor) []byte {
	b, _ := json.Marshal(m)
	return b
}
func (fakeCodec) EncodeQuality(q screen.Quality) []byte { b, _ := json.Marshal(q); return b }
func (fakeCodec) NewDecoder() Decoder                   { return &fakeDecoder{} }
func (fakeCodec) EncodeInput(m any) (uint8, []byte, error) {
	var t uint8
	switch m.(type) {
	case input.MouseMove:
		t = input.MsgMouseMove
	case input.MouseButton:
		t = input.MsgMouseButton
	case input.Wheel:
		t = input.MsgWheel
	case input.Key:
		t = input.MsgKey
	case input.Text:
		t = input.MsgText
	case input.ReleaseAll:
		t = input.MsgReleaseAll
	default:
		return 0, nil, errors.New("unknown input message")
	}
	b, err := json.Marshal(m)
	return t, b, err
}

// fakeDecoder applies OpRaw and OpSolid rects and buffers parts, like
// screen.Decoder. A rect with Kind OpCacheRef always fails, to stand in for
// an unknown cache slot.
type fakeDecoder struct {
	parts  []screen.Rect
	resets int
}

func (d *fakeDecoder) Reset() { d.parts, d.resets = nil, d.resets+1 }

func (d *fakeDecoder) Apply(dst *image.RGBA, f screen.Frame) ([]image.Rectangle, bool, error) {
	d.parts = append(d.parts, f.Rects...)
	if f.More {
		return nil, false, nil
	}
	rs := d.parts
	d.parts = nil
	var dirty []image.Rectangle
	for _, r := range rs {
		rr := image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H)
		if r.W <= 0 || r.H <= 0 || !rr.In(dst.Rect) {
			return nil, false, fmt.Errorf("rect %v outside", rr)
		}
		switch r.Kind {
		case screen.OpRaw:
			if len(r.RGBA) != r.W*r.H*4 {
				return nil, false, errors.New("short pixels")
			}
			for y := 0; y < r.H; y++ {
				copy(dst.Pix[dst.PixOffset(r.X, r.Y+y):], r.RGBA[y*r.W*4:(y+1)*r.W*4])
			}
		case screen.OpSolid:
			for y := r.Y; y < r.Y+r.H; y++ {
				for x := r.X; x < r.X+r.W; x++ {
					copy(dst.Pix[dst.PixOffset(x, y):], r.Color[:])
				}
			}
		default:
			return nil, false, errors.New("unknown cache slot")
		}
		dirty = append(dirty, rr)
	}
	return dirty, true, nil
}

// fakeClient is the client end of the two streams.
type fakeClient struct {
	screen net.Conn
	input  net.Conn
	sw     *wire.Writer
	sr     *wire.Reader
	ir     *wire.Reader
	marker uint32
}

func start(t *testing.T) (*Session, *fakeClient) {
	t.Helper()
	sa, sb := net.Pipe()
	ia, ib := net.Pipe()
	closer := closerFunc(func() error { sa.Close(); ia.Close(); return nil })
	s := Run(Info{ClientName: "anna@ANNA-PC"}, sa, ia, closer, fakeCodec{})
	c := &fakeClient{screen: sb, input: ib, sw: wire.NewWriter(sb), sr: wire.NewReader(sb), ir: wire.NewReader(ib)}
	t.Cleanup(func() { s.Close(); sb.Close(); ib.Close() })
	return s, c
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

func (c *fakeClient) send(t *testing.T, typ uint8, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := c.sw.WriteFrame(typ, b); err != nil {
		t.Fatal(err)
	}
}

func (c *fakeClient) ack(t *testing.T) uint32 {
	t.Helper()
	c.screen.SetReadDeadline(time.Now().Add(5 * time.Second))
	typ, b, err := c.sr.ReadFrame()
	if err != nil || typ != screen.MsgAck {
		t.Fatalf("ack: type %d, %v", typ, err)
	}
	var a screen.Ack
	json.Unmarshal(b, &a)
	return a.Seq
}

func solid(w, h int, v byte) []byte {
	b := make([]byte, w*h*4)
	for i := range b {
		b[i] = v
	}
	return b
}

func TestFramesAppliedAndAcked(t *testing.T) {
	s, c := start(t)
	c.send(t, screen.MsgHello, screen.Hello{UserMachine: "anna@ANNA-PC", Width: 100, Height: 50})
	c.send(t, screen.MsgFrame, screen.Frame{Seq: 1, Rects: []screen.Rect{{X: 0, Y: 0, W: 100, H: 50, RGBA: solid(100, 50, 7)}}})
	if seq := c.ack(t); seq != 1 {
		t.Fatalf("ack seq %d", seq)
	}
	c.send(t, screen.MsgFrame, screen.Frame{Seq: 2, Rects: []screen.Rect{{X: 10, Y: 20, W: 2, H: 3, RGBA: solid(2, 3, 200)}}})
	if seq := c.ack(t); seq != 2 {
		t.Fatalf("ack seq %d", seq)
	}

	var calls int
	s.Update(func(img *image.RGBA, dirty []image.Rectangle, resized bool) {
		calls++
		if !resized || img.Rect.Dx() != 100 || img.Rect.Dy() != 50 {
			t.Errorf("resized=%v size %v", resized, img.Rect)
		}
		if len(dirty) != 1 || dirty[0] != img.Rect {
			t.Errorf("dirty = %v", dirty)
		}
		if img.Pix[img.PixOffset(0, 0)] != 7 || img.Pix[img.PixOffset(11, 22)] != 200 || img.Pix[img.PixOffset(12, 22)] != 7 {
			t.Error("pixels not applied")
		}
	})
	s.Update(func(*image.RGBA, []image.Rectangle, bool) { calls++ })
	if calls != 1 {
		t.Errorf("Update called %d times, want 1 (nothing new the second time)", calls)
	}

	c.send(t, screen.MsgFrame, screen.Frame{Seq: 3, Rects: []screen.Rect{
		{X: 0, Y: 0, W: 1, H: 1, RGBA: solid(1, 1, 1)},
		{X: 90, Y: 40, W: 1, H: 1, RGBA: solid(1, 1, 1)},
	}})
	c.ack(t)
	s.Update(func(img *image.RGBA, dirty []image.Rectangle, resized bool) {
		if resized || !covers(dirty, image.Pt(0, 0)) || !covers(dirty, image.Pt(90, 40)) {
			t.Errorf("resized=%v dirty=%v", resized, dirty)
		}
	})
	if s.Frames() != 3 {
		t.Errorf("frames = %d", s.Frames())
	}
	if s.NameMismatch() != "" {
		t.Errorf("mismatch %q", s.NameMismatch())
	}
}

func TestNameMismatch(t *testing.T) {
	s, c := start(t)
	c.send(t, screen.MsgHello, screen.Hello{UserMachine: "bob@OTHER", Width: 10, Height: 10})
	c.send(t, screen.MsgFrame, screen.Frame{Seq: 1})
	c.ack(t)
	if s.NameMismatch() != "bob@OTHER" {
		t.Errorf("mismatch = %q", s.NameMismatch())
	}
}

func TestBadFramesEndSession(t *testing.T) {
	cases := map[string]func(*testing.T, *fakeClient){
		"frame before hello": func(t *testing.T, c *fakeClient) {
			c.send(t, screen.MsgFrame, screen.Frame{Seq: 1})
		},
		"huge hello": func(t *testing.T, c *fakeClient) {
			c.send(t, screen.MsgHello, screen.Hello{Width: MaxSide + 1, Height: 10})
		},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			s, c := start(t)
			go f(t, c)
			select {
			case <-s.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("session still running")
			}
			if s.Err() == nil {
				t.Error("no error")
			}
		})
	}
}

func TestClientEndsSession(t *testing.T) {
	s, c := start(t)
	c.screen.Close()
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session still running")
	}
}

func TestInput(t *testing.T) {
	s, c := start(t)
	msgs := []any{
		input.MouseMove{X: 100, Y: 65535},
		input.MouseButton{Button: input.ButtonLeft, Down: true},
		input.Wheel{DY: -120},
		input.Key{Code: "KeyA", Down: true},
		input.Text{Text: "é"},
		input.ReleaseAll{},
	}
	for _, m := range msgs {
		s.Send(m)
	}
	want := []uint8{input.MsgMouseMove, input.MsgMouseButton, input.MsgWheel, input.MsgKey, input.MsgText, input.MsgReleaseAll}
	for i, w := range want {
		c.input.SetReadDeadline(time.Now().Add(5 * time.Second))
		typ, b, err := c.ir.ReadFrame()
		if err != nil || typ != w {
			t.Fatalf("message %d: type %d, %v", i, typ, err)
		}
		if i == 4 && !strings.Contains(string(b), "é") {
			t.Errorf("text payload %s", b)
		}
	}
	s.Close()
	s.Send(input.Key{Code: "KeyB"}) // after close: ignored, no panic
	if s.Err() != nil {
		t.Errorf("Err after Close = %v", s.Err())
	}
}

func TestCloseEndsStreams(t *testing.T) {
	s, c := start(t)
	s.Close()
	c.screen.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.screen.Read(make([]byte, 1)); err != io.EOF && !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("client read after close: %v", err)
	}
}

func newKeyLine(t *testing.T) (string, string) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	k, _ := ssh.NewPublicKey(pub)
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))) + " anna@ANNA-PC", ssh.FingerprintSHA256(k)
}

func TestVerify(t *testing.T) {
	line, fp := newKeyLine(t)
	other, _ := newKeyLine(t)
	p := &control.Presence{Clients: []control.ClientInfo{{Name: "anna@ANNA-PC", Fingerprint: fp}}}
	d := control.AgentSessionData{RequestID: "r1", ClientName: "anna@ANNA-PC", ClientKey: line}

	info, err := Verify(d, p, true)
	if err != nil || info.Fingerprint != fp || info.ClientKey == nil {
		t.Fatalf("Verify = %+v, %v", info, err)
	}
	if _, err := Verify(d, p, false); err == nil {
		t.Error("accepted a request this agent did not open")
	}
	if _, err := Verify(d, nil, true); err == nil {
		t.Error("accepted without presence")
	}
	bad := d
	bad.ClientKey = other
	if _, err := Verify(bad, p, true); err == nil {
		t.Error("accepted a key that is not the registered one")
	}
	bad = d
	bad.ClientName = "bob@PC"
	if _, err := Verify(bad, p, true); err == nil {
		t.Error("accepted an unknown client")
	}
	bad = d
	bad.ClientKey = "garbage"
	if _, err := Verify(bad, p, true); err == nil {
		t.Error("accepted a garbage key")
	}
}

func TestSubImageBytes(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	for i := range img.Pix {
		img.Pix[i] = byte(i)
	}
	full := SubImageBytes(img, image.Rect(0, 1, 4, 3), nil)
	if len(full) != 32 || full[0] != 16 {
		t.Errorf("full rows: len %d first %d", len(full), full[0])
	}
	part := SubImageBytes(img, image.Rect(1, 1, 3, 3), nil)
	want := []byte{20, 21, 22, 23, 24, 25, 26, 27, 36, 37, 38, 39, 40, 41, 42, 43}
	if string(part) != string(want) {
		t.Errorf("part = %v", part)
	}
	if SubImageBytes(img, image.Rect(10, 10, 12, 12), nil) != nil {
		t.Error("outside rect")
	}
}

func covers(rs []image.Rectangle, p image.Point) bool {
	for _, r := range rs {
		if p.In(r) {
			return true
		}
	}
	return false
}

// read returns the next message the agent sent on the screen channel.
func (c *fakeClient) read(t *testing.T) (uint8, []byte) {
	t.Helper()
	c.screen.SetReadDeadline(time.Now().Add(5 * time.Second))
	typ, b, err := c.sr.ReadFrame()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return typ, b
}

// sync waits until the session has handled everything sent before it, using
// a cursor position as a marker.
func (c *fakeClient) sync(t *testing.T, s *Session) {
	t.Helper()
	c.marker++
	id := 1_000_000 + c.marker
	c.send(t, screen.MsgCursorPos, screen.CursorPos{ID: id})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if p, _, ok := s.Cursor(); ok && p.ID == id {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("sync marker not seen")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPartsAppliedOnceAndAckedOnce(t *testing.T) {
	s, c := start(t)
	c.send(t, screen.MsgHello, screen.Hello{Width: 100, Height: 100})
	c.sync(t, s)
	s.Update(func(*image.RGBA, []image.Rectangle, bool) {}) // the resize

	c.send(t, screen.MsgFrame, screen.Frame{Seq: 5, More: true, Rects: []screen.Rect{{X: 0, Y: 0, W: 10, H: 10, Kind: screen.OpSolid, Color: [4]byte{9, 9, 9, 255}}}})
	c.send(t, screen.MsgFrame, screen.Frame{Seq: 5, More: true, Rects: []screen.Rect{{X: 70, Y: 70, W: 10, H: 10, Kind: screen.OpSolid, Color: [4]byte{8, 8, 8, 255}}}})
	c.sync(t, s)
	s.Update(func(*image.RGBA, []image.Rectangle, bool) { t.Error("parts of an incomplete frame were published") })
	if s.Frames() != 0 {
		t.Errorf("frames = %d before the last part", s.Frames())
	}
	c.send(t, screen.MsgFrame, screen.Frame{Seq: 5, Rects: []screen.Rect{{X: 20, Y: 20, W: 1, H: 1, RGBA: solid(1, 1, 7)}}})
	if seq := c.ack(t); seq != 5 {
		t.Fatalf("ack %d", seq)
	}
	c.send(t, screen.MsgFrame, screen.Frame{Seq: 6})
	if seq := c.ack(t); seq != 6 {
		t.Fatalf("second ack is %d, want 6 (parts must not be acked)", seq)
	}
	var calls int
	s.Update(func(img *image.RGBA, dirty []image.Rectangle, _ bool) {
		calls++
		for _, p := range []image.Point{{0, 0}, {75, 75}, {20, 20}} {
			if !covers(dirty, p) {
				t.Errorf("%v not in dirty %v", p, dirty)
			}
		}
		if img.Pix[img.PixOffset(5, 5)] != 9 || img.Pix[img.PixOffset(75, 75)] != 8 || img.Pix[img.PixOffset(20, 20)] != 7 {
			t.Error("parts not applied")
		}
	})
	if calls != 1 || s.Frames() != 2 {
		t.Errorf("calls %d frames %d", calls, s.Frames())
	}
}

func TestRefreshOnDecodeError(t *testing.T) {
	s, c := start(t)
	c.send(t, screen.MsgHello, screen.Hello{Width: 100, Height: 100})
	c.send(t, screen.MsgFrame, screen.Frame{Seq: 1, Rects: []screen.Rect{{X: 0, Y: 0, W: 4, H: 4, Kind: screen.OpCacheRef, Slot: 3}}})
	got := map[uint8]int{}
	for range 2 {
		typ, b := c.read(t)
		got[typ]++
		if typ == screen.MsgRefresh && len(b) != 0 {
			t.Errorf("refresh payload %q", b)
		}
	}
	if got[screen.MsgAck] != 1 || got[screen.MsgRefresh] != 1 {
		t.Fatalf("after a bad frame: %v, want one ack and one refresh", got)
	}
	if n, err := s.Refreshes(); n != 1 || err == nil {
		t.Errorf("refreshes %d, %v", n, err)
	}

	// Frames still in flight were encoded against the old cache: they are
	// acked once each (multi-part frames after their last part) but not
	// applied, and they don't trigger another Refresh.
	c.send(t, screen.MsgFrame, screen.Frame{Seq: 2, More: true, Rects: []screen.Rect{{X: 99, Y: 99, W: 4, H: 4, Kind: screen.OpSolid}}})
	c.send(t, screen.MsgFrame, screen.Frame{Seq: 2, Rects: []screen.Rect{{X: 0, Y: 0, W: 1, H: 1, RGBA: solid(1, 1, 3)}}})
	if seq := c.ack(t); seq != 2 {
		t.Fatalf("ack %d, want 2", seq)
	}
	if s.Frames() != 0 {
		t.Errorf("frames %d, want 0 while waiting for a full frame", s.Frames())
	}

	// The client's Full frame resets the decoder and is applied; normal
	// frames follow.
	c.send(t, screen.MsgFrame, screen.Frame{Seq: 3, Full: true, Rects: []screen.Rect{{X: 1, Y: 1, W: 1, H: 1, RGBA: solid(1, 1, 5)}}})
	if seq := c.ack(t); seq != 3 {
		t.Fatalf("ack %d", seq)
	}
	c.send(t, screen.MsgFrame, screen.Frame{Seq: 4, Rects: []screen.Rect{{X: 2, Y: 2, W: 1, H: 1, RGBA: solid(1, 1, 6)}}})
	if seq := c.ack(t); seq != 4 {
		t.Fatalf("ack %d", seq)
	}
	if s.Frames() != 2 {
		t.Errorf("frames %d, want 2", s.Frames())
	}
	if n, _ := s.Refreshes(); n != 1 {
		t.Errorf("refreshes %d, want 1", n)
	}
	select {
	case <-s.Done():
		t.Fatalf("session ended: %v", s.Err())
	default:
	}
	c.sync(t, s)
	if d := s.dec.(*fakeDecoder); d.resets < 3 { // hello, the error and the full frame
		t.Errorf("decoder reset %d times", d.resets)
	}
}

// skipDecoder fails on a part with More set, to test skipping the rest.
type skipDecoder struct{ fakeDecoder }

func (d *skipDecoder) Apply(dst *image.RGBA, f screen.Frame) ([]image.Rectangle, bool, error) {
	if f.More && len(f.Rects) > 0 && f.Rects[0].Kind == screen.OpCacheRef {
		return nil, false, errors.New("unknown slot")
	}
	return d.fakeDecoder.Apply(dst, f)
}

type skipCodec struct{ fakeCodec }

func (skipCodec) NewDecoder() Decoder { return &skipDecoder{} }

func TestSkipRestOfFailedFrame(t *testing.T) {
	sa, sb := net.Pipe()
	ia, ib := net.Pipe()
	s := Run(Info{}, sa, ia, closerFunc(func() error { sa.Close(); ia.Close(); return nil }), skipCodec{})
	c := &fakeClient{screen: sb, input: ib, sw: wire.NewWriter(sb), sr: wire.NewReader(sb), ir: wire.NewReader(ib)}
	defer func() { s.Close(); sb.Close(); ib.Close() }()
	go io.Copy(io.Discard, ib)

	c.send(t, screen.MsgHello, screen.Hello{Width: 64, Height: 64})
	c.send(t, screen.MsgFrame, screen.Frame{Seq: 7, More: true, Rects: []screen.Rect{{X: 0, Y: 0, W: 4, H: 4, Kind: screen.OpCacheRef}}})
	if typ, _ := c.read(t); typ != screen.MsgRefresh {
		t.Fatalf("got %d, want a refresh", typ)
	}
	c.send(t, screen.MsgFrame, screen.Frame{Seq: 7, More: true, Rects: []screen.Rect{{X: 0, Y: 0, W: 1, H: 1, RGBA: solid(1, 1, 1)}}})
	c.send(t, screen.MsgFrame, screen.Frame{Seq: 7, Rects: []screen.Rect{{X: 0, Y: 0, W: 1, H: 1, RGBA: solid(1, 1, 1)}}})
	if seq := c.ack(t); seq != 7 {
		t.Fatalf("ack %d", seq)
	}
	c.send(t, screen.MsgFrame, screen.Frame{Seq: 8, Full: true, Rects: []screen.Rect{{X: 2, Y: 2, W: 1, H: 1, RGBA: solid(1, 1, 2)}}})
	if seq := c.ack(t); seq != 8 {
		t.Fatalf("ack %d", seq)
	}
	if s.Frames() != 1 {
		t.Errorf("frames %d, want only frame 8 applied", s.Frames())
	}
}

func TestHelloResizesAndResets(t *testing.T) {
	s, c := start(t)
	mons := []screen.Monitor{{W: 100, H: 50, Primary: true, Name: "A"}, {X: 100, W: 80, H: 60, Name: "B"}}
	c.send(t, screen.MsgHello, screen.Hello{Width: 100, Height: 50, Monitors: mons, Current: 0})
	c.send(t, screen.MsgFrame, screen.Frame{Seq: 1, Rects: []screen.Rect{{X: 0, Y: 0, W: 100, H: 50, RGBA: solid(100, 50, 7)}}})
	c.ack(t)
	s.Update(func(*image.RGBA, []image.Rectangle, bool) {})

	c.send(t, screen.MsgHello, screen.Hello{Width: 80, Height: 60, Monitors: mons, Current: 1})
	c.sync(t, s)
	h, ok := s.Hello()
	if !ok || h.Current != 1 || len(h.Monitors) != 2 || h.Monitors[1].Name != "B" {
		t.Errorf("hello %+v", h)
	}
	var calls int
	s.Update(func(img *image.RGBA, dirty []image.Rectangle, resized bool) {
		calls++
		if !resized || img.Rect != image.Rect(0, 0, 80, 60) || len(dirty) != 1 || dirty[0] != img.Rect {
			t.Errorf("resized %v rect %v dirty %v", resized, img.Rect, dirty)
		}
	})
	if calls != 1 {
		t.Error("no upload after a resize")
	}
	if d := s.dec.(*fakeDecoder); d.resets != 2 {
		t.Errorf("decoder reset %d times, want once per hello", d.resets)
	}

	// A hello with the same size still resets the decoder but keeps the image.
	c.send(t, screen.MsgHello, screen.Hello{Width: 80, Height: 60, Current: 5})
	c.sync(t, s)
	if h, _ := s.Hello(); h.Current != -1 {
		t.Errorf("out of range Current kept: %d", h.Current)
	}
	s.Update(func(*image.RGBA, []image.Rectangle, bool) { t.Error("same-size hello resized") })
}

func TestCursorMessages(t *testing.T) {
	s, c := start(t)
	if _, _, ok := s.Cursor(); ok {
		t.Error("cursor before any position")
	}
	shape := screen.CursorShape{ID: 42, HotX: 1, HotY: 2, W: 2, H: 2, RGBA: solid(2, 2, 255)}
	c.send(t, screen.MsgCursor, shape)
	c.send(t, screen.MsgCursor, screen.CursorShape{ID: 43, W: 3, H: 3, RGBA: solid(1, 1, 0)}) // bad: ignored
	c.send(t, screen.MsgCursorPos, screen.CursorPos{X: -5, Y: 30, ID: 42, Visible: true})
	deadline := time.Now().Add(5 * time.Second)
	for {
		p, sh, ok := s.Cursor()
		if ok && p.ID == 42 {
			if p.X != -5 || p.Y != 30 || !p.Visible || sh == nil || sh.HotY != 2 || sh.W != 2 {
				t.Fatalf("cursor %+v shape %+v", p, sh)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no cursor")
		}
		time.Sleep(time.Millisecond)
	}
	c.send(t, screen.MsgCursorPos, screen.CursorPos{ID: 43, Visible: true})
	deadline = time.Now().Add(5 * time.Second)
	for {
		p, sh, _ := s.Cursor()
		if p.ID == 43 {
			if sh != nil {
				t.Error("invalid shape cached")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no cursor")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestShapeCacheBounded(t *testing.T) {
	s := newSession(Info{}, fakeCodec{}, nil)
	for i := range maxShapes + 10 {
		s.addShape(screen.CursorShape{ID: uint32(i), W: 1, H: 1, RGBA: solid(1, 1, 1)})
	}
	if len(s.shapes) != maxShapes || s.shapes[0] != nil || s.shapes[maxShapes+9] == nil {
		t.Errorf("%d shapes", len(s.shapes))
	}
}

func TestControlMessages(t *testing.T) {
	s, c := start(t)
	s.SelectMonitor(1)
	typ, b := c.read(t)
	if typ != screen.MsgSelectMonitor || !strings.Contains(string(b), `"Index":1`) {
		t.Fatalf("got %d %s", typ, b)
	}
	s.SetQuality(screen.QualitySharp)
	typ, b = c.read(t)
	if typ != screen.MsgQuality || !strings.Contains(string(b), `"Mode":1`) {
		t.Fatalf("got %d %s", typ, b)
	}
	if s.Quality() != screen.QualitySharp {
		t.Error("quality not kept")
	}
	s.Refresh()
	if typ, _ = c.read(t); typ != screen.MsgRefresh {
		t.Fatalf("got %d", typ)
	}
}

func TestStats(t *testing.T) {
	s, c := start(t)
	now := time.Now()
	s.statsAt = now
	c.send(t, screen.MsgHello, screen.Hello{Width: 10, Height: 10})
	c.send(t, screen.MsgFrame, screen.Frame{Seq: 1})
	c.ack(t)
	fps, mbps := s.Stats(now.Add(2 * time.Second))
	if fps != 0.5 || mbps <= 0 {
		t.Errorf("fps %v mbps %v", fps, mbps)
	}
}

func TestInputMovesCoalescedInOrder(t *testing.T) {
	s, c := start(t)
	for i := range 50 {
		s.Send(input.MouseMove{X: uint16(i)})
	}
	s.Send(input.Key{Code: "KeyA", Down: true})
	s.Send(input.MouseMove{X: 1000})
	var moves []uint16
	sawKey := false
	for {
		c.input.SetReadDeadline(time.Now().Add(5 * time.Second))
		typ, b, err := c.ir.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		if typ == input.MsgKey {
			sawKey = true
			if len(moves) == 0 || moves[len(moves)-1] != 49 {
				t.Errorf("key not preceded by the latest move: %v", moves)
			}
			continue
		}
		var m input.MouseMove
		json.Unmarshal(b, &m)
		moves = append(moves, m.X)
		if m.X == 1000 {
			break
		}
	}
	if !sawKey || len(moves) > 10 {
		t.Errorf("moves %v key %v", moves, sawKey)
	}
}

func TestCoalescer(t *testing.T) {
	t0 := time.Unix(1000, 0)
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	c := Coalescer{Interval: 4 * time.Millisecond}
	mv := func(x uint16) input.MouseMove { return input.MouseMove{X: x} }
	check := func(name string, got []any, want ...any) {
		t.Helper()
		if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", want) {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}
	check("first move", c.Push(mv(1), at(0)), mv(1))
	check("held", c.Push(mv(2), at(1)))
	check("replaced", c.Push(mv(3), at(2)))
	if due, ok := c.Due(); !ok || !due.Equal(at(4)) {
		t.Errorf("due %v %v", due, ok)
	}
	check("tick early", c.Tick(at(3)))
	check("tick due", c.Tick(at(4)), mv(3))
	if _, ok := c.Due(); ok {
		t.Error("still due")
	}
	check("move after interval", c.Push(mv(4), at(9)), mv(4))
	check("held again", c.Push(mv(5), at(10)))
	key := input.Key{Code: "KeyA", Down: true}
	check("key flushes the move first", c.Push(key, at(11)), mv(5), key)
	check("move right after flush is held", c.Push(mv(6), at(12)))
	btn := input.MouseButton{Down: true}
	check("button", c.Push(btn, at(13)), mv(6), btn)
	check("wheel alone", c.Push(input.Wheel{DY: 120}, at(14)), input.Wheel{DY: 120})
	check("keys never coalesced", c.Push(key, at(14)), key)
}

func TestPlanUpload(t *testing.T) {
	b := image.Rect(0, 0, 1920, 1080)
	if PlanUpload(nil, b) != nil {
		t.Error("nothing dirty")
	}
	if got := PlanUpload([]image.Rectangle{image.Rect(5000, 0, 5001, 1)}, b); got != nil {
		t.Errorf("outside: %v", got)
	}
	// A caret: one cell.
	got := PlanUpload([]image.Rectangle{image.Rect(100, 100, 102, 120)}, b)
	if len(got) != 1 || got[0] != image.Rect(64, 64, 128, 128) {
		t.Errorf("caret: %v", got)
	}
	// Two far apart rects stay apart; adjacent tiles join.
	got = PlanUpload([]image.Rectangle{image.Rect(0, 0, 64, 64), image.Rect(64, 0, 128, 64), image.Rect(1800, 1000, 1810, 1010)}, b)
	if len(got) != 2 || got[0] != image.Rect(0, 0, 128, 64) || got[1] != image.Rect(1792, 960, 1856, 1024) {
		t.Errorf("tiles: %v", got)
	}
	// A vertical strip of tiles becomes one rect.
	var strip []image.Rectangle
	for y := 0; y < 1080; y += 64 {
		strip = append(strip, image.Rect(640, y, 704, min(y+64, 1080)))
	}
	if got := PlanUpload(strip, b); len(got) != 1 || got[0] != image.Rect(640, 0, 704, 1080) {
		t.Errorf("strip: %v", got)
	}
	// Most of the screen: one full upload.
	if got := PlanUpload([]image.Rectangle{image.Rect(0, 0, 1920, 700)}, b); len(got) != 1 || got[0] != b {
		t.Errorf("large: %v", got)
	}
	// Edge cells are clipped to the image.
	if got := PlanUpload([]image.Rectangle{image.Rect(1919, 1079, 1920, 1080)}, b); len(got) != 1 || got[0] != image.Rect(1856, 1024, 1920, 1080) {
		t.Errorf("edge: %v", got)
	}
	// Scattered tiles: never more than MaxUploads, always covering.
	var scattered []image.Rectangle
	for i := range 300 {
		x, y := (i*7919)%1900, (i*104729)%1060
		scattered = append(scattered, image.Rect(x, y, x+8, y+8))
	}
	got = PlanUpload(scattered, b)
	if len(got) > MaxUploads {
		t.Errorf("%d uploads", len(got))
	}
	for _, r := range scattered {
		if !covers(got, r.Min) || !covers(got, r.Max.Sub(image.Pt(1, 1))) {
			t.Fatalf("%v not covered by %v", r, got)
		}
	}
}

// TestRealCodec runs parts, a decode error and cursor messages through the
// real proto/screen encoding and Decoder.
func TestRealCodec(t *testing.T) {
	sa, sb := net.Pipe()
	ia, ib := net.Pipe()
	s := Run(Info{}, sa, ia, closerFunc(func() error { sa.Close(); ia.Close(); return nil }), ProtoCodec{})
	defer func() { s.Close(); sb.Close(); ib.Close() }()
	go io.Copy(io.Discard, ib)
	w, r := wire.NewWriter(sb), wire.NewReader(sb)
	read := func() (uint8, []byte) {
		t.Helper()
		sb.SetReadDeadline(time.Now().Add(5 * time.Second))
		typ, b, err := r.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		return typ, b
	}

	mons := []screen.Monitor{{W: 128, H: 64, Primary: true, Name: "A"}, {X: 128, W: 64, H: 64, Name: "B"}}
	w.WriteFrame(screen.MsgHello, screen.EncodeHello(screen.Hello{Width: 128, Height: 64, Monitors: mons, Current: 0}))
	w.WriteFrame(screen.MsgFrame, screen.EncodeFrame(screen.Frame{Seq: 1, More: true, Rects: []screen.Rect{{Kind: screen.OpSolid, W: 64, H: 64, Color: [4]byte{1, 2, 3, 255}, Store: true, Slot: 4}}}))
	w.WriteFrame(screen.MsgFrame, screen.EncodeFrame(screen.Frame{Seq: 1, Rects: []screen.Rect{{Kind: screen.OpCacheRef, X: 64, W: 64, H: 64, Slot: 4}}}))
	if typ, b := read(); typ != screen.MsgAck {
		t.Fatalf("got %d", typ)
	} else if a, _ := screen.DecodeAck(b); a.Seq != 1 {
		t.Fatalf("ack %d", a.Seq)
	}
	shape := screen.CursorShape{ID: 7, W: 2, H: 2, RGBA: solid(2, 2, 200)}
	w.WriteFrame(screen.MsgCursor, screen.EncodeCursorShape(shape))
	w.WriteFrame(screen.MsgCursorPos, screen.EncodeCursorPos(screen.CursorPos{X: 3, Y: 4, ID: 7, Visible: true}))

	// An unknown cache slot: Refresh and an ack, the session goes on.
	w.WriteFrame(screen.MsgFrame, screen.EncodeFrame(screen.Frame{Seq: 2, Rects: []screen.Rect{{Kind: screen.OpCacheRef, W: 8, H: 8, Slot: 99}}}))
	got := map[uint8]int{}
	for range 2 {
		typ, _ := read()
		got[typ]++
	}
	if got[screen.MsgAck] != 1 || got[screen.MsgRefresh] != 1 {
		t.Fatalf("after a bad frame: %v", got)
	}
	s.Update(func(img *image.RGBA, _ []image.Rectangle, _ bool) {
		if img.Pix[img.PixOffset(100, 10)] != 1 || img.Pix[img.PixOffset(10, 10)+2] != 3 {
			t.Error("solid and cache ref not applied")
		}
	})
	p, sh, ok := s.Cursor()
	if !ok || p.X != 3 || sh == nil || sh.RGBA[0] != 200 {
		t.Errorf("cursor %+v %v", p, sh)
	}
	if h, _ := s.Hello(); len(h.Monitors) != 2 || h.Monitors[1].Name != "B" {
		t.Errorf("hello %+v", h)
	}
	s.SelectMonitor(1)
	if typ, b := read(); typ != screen.MsgSelectMonitor {
		t.Fatalf("got %d", typ)
	} else if m, err := screen.DecodeSelectMonitor(b); err != nil || m.Index != 1 {
		t.Errorf("select %+v %v", m, err)
	}
	s.SetQuality(screen.QualityFast)
	if typ, b := read(); typ != screen.MsgQuality {
		t.Fatalf("got %d", typ)
	} else if q, err := screen.DecodeQuality(b); err != nil || q.Mode != screen.QualityFast {
		t.Errorf("quality %+v %v", q, err)
	}
}
