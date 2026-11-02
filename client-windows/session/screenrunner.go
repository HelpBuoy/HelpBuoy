package session

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/help-me-win/client-windows/capture"
	"github.com/mevdschee/help-me-win/proto/encode"
	"github.com/mevdschee/help-me-win/proto/inner"
	"github.com/mevdschee/help-me-win/proto/screen"
	"github.com/mevdschee/help-me-win/proto/wire"
)

// FrameEncoder turns captures into frames; *encode.Encoder implements it.
type FrameEncoder interface {
	Encode(cur *image.RGBA, now time.Time) []screen.Frame
	RefineDue() time.Time
	Reset()
	SetQuality(screen.QualityMode)
	Acked(bytes int, rtt time.Duration)
	Stats() encode.Stats
}

// Injector injects the helper's input; *injector.Injector implements it.
type Injector interface {
	InputSink
	SetMonitor(r image.Rectangle)
	Close() error
}

// MessageCodec encodes and decodes the screen channel's messages.
// ScreenMessages is the real one; tests use a fake.
type MessageCodec interface {
	EncodeHello(screen.Hello) []byte
	EncodeFrame(screen.Frame) []byte
	EncodeCursorShape(screen.CursorShape) []byte
	EncodeCursorPos(screen.CursorPos) []byte
	DecodeAck([]byte) (screen.Ack, error)
	DecodeSelectMonitor([]byte) (screen.SelectMonitor, error)
	DecodeQuality([]byte) (screen.Quality, error)
}

// ScreenMessages is the MessageCodec of proto/screen.
type ScreenMessages struct{}

func (ScreenMessages) EncodeHello(h screen.Hello) []byte { return screen.EncodeHello(h) }
func (ScreenMessages) EncodeFrame(f screen.Frame) []byte { return screen.EncodeFrame(f) }
func (ScreenMessages) EncodeCursorShape(c screen.CursorShape) []byte {
	return screen.EncodeCursorShape(c)
}
func (ScreenMessages) EncodeCursorPos(c screen.CursorPos) []byte { return screen.EncodeCursorPos(c) }
func (ScreenMessages) DecodeAck(b []byte) (screen.Ack, error)    { return screen.DecodeAck(b) }
func (ScreenMessages) DecodeSelectMonitor(b []byte) (screen.SelectMonitor, error) {
	return screen.DecodeSelectMonitor(b)
}
func (ScreenMessages) DecodeQuality(b []byte) (screen.Quality, error) {
	return screen.DecodeQuality(b)
}

// Pacing sets the adaptive capture rate (PLAN.md 5.2). Zero fields pick the
// defaults in parentheses.
type Pacing struct {
	MaxFPS       int           // ceiling for the encoder's target fps (60)
	IdleInterval time.Duration // capture interval while nothing changes (200 ms, 5 Hz)
	ActiveFor    time.Duration // keep the fast rate this long after the last change (500 ms)
	MonitorCheck time.Duration // how often the monitor layout is checked (1 s)
}

func (p Pacing) withDefaults() Pacing {
	if p.MaxFPS <= 0 {
		p.MaxFPS = 60
	}
	if p.IdleInterval <= 0 {
		p.IdleInterval = 200 * time.Millisecond
	}
	if p.ActiveFor <= 0 {
		p.ActiveFor = 500 * time.Millisecond
	}
	if p.MonitorCheck <= 0 {
		p.MonitorCheck = time.Second
	}
	return p
}

// pacer decides when to capture next. It only does arithmetic on the times
// it is given, so it is tested with a fake clock.
type pacer struct {
	p           Pacing
	fps         int // target from the encoder, 0 = MaxFPS
	lastActive  time.Time
	lastCapture time.Time
}

func newPacer(p Pacing) *pacer { return &pacer{p: p.withDefaults()} }

// setFPS takes the encoder's target frame rate.
func (pc *pacer) setFPS(fps int) { pc.fps = fps }

// fastInterval is the capture interval while the screen changes.
func (pc *pacer) fastInterval() time.Duration {
	fps := pc.fps
	if fps <= 0 || fps > pc.p.MaxFPS {
		fps = pc.p.MaxFPS
	}
	return time.Second / time.Duration(fps)
}

