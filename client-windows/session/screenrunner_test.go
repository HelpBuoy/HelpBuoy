package session

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/helpbuoy/client-windows/capture"
	"github.com/mevdschee/helpbuoy/proto/encode"
	"github.com/mevdschee/helpbuoy/proto/screen"
	"github.com/mevdschee/helpbuoy/proto/wire"
)

// fakeScreen is a capture.Screen whose content, layout and cursor the test
// sets. A capture's first pixel byte is the current content number.
type fakeScreen struct {
	mu       sync.Mutex
	monitors []capture.Monitor
	content  byte
	cursor   capture.CursorInfo
	shapes   map[uintptr]capture.Shape
	opened   []capture.Monitor
	shapeReq int
	closed   bool
}

func (s *fakeScreen) Monitors() ([]capture.Monitor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]capture.Monitor(nil), s.monitors...), nil
}

func (s *fakeScreen) Open(m capture.Monitor) (capture.Capturer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opened = append(s.opened, m)
	return &fakeCapturer{s: s, w: m.Rect.Dx(), h: m.Rect.Dy()}, nil
}

func (s *fakeScreen) Cursor() (capture.CursorInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor, nil
}

func (s *fakeScreen) CursorShape(h uintptr) (capture.Shape, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shapeReq++
	sh, ok := s.shapes[h]
	if !ok {
		return capture.Shape{}, errors.New("invalid cursor handle")
	}
	return sh, nil
}

func (s *fakeScreen) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func (s *fakeScreen) set(f func(s *fakeScreen)) {
	s.mu.Lock()
	f(s)
	s.mu.Unlock()
}

type fakeCapturer struct {
	s    *fakeScreen
	w, h int
}

func (c *fakeCapturer) Capture() (*image.RGBA, error) {
	img := image.NewRGBA(image.Rect(0, 0, c.w, c.h))
	c.s.mu.Lock()
	img.Pix[0] = c.s.content
	c.s.mu.Unlock()
	return img, nil
}

func (c *fakeCapturer) Close() error { return nil }

// fakeEncoder sends a frame whenever the first pixel changed, a full one
// (W = image width) after Reset.
type fakeEncoder struct {
	mu      sync.Mutex
	last    int // -1 after Reset
	parts   int
	resets  int
	quality screen.QualityMode
	acked   []int
	fps     int
	refine  time.Time
}

func (e *fakeEncoder) Encode(cur *image.RGBA, now time.Time) []screen.Frame {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.refine.IsZero() && !now.Before(e.refine) {
		e.refine = time.Time{}
	}
	w := 1
	if e.last < 0 {
		w = cur.Rect.Dx()
	} else if int(cur.Pix[0]) == e.last {
		return nil
	}
	e.last = int(cur.Pix[0])
	n := max(e.parts, 1)
	fs := make([]screen.Frame, n)
	for i := range fs {
		fs[i].Rects = []screen.Rect{{W: w, H: 1, RGBA: make([]byte, 100)}}
	}
	return fs
}

func (e *fakeEncoder) RefineDue() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.refine
}

func (e *fakeEncoder) Reset() {
	e.mu.Lock()
	e.last = -1
	e.resets++
	e.mu.Unlock()
}

func (e *fakeEncoder) SetQuality(m screen.QualityMode) {
	e.mu.Lock()
	e.quality = m
	e.mu.Unlock()
}

func (e *fakeEncoder) Acked(bytes int, rtt time.Duration) {
	e.mu.Lock()
	e.acked = append(e.acked, bytes)
	e.mu.Unlock()
}

func (e *fakeEncoder) Stats() encode.Stats {
	e.mu.Lock()
	defer e.mu.Unlock()
	return encode.Stats{TargetFPS: e.fps}
}

func (e *fakeEncoder) get(f func(e *fakeEncoder)) {
	e.mu.Lock()
	f(e)
	e.mu.Unlock()
}