// active records something that makes more changes likely: new damage,
// cursor movement or injected input.
func (pc *pacer) active(now time.Time) { pc.lastActive = now }

// captured records a capture at now.
func (pc *pacer) captured(now time.Time) { pc.lastCapture = now }

// next returns when to capture after a capture at now: soon while things
// change, at the idle rate otherwise, and no later than refine (zero for
// none).
func (pc *pacer) next(now, refine time.Time) time.Time {
	iv := pc.p.IdleInterval
	if !pc.lastActive.IsZero() && now.Sub(pc.lastActive) < pc.p.ActiveFor {
		iv = min(iv, pc.fastInterval())
	}
	t := now.Add(iv)
	if !refine.IsZero() && refine.Before(t) {
		t = refine
		if t.Before(now) {
			t = now
		}
	}
	return t
}

// kicked returns when to capture after injected input at now: right away,
// but never faster than MaxFPS.
func (pc *pacer) kicked(now time.Time) time.Time {
	pc.active(now)
	t := pc.lastCapture.Add(time.Second / time.Duration(pc.p.MaxFPS))
	if t.Before(now) {
		return now
	}
	return t
}

// ScreenRunner is the Runner of real sessions: it shares a monitor of the
// local screen through the encoder, sends the cursor, and injects the
// helper's input (PLAN.md 5 and 6.4).
type ScreenRunner struct {
	HostKey ssh.Signer  // the app key, host key of the inner server
	Serve   InnerServer // default ServeInner
	// NewSource opens the local screen for one session.
	NewSource func() (capture.Screen, error)
	// NewEncoder returns the encoder of one session (encode.New with SwapRB
	// for GDI captures).
	NewEncoder func() FrameEncoder
	// NewInjector returns the injector of one session; onInjected kicks the
	// capture loop. Nil discards input.
	NewInjector func(log *slog.Logger, onInjected func()) Injector
	Codec       MessageCodec // default ScreenMessages
	// Hello carries Hostname, UserMachine and OS; sizes and monitors are
	// filled in per monitor.
	Hello screen.Hello
	// PerformanceMode, if set and returning true at session start, calls
	// EnablePerformance and restores the settings at the end.
	PerformanceMode   func() bool
	EnablePerformance func(log *slog.Logger) (restore func())
	Pacing            Pacing
	Logger            *slog.Logger
}

func (r *ScreenRunner) log() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