// jsonCodec encodes every message as JSON.
type jsonCodec struct{}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func (jsonCodec) EncodeHello(h screen.Hello) []byte             { return mustJSON(h) }
func (jsonCodec) EncodeCursorShape(c screen.CursorShape) []byte { return mustJSON(c) }
func (jsonCodec) EncodeCursorPos(c screen.CursorPos) []byte     { return mustJSON(c) }
func (jsonCodec) EncodeFrame(f screen.Frame) []byte {
	return mustJSON(sentFrame{Seq: f.Seq, More: f.More, W: f.Rects[0].W})
}
func (jsonCodec) DecodeAck(b []byte) (a screen.Ack, err error) {
	err = json.Unmarshal(b, &a)
	return
}
func (jsonCodec) DecodeSelectMonitor(b []byte) (s screen.SelectMonitor, err error) {
	err = json.Unmarshal(b, &s)
	return
}
func (jsonCodec) DecodeQuality(b []byte) (q screen.Quality, err error) {
	err = json.Unmarshal(b, &q)
	return
}

type sentFrame struct {
	Seq  uint32
	More bool
	W    int
}

type fakeInjector struct {
	mu       sync.Mutex
	monitors []image.Rectangle
	inputs   []uint8
	closed   bool
	kick     func()
}

func (j *fakeInjector) SetMonitor(r image.Rectangle) {
	j.mu.Lock()
	j.monitors = append(j.monitors, r)
	j.mu.Unlock()
}

func (j *fakeInjector) Input(typ uint8, _ []byte) {
	j.mu.Lock()
	j.inputs = append(j.inputs, typ)
	j.mu.Unlock()
}

func (j *fakeInjector) Close() error {
	j.mu.Lock()
	j.closed = true
	j.mu.Unlock()
	return nil
}

func (j *fakeInjector) get(f func(j *fakeInjector)) {
	j.mu.Lock()
	f(j)
	j.mu.Unlock()
}

// agentSide reads the screen channel like an agent would.
type agentSide struct {
	hellos chan screen.Hello
	frames chan sentFrame
	shapes chan screen.CursorShape
	poses  chan screen.CursorPos
	w      *wire.Writer
}

type runnerFixture struct {
	t      *testing.T
	scr    *fakeScreen
	enc    *fakeEncoder
	inj    *fakeInjector
	h      *agentSide
	inputW *wire.Writer
	inner  *fakeInner
	cancel context.CancelFunc
	runErr chan error
}

var (
	mon0 = capture.Monitor{Rect: image.Rect(0, 0, 40, 30), Primary: true, Name: "DISPLAY1"}
	mon1 = capture.Monitor{Rect: image.Rect(40, -10, 60, 10), Name: "DISPLAY2"}
)

func startRunner(t *testing.T, pacing Pacing, setup func(f *runnerFixture)) *runnerFixture {
	t.Helper()
	cs, hs := net.Pipe()
	ci, hi := net.Pipe()
	fi := &fakeInner{screen: pipeStream{cs}, input: pipeStream{ci}, closed: make(chan struct{})}
	f := &runnerFixture{
		t: t,
		scr: &fakeScreen{
			monitors: []capture.Monitor{mon0, mon1},
			cursor:   capture.CursorInfo{Showing: true, Pos: image.Pt(5, 6), Handle: 1},
			shapes: map[uintptr]capture.Shape{
				1: {W: 1, H: 1, RGBA: []byte{1, 2, 3, 255}},
				2: {W: 1, H: 1, RGBA: []byte{9, 9, 9, 255}},
				3: {W: 1, H: 1, RGBA: []byte{1, 2, 3, 255}}, // same image as 1
			},
		},
		enc: &fakeEncoder{last: -1},
		inj: &fakeInjector{},
		h: &agentSide{
			hellos: make(chan screen.Hello, 16),
			frames: make(chan sentFrame, 64),
			shapes: make(chan screen.CursorShape, 16),
			poses:  make(chan screen.CursorPos, 64),
			w:      wire.NewWriter(hs),
		},
		inputW: wire.NewWriter(hi),
		inner:  fi,
		runErr: make(chan error, 1),
	}
	if setup != nil {
		setup(f)
	}
	t.Cleanup(func() { hs.Close(); hi.Close() })
	go func() {
		r := wire.NewReader(hs)
		for {
			typ, b, err := r.ReadFrame()
			if err != nil {
				return
			}
			switch typ {
			case screen.MsgHello:
				var h screen.Hello
				json.Unmarshal(b, &h)
				f.h.hellos <- h
			case screen.MsgFrame:
				var fr sentFrame
				json.Unmarshal(b, &fr)
				f.h.frames <- fr
			case screen.MsgCursor:
				var c screen.CursorShape
				json.Unmarshal(b, &c)
				f.h.shapes <- c
			case screen.MsgCursorPos:
				var c screen.CursorPos
				json.Unmarshal(b, &c)
				f.h.poses <- c
			}
		}
	}()
	r := &ScreenRunner{
		Serve: func(rw io.ReadWriteCloser, _ ssh.Signer, _ ssh.PublicKey) (InnerSession, error) {
			return fi, nil
		},
		NewSource:  func() (capture.Screen, error) { return f.scr, nil },
		NewEncoder: func() FrameEncoder { return f.enc },
		NewInjector: func(_ *slog.Logger, kick func()) Injector {
			f.inj.kick = kick
			return f.inj
		},
		Codec:  jsonCodec{},
		Hello:  screen.Hello{Hostname: "PC", UserMachine: "anna@PC", OS: "windows"},
		Pacing: pacing,
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	ch, other := net.Pipe()
	t.Cleanup(func() { other.Close(); cancel() })
	go func() { f.runErr <- r.Run(ctx, ch, nil) }()
	return f
}

func (f *runnerFixture) send(typ uint8, v any) {
	var b []byte
	if v != nil {
		b = mustJSON(v)
	}
	if err := f.h.w.WriteFrame(typ, b); err != nil {
		f.t.Fatal(err)
	}
}

func recvT[T any](t *testing.T, c <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(2 * time.Second):
		t.Fatalf("no %s", what)
		panic("unreachable")
	}
}

func noneT[T any](t *testing.T, c <-chan T, d time.Duration, what string) {
	t.Helper()
	select {
	case v := <-c:
		t.Fatalf("unexpected %s: %+v", what, v)
	case <-time.After(d):
	}
}

// changeLoop keeps changing the screen content until the test ends.
func (f *runnerFixture) changeLoop() {
	stop := make(chan struct{})
	f.t.Cleanup(func() { close(stop) })
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
				f.scr.set(func(s *fakeScreen) { s.content++ })
			}
		}
	}()
}

func (f *runnerFixture) end() {
	f.t.Helper()
	f.cancel()
	select {
	case err := <-f.runErr:
		if err != nil {
			f.t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		f.t.Fatal("Run did not return")
	}
}

var fast = Pacing{MaxFPS: 200, IdleInterval: 5 * time.Millisecond, MonitorCheck: time.Hour}

func TestRunnerHelloFlowControlAndAcks(t *testing.T) {
	f := startRunner(t, fast, func(f *runnerFixture) {
		f.scr.cursor.Pos = image.Pt(45, 0) // on the second monitor
	})
	h := recvT(t, f.h.hellos, "hello")
	if h.Hostname != "PC" || h.Current != 1 || h.Width != 20 || h.Height != 20 || len(h.Monitors) != 2 {
		t.Fatalf("hello %+v", h)
	}
	if m := h.Monitors[1]; m.X != 40 || m.Y != -10 || m.W != 20 || m.Name != "DISPLAY2" || !h.Monitors[0].Primary {
		t.Fatalf("monitors %+v", h.Monitors)
	}
	f.inj.get(func(j *fakeInjector) {
		if len(j.monitors) != 1 || j.monitors[0] != mon1.Rect {
			t.Errorf("injector monitors %v", j.monitors)
		}
	})
	f.changeLoop()
	if fr := recvT(t, f.h.frames, "frame"); fr.Seq != 1 || fr.W != 20 || fr.More {
		t.Fatalf("first frame %+v", fr)
	}
	if fr := recvT(t, f.h.frames, "frame"); fr.Seq != 2 || fr.W != 1 {
		t.Fatalf("second frame %+v", fr)
	}
	noneT(t, f.h.frames, 100*time.Millisecond, "frame with 2 unacked")
	f.send(screen.MsgAck, screen.Ack{Seq: 1})
	if fr := recvT(t, f.h.frames, "frame"); fr.Seq != 3 {
		t.Fatalf("frame %+v", fr)
	}
	noneT(t, f.h.frames, 50*time.Millisecond, "frame with 2 unacked")
	// Stale ack: nothing; cumulative ack: two more.
	f.send(screen.MsgAck, screen.Ack{Seq: 1})
	noneT(t, f.h.frames, 50*time.Millisecond, "frame after a stale ack")
	f.send(screen.MsgAck, screen.Ack{Seq: 3})
	a, b := recvT(t, f.h.frames, "frame"), recvT(t, f.h.frames, "frame")
	if a.Seq != 4 || b.Seq != 5 {
		t.Fatalf("frames %d %d", a.Seq, b.Seq)
	}
	f.end()
	f.enc.get(func(e *fakeEncoder) {
		// Acks of 1 and 3 reached the rate controller; 2 was acked
		// cumulatively without an RTT of its own.
		if len(e.acked) != 2 || e.acked[0] == 0 {
			t.Errorf("acked %v", e.acked)
		}
	})
	f.inj.get(func(j *fakeInjector) {
		if !j.closed {
			t.Error("injector not closed")
		}
	})
	f.scr.set(func(s *fakeScreen) {
		if !s.closed {
			t.Error("screen not closed")
		}
	})
}

func TestRunnerParts(t *testing.T) {
	f := startRunner(t, fast, func(f *runnerFixture) { f.enc.parts = 3 })
	recvT(t, f.h.hellos, "hello")
	f.changeLoop()
	// Two frames of three parts each go out before the first ack.
	for seq := uint32(1); seq <= 2; seq++ {
		for i := 0; i < 3; i++ {
			fr := recvT(t, f.h.frames, "part")
			if fr.Seq != seq || fr.More != (i < 2) {
				t.Fatalf("frame %d part %d: %+v", seq, i, fr)
			}
		}
	}
	noneT(t, f.h.frames, 100*time.Millisecond, "part with 2 unacked")
	f.send(screen.MsgAck, screen.Ack{Seq: 1})
	if fr := recvT(t, f.h.frames, "part"); fr.Seq != 3 {
		t.Fatalf("part %+v", fr)
	}
	f.end()
}

func TestRunnerRefreshSelectQuality(t *testing.T) {
	f := startRunner(t, fast, nil)
	if h := recvT(t, f.h.hellos, "hello"); h.Current != 0 {
		t.Fatalf("hello %+v", h)
	}
	if fr := recvT(t, f.h.frames, "frame"); fr.W != 40 {
		t.Fatalf("frame %+v", fr)
	}
	noneT(t, f.h.frames, 50*time.Millisecond, "frame of a static screen")

	// Refresh: a full frame without any change.
	f.send(screen.MsgAck, screen.Ack{Seq: 1})
	f.send(screen.MsgRefresh, nil)
	if fr := recvT(t, f.h.frames, "frame"); fr.W != 40 || fr.Seq != 2 {
		t.Fatalf("refresh frame %+v", fr)
	}

	// SelectMonitor: new Hello, the injector follows, full frame.
	f.send(screen.MsgAck, screen.Ack{Seq: 2})
	f.send(screen.MsgSelectMonitor, screen.SelectMonitor{Index: 1})
	if h := recvT(t, f.h.hellos, "hello"); h.Current != 1 || h.Width != 20 {
		t.Fatalf("hello %+v", h)
	}
	if fr := recvT(t, f.h.frames, "frame"); fr.W != 20 {
		t.Fatalf("frame %+v", fr)
	}
	// An index out of range is ignored.
	f.send(screen.MsgSelectMonitor, screen.SelectMonitor{Index: 7})
	noneT(t, f.h.hellos, 50*time.Millisecond, "hello for a bad index")

	f.send(screen.MsgQuality, screen.Quality{Mode: screen.QualitySharp})
	time.Sleep(50 * time.Millisecond)
	f.end()
	f.enc.get(func(e *fakeEncoder) {
		if e.quality != screen.QualitySharp {
			t.Errorf("quality %d", e.quality)
		}
		if e.resets < 3 {
			t.Errorf("%d resets", e.resets)
		}
	})
	f.inj.get(func(j *fakeInjector) {
		if len(j.monitors) != 2 || j.monitors[1] != mon1.Rect {
			t.Errorf("injector monitors %v", j.monitors)
		}
	})
	f.scr.set(func(s *fakeScreen) {
		if len(s.opened) != 2 || s.opened[1] != mon1 {
			t.Errorf("opened %v", s.opened)
		}
	})
}

func TestRunnerCursor(t *testing.T) {
	f := startRunner(t, fast, nil)
	recvT(t, f.h.hellos, "hello")
	s1 := recvT(t, f.h.shapes, "shape")
	p := recvT(t, f.h.poses, "pos")
	if p.X != 5 || p.Y != 6 || !p.Visible || p.ID != s1.ID || s1.W != 1 {
		t.Fatalf("shape %+v pos %+v", s1, p)
	}
	noneT(t, f.h.poses, 50*time.Millisecond, "pos without a change")

	// A new shape is sent once, with a position update.
	f.scr.set(func(s *fakeScreen) { s.cursor.Handle = 2 })
	s2 := recvT(t, f.h.shapes, "shape")
	if p := recvT(t, f.h.poses, "pos"); p.ID != s2.ID || s2.ID == s1.ID {
		t.Fatalf("pos %+v shapes %d %d", p, s1.ID, s2.ID)
	}
	// Back to a handle whose image the agent has: position only.
	f.scr.set(func(s *fakeScreen) { s.cursor.Handle = 3 })
	if p := recvT(t, f.h.poses, "pos"); p.ID != s1.ID {
		t.Fatalf("pos %+v", p)
	}
	noneT(t, f.h.shapes, 50*time.Millisecond, "known shape")

	// Moves are relative to the shared monitor; off it the cursor is hidden.
	f.scr.set(func(s *fakeScreen) { s.cursor.Pos = image.Pt(45, 2) })
	if p := recvT(t, f.h.poses, "pos"); p.X != 45 || p.Y != 2 || p.Visible {
		t.Fatalf("pos %+v", p)
	}
	f.scr.set(func(s *fakeScreen) { s.cursor.Pos = image.Pt(3, 3); s.cursor.Showing = false })
	if p := recvT(t, f.h.poses, "pos"); p.Visible {
		t.Fatalf("pos %+v", p)
	}

	// After switching monitors the shape is sent again (new Hello).
	f.scr.set(func(s *fakeScreen) { s.cursor.Showing = true; s.cursor.Pos = image.Pt(50, 0) })
	recvT(t, f.h.poses, "pos")
	f.send(screen.MsgSelectMonitor, screen.SelectMonitor{Index: 1})
	recvT(t, f.h.hellos, "hello")
	if s := recvT(t, f.h.shapes, "shape"); s.ID != s1.ID {
		t.Fatalf("shape %+v", s)
	}
	if p := recvT(t, f.h.poses, "pos"); p.X != 10 || p.Y != 10 || !p.Visible {
		t.Fatalf("pos %+v", p)
	}
	f.end()
}

func TestRunnerCursorFallback(t *testing.T) {
	f := startRunner(t, fast, func(f *runnerFixture) { f.scr.cursor.Handle = 99 })
	recvT(t, f.h.hellos, "hello")
	s := recvT(t, f.h.shapes, "shape")
	if s.ID != capture.ShapeID(capture.Arrow()) {
		t.Fatalf("shape %+v", s)
	}
	if p := recvT(t, f.h.poses, "pos"); !p.Visible || p.ID != s.ID {
		t.Fatalf("pos %+v", p)
	}
	f.end()
}

func TestRunnerMonitorChange(t *testing.T) {
	p := fast
	p.MonitorCheck = 10 * time.Millisecond
	f := startRunner(t, p, nil)
	recvT(t, f.h.hellos, "hello")
	recvT(t, f.h.frames, "frame")
	f.send(screen.MsgAck, screen.Ack{Seq: 1})
	// The resolution of the shared monitor changes and a monitor is added
	// before it, so the shared one moves in the list.
	big := capture.Monitor{Rect: image.Rect(0, 0, 50, 40), Primary: true, Name: "DISPLAY1"}
	extra := capture.Monitor{Rect: image.Rect(-10, 0, 0, 10), Name: "DISPLAY3"}
	f.scr.set(func(s *fakeScreen) { s.monitors = []capture.Monitor{extra, big, mon1} })
	h := recvT(t, f.h.hellos, "hello")
	if h.Current != 1 || h.Width != 50 || h.Height != 40 || len(h.Monitors) != 3 {
		t.Fatalf("hello %+v", h)
	}
	if fr := recvT(t, f.h.frames, "frame"); fr.W != 50 {
		t.Fatalf("frame %+v", fr)
	}
	f.end()
	f.inj.get(func(j *fakeInjector) {
		if j.monitors[len(j.monitors)-1] != big.Rect {
			t.Errorf("injector monitors %v", j.monitors)
		}
	})
}

func TestRunnerKickAndInput(t *testing.T) {
	p := Pacing{MaxFPS: 60, IdleInterval: 10 * time.Second, ActiveFor: time.Millisecond, MonitorCheck: time.Hour}
	f := startRunner(t, p, nil)
	recvT(t, f.h.hellos, "hello")
	recvT(t, f.h.frames, "frame")
	f.send(screen.MsgAck, screen.Ack{Seq: 1})
	time.Sleep(20 * time.Millisecond)
	f.scr.set(func(s *fakeScreen) { s.content = 9 })
	noneT(t, f.h.frames, 100*time.Millisecond, "frame before the idle interval")

	// Injected input kicks the capture.
	f.inputW.WriteFrame(4, []byte("x"))
	f.inj.kick()
	if fr := recvT(t, f.h.frames, "frame"); fr.Seq != 2 {
		t.Fatalf("frame %+v", fr)
	}
	f.end()
	f.inj.get(func(j *fakeInjector) {
		if len(j.inputs) != 1 || j.inputs[0] != 4 {
			t.Errorf("inputs %v", j.inputs)
		}
	})
}

func TestRunnerRefineDue(t *testing.T) {
	p := Pacing{MaxFPS: 60, IdleInterval: 10 * time.Second, ActiveFor: time.Millisecond, MonitorCheck: time.Hour}
	start := time.Now()
	f := startRunner(t, p, func(f *runnerFixture) { f.enc.refine = start.Add(300 * time.Millisecond) })
	recvT(t, f.h.hellos, "hello")
	recvT(t, f.h.frames, "frame")
	f.send(screen.MsgAck, screen.Ack{Seq: 1})
	// The content changes after the fast captures that follow a change are
	// over; with a 10 s idle interval only the refine deadline makes the
	// loop call Encode again.
	time.Sleep(50 * time.Millisecond)
	f.scr.set(func(s *fakeScreen) { s.content = 5 })
	recvT(t, f.h.frames, "refine frame")
	if d := time.Since(start); d < 290*time.Millisecond || d > 2*time.Second {
		t.Fatalf("refine after %v", d)
	}
	f.end()
}

func TestPacer(t *testing.T) {
	t0 := time.Unix(1000, 0)
	pc := newPacer(Pacing{})
	// Static screen: 5 Hz.
	pc.captured(t0)
	if got := pc.next(t0, time.Time{}); got != t0.Add(200*time.Millisecond) {
		t.Fatalf("idle next %v", got.Sub(t0))
	}
	// Changes: the encoder's target, capped at 60.
	pc.active(t0)
	if got := pc.next(t0, time.Time{}); got.Sub(t0) != time.Second/60 {
		t.Fatalf("active next %v", got.Sub(t0))
	}
	pc.setFPS(20)
	if got := pc.next(t0, time.Time{}); got.Sub(t0) != 50*time.Millisecond {
		t.Fatalf("20 fps next %v", got.Sub(t0))
	}
	pc.setFPS(500)
	if got := pc.next(t0, time.Time{}); got.Sub(t0) != time.Second/60 {
		t.Fatalf("capped next %v", got.Sub(t0))
	}
	// Back off once nothing changed for ActiveFor.
	t1 := t0.Add(600 * time.Millisecond)
	pc.captured(t1)
	if got := pc.next(t1, time.Time{}); got.Sub(t1) != 200*time.Millisecond {
		t.Fatalf("backed off next %v", got.Sub(t1))
	}
	// A refine due earlier wins; one already overdue means now.
	if got := pc.next(t1, t1.Add(30*time.Millisecond)); got.Sub(t1) != 30*time.Millisecond {
		t.Fatalf("refine next %v", got.Sub(t1))
	}
	if got := pc.next(t1, t0); got != t1 {
		t.Fatalf("overdue refine %v", got.Sub(t1))
	}
	// A kick right after a capture waits for the 60 fps slot, later it is
	// immediate, and it switches to the fast rate.
	if got := pc.kicked(t1.Add(time.Millisecond)); got != t1.Add(time.Second/60) {
		t.Fatalf("kick %v", got.Sub(t1))
	}
	t2 := t1.Add(time.Second)
	if got := pc.kicked(t2); got != t2 {
		t.Fatalf("late kick %v", got.Sub(t2))
	}
	pc.captured(t2)
	if got := pc.next(t2, time.Time{}); got.Sub(t2) != time.Second/60 {
		t.Fatalf("after kick %v", got.Sub(t2))
	}
}