// Run implements Runner.
func (r *ScreenRunner) Run(ctx context.Context, ch io.ReadWriteCloser, helperKey ssh.PublicKey) error {
	if r.NewSource == nil || r.NewEncoder == nil {
		ch.Close()
		return errors.New("session: screen runner without source or encoder")
	}
	serve := r.Serve
	if serve == nil {
		serve = ServeInner
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { ch.Close() })
	defer stop()

	s, err := serve(ch, r.HostKey, helperKey)
	if err != nil {
		return fmt.Errorf("inner handshake: %w", err)
	}
	defer s.Close()
	go func() {
		s.Wait()
		cancel()
	}()

	src, err := r.NewSource()
	if err != nil {
		return fmt.Errorf("screen capture: %w", err)
	}
	defer src.Close()

	if r.PerformanceMode != nil && r.EnablePerformance != nil && r.PerformanceMode() {
		restore := r.EnablePerformance(r.log())
		defer restore()
	}

	kicks := make(chan struct{}, 1)
	kick := func() {
		select {
		case kicks <- struct{}{}:
		default:
		}
	}
	var inj Injector
	if r.NewInjector != nil {
		inj = r.NewInjector(r.log(), kick)
	}
	// Stopping blocks input first (releasing held keys), then closes the
	// inner connection, which also ends the capture loop's writes.
	var closeInj sync.Once
	stopInput := func() {
		closeInj.Do(func() {
			if inj != nil {
				inj.Close()
			}
		})
	}
	defer stopInput()
	context.AfterFunc(ctx, func() {
		stopInput()
		s.Close()
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		readInputTo(s.Input(), inj)
	}()

	codec := r.Codec
	if codec == nil {
		codec = ScreenMessages{}
	}
	st := &streamer{
		log:     r.log(),
		src:     src,
		enc:     r.NewEncoder(),
		inj:     inj,
		codec:   codec,
		hello:   r.Hello,
		pace:    newPacer(r.Pacing),
		kicks:   kicks,
		sent:    make(map[uint32]bool),
		monitor: -1,
	}
	err = st.run(ctx, s.Screen())
	stopInput()
	cancel()
	s.Close()
	wg.Wait()
	st.closeCapturer()
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// readInputTo passes input frames to sink (which may be nil) until the
// stream ends.
func readInputTo(st inner.Stream, sink InputSink) {
	fr := wire.NewReader(st)
	for {
		typ, payload, err := fr.ReadFrame()
		if err != nil {
			return
		}
		if sink != nil {
			sink.Input(typ, payload)
		}
	}
}

// helperMsg is a decoded message from the helper on the screen channel.
type helperMsg struct {
	typ uint8
	ack screen.Ack
	sel screen.SelectMonitor
	q   screen.Quality
}

// inflightFrame remembers the size of a sent frame for the rate controller.
type inflightFrame struct {
	seq   uint32
	bytes int
}

// streamer is the capture and send loop of one session. All its fields are
// used by the loop goroutine only.
type streamer struct {
	log   *slog.Logger
	src   capture.Screen
	enc   FrameEncoder
	inj   Injector
	codec MessageCodec
	hello screen.Hello
	pace  *pacer
	kicks <-chan struct{}
	w     *wire.Writer

	monitors []capture.Monitor
	monitor  int // index into monitors, -1 before the first Hello
	capt     capture.Capturer
	nextMon  time.Time // next monitor layout check

	win      screen.Window
	seq      uint32
	inflight []inflightFrame
	pending  bool // a capture was due while the window was full

	// Cursor state.
	sent       map[uint32]bool // shape IDs the helper has
	lastHandle uintptr
	shapeID    uint32
	lastPos    screen.CursorPos
	posSent    bool

	frames    int
	captErrAt time.Time // last logged capture error
}

func (st *streamer) closeCapturer() {
	if st.capt != nil {
		st.capt.Close()
		st.capt = nil
	}
}

func (st *streamer) run(ctx context.Context, stream inner.Stream) error {
	st.w = wire.NewWriter(stream)
	msgs := make(chan helperMsg, 16)
	readErr := make(chan error, 1)
	go st.readHelper(stream, msgs, readErr)

	ms, err := st.src.Monitors()
	if err != nil {
		return err
	}
	var at *image.Point
	if ci, err := st.src.Cursor(); err == nil {
		at = &ci.Pos
	}
	if err := st.share(ms, capture.DefaultMonitor(ms, at), time.Now()); err != nil {
		return st.writeErr(ctx, err)
	}

	due := time.Now()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		timer.Reset(time.Until(due))
		select {
		case <-ctx.Done():
			st.log.Info("screen stream ended", "frames", st.frames)
			return nil
		case err := <-readErr:
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return err
		case m := <-msgs:
			now := time.Now()
			capNow, err := st.handle(m, now)
			if err != nil {
				return st.writeErr(ctx, err)
			}
			if capNow || (st.pending && st.win.CanSend()) {
				due = now
			}
		case <-st.kicks:
			if k := st.pace.kicked(time.Now()); k.Before(due) {
				due = k
			}
		case <-timer.C:
			now := time.Now()
			if err := st.tick(now); err != nil {
				return st.writeErr(ctx, err)
			}
			due = st.pace.next(now, st.enc.RefineDue())
		}
	}
}

// writeErr turns a write error after the session was ended into nil.
func (st *streamer) writeErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// readHelper decodes the helper's messages until the stream ends.
func (st *streamer) readHelper(stream inner.Stream, msgs chan<- helperMsg, errc chan<- error) {
	fr := wire.NewReader(stream)
	for {
		typ, payload, err := fr.ReadFrame()
		if err != nil {
			errc <- err
			return
		}
		m := helperMsg{typ: typ}
		switch typ {
		case screen.MsgAck:
			m.ack, err = st.codec.DecodeAck(payload)
		case screen.MsgRefresh:
		case screen.MsgSelectMonitor:
			m.sel, err = st.codec.DecodeSelectMonitor(payload)
		case screen.MsgQuality:
			m.q, err = st.codec.DecodeQuality(payload)
		default:
			continue // unknown messages from newer helpers are ignored
		}
		if err != nil {
			errc <- fmt.Errorf("screen message %d: %w", typ, err)
			return
		}
		msgs <- m
	}
}

// handle applies one helper message. It reports whether to capture now.
func (st *streamer) handle(m helperMsg, now time.Time) (bool, error) {
	switch m.typ {
	case screen.MsgAck:
		rtt, ok := st.win.Acked(m.ack.Seq)
		n := 0
		for n < len(st.inflight) && int32(st.inflight[n].seq-m.ack.Seq) <= 0 {
			if ok && st.inflight[n].seq == m.ack.Seq {
				st.enc.Acked(st.inflight[n].bytes, rtt)
				st.pace.setFPS(st.enc.Stats().TargetFPS)
			}
			n++
		}
		st.inflight = append(st.inflight[:0], st.inflight[n:]...)
		return false, nil
	case screen.MsgRefresh:
		st.log.Debug("refresh requested")
		st.enc.Reset()
		st.forgetCursor()
		return true, nil
	case screen.MsgSelectMonitor:
		ms, err := st.src.Monitors()
		if err != nil {
			st.log.Warn("monitors not listed", "err", err)
			return false, nil
		}
		// The index refers to the list of the last Hello.
		i := m.sel.Index
		if i < 0 || i >= len(st.monitors) {
			st.log.Warn("helper selected an unknown monitor", "index", i)
			return false, nil
		}
		want := st.monitors[i]
		return true, st.share(ms, capture.FindMonitor(ms, want.Name, want.Rect), now)
	case screen.MsgQuality:
		st.enc.SetQuality(m.q.Mode)
		return false, nil
	}
	return false, nil
}

// share starts sharing ms[i]: it opens a capturer, resets the encoder and
// sends a new Hello.
func (st *streamer) share(ms []capture.Monitor, i int, now time.Time) error {
	if i < 0 || i >= len(ms) {
		return errors.New("session: no monitor to share")
	}
	st.closeCapturer()
	c, err := st.src.Open(ms[i])
	if err != nil {
		st.log.Warn("monitor capture not opened", "monitor", ms[i].Name, "err", err)
	}
	st.capt = c
	st.monitors, st.monitor = ms, i
	st.nextMon = now.Add(st.pace.p.MonitorCheck)
	st.enc.Reset()
	st.forgetCursor()
	m := ms[i]
	h := st.hello
	h.Width, h.Height = m.Rect.Dx(), m.Rect.Dy()
	h.Monitors = make([]screen.Monitor, len(ms))
	for j, mm := range ms {
		h.Monitors[j] = screen.Monitor{
			X: mm.Rect.Min.X, Y: mm.Rect.Min.Y, W: mm.Rect.Dx(), H: mm.Rect.Dy(),
			Primary: mm.Primary, Name: mm.Name,
		}
	}
	h.Current = i
	st.log.Info("sharing monitor", "index", i, "name", m.Name, "rect", m.Rect)
	if st.inj != nil {
		st.inj.SetMonitor(m.Rect)
	}
	if err := st.w.WriteFrame(screen.MsgHello, st.codec.EncodeHello(h)); err != nil {
		return err
	}
	st.pace.active(now)
	return nil
}

// forgetCursor makes the next tick send the cursor shape and position again.
func (st *streamer) forgetCursor() {
	clear(st.sent)
	st.lastHandle = 0
	st.shapeID = 0
	st.posSent = false
}

// tick is one scheduled capture: monitor layout check, cursor and, if the
// window allows, a frame.
func (st *streamer) tick(now time.Time) error {
	if !now.Before(st.nextMon) {
		st.nextMon = now.Add(st.pace.p.MonitorCheck)
		ms, err := st.src.Monitors()
		cur := st.monitors[st.monitor]
		switch {
		case err != nil:
		case !capture.SameMonitors(ms, st.monitors):
			st.log.Info("monitor layout changed", "monitors", len(ms))
			if err := st.share(ms, capture.FindMonitor(ms, cur.Name, cur.Rect), now); err != nil {
				return err
			}
		case st.capt == nil:
			// Opening failed before; the helper already has the Hello.
			if c, err := st.src.Open(cur); err == nil {
				st.capt = c
				st.enc.Reset()
			}
		}
	}
	if err := st.cursor(now); err != nil {
		return err
	}
	if !st.win.CanSend() {
		st.pending = true // the next ack captures right away
		return nil
	}
	st.pending = false
	if st.capt == nil {
		return nil
	}
	img, err := st.capt.Capture()
	st.pace.captured(now)
	if err != nil {
		// Typically the secure desktop (UAC, lock screen) or a mode change
		// in progress; check the layout soon and keep trying slowly.
		if now.Sub(st.captErrAt) > 10*time.Second {
			st.log.Warn("capture failed", "err", err)
			st.captErrAt = now
		}
		st.nextMon = now
		return nil
	}
	parts := st.enc.Encode(img, now)
	if len(parts) == 0 {
		return nil
	}
	st.pace.active(now)
	return st.send(parts)
}

// send numbers and writes the parts of one frame; only the last one counts
// for the window.
func (st *streamer) send(parts []screen.Frame) error {
	st.seq++
	bytes := 0
	for i := range parts {
		f := parts[i]
		f.Seq = st.seq
		f.More = i < len(parts)-1
		b := st.codec.EncodeFrame(f)
		bytes += len(b)
		if !f.More {
			st.win.Sent(st.seq)
			st.inflight = append(st.inflight, inflightFrame{seq: st.seq, bytes: bytes})
		}
		if err := st.w.WriteFrame(screen.MsgFrame, b); err != nil {
			return err
		}
	}
	st.frames++
	return nil
}

// cursor sends the cursor shape when it changed to one the helper doesn't
// have, and the position when position, visibility or shape changed.
func (st *streamer) cursor(now time.Time) error {
	ci, err := st.src.Cursor()
	if err != nil {
		return nil // e.g. on the secure desktop; try again next tick
	}
	if ci.Handle != 0 && ci.Handle != st.lastHandle {
		st.lastHandle = ci.Handle
		sh, err := st.src.CursorShape(ci.Handle)
		if err != nil && st.shapeID == 0 {
			sh, err = capture.Arrow(), nil // better than no cursor at all
		}
		if err != nil {
			st.log.Debug("cursor shape not read", "err", err)
		} else {
			id := capture.ShapeID(sh)
			if !st.sent[id] {
				msg := screen.CursorShape{ID: id, HotX: sh.HotX, HotY: sh.HotY, W: sh.W, H: sh.H, RGBA: sh.RGBA}
				if err := st.w.WriteFrame(screen.MsgCursor, st.codec.EncodeCursorShape(msg)); err != nil {
					return err
				}
				st.sent[id] = true
			}
			st.shapeID = id
		}
	}
	m := st.monitors[st.monitor].Rect
	pos := screen.CursorPos{
		X:       ci.Pos.X - m.Min.X,
		Y:       ci.Pos.Y - m.Min.Y,
		ID:      st.shapeID,
		Visible: ci.Showing && ci.Handle != 0 && st.shapeID != 0 && ci.Pos.In(m),
	}
	if st.posSent && pos == st.lastPos {
		return nil
	}
	if st.posSent && (pos.X != st.lastPos.X || pos.Y != st.lastPos.Y) {
		st.pace.active(now)
	}
	if err := st.w.WriteFrame(screen.MsgCursorPos, st.codec.EncodeCursorPos(pos)); err != nil {
		return err
	}
	st.lastPos, st.posSent = pos, true
	return nil
}
