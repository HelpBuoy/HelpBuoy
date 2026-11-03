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

	"github.com/mevdschee/helpbuoy/proto/inner"
	"github.com/mevdschee/helpbuoy/proto/screen"
	"github.com/mevdschee/helpbuoy/proto/wire"
)

// InnerSession is the client's side of an inner SSH connection, as returned
// by inner.Serve.
type InnerSession interface {
	Screen() inner.Stream
	Input() inner.Stream
	Close() error
	Wait() error
}

// InnerServer starts the inner SSH server on the session channel.
type InnerServer func(rw io.ReadWriteCloser, hostKey ssh.Signer, helperKey ssh.PublicKey) (InnerSession, error)

// ServeInner is the InnerServer of proto/inner.
func ServeInner(rw io.ReadWriteCloser, hostKey ssh.Signer, helperKey ssh.PublicKey) (InnerSession, error) {
	s, err := inner.Serve(rw, hostKey, helperKey)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Codec renders and encodes the screen stream. ScreenCodec is the real one;
// tests use a fake so they don't depend on proto/screen.
type Codec interface {
	Render(dst *image.RGBA, t float64, label string)
	Diff(prev, cur *image.RGBA) []screen.Rect
	EncodeHello(screen.Hello) []byte
	EncodeFrame(screen.Frame) []byte
	DecodeAck([]byte) (screen.Ack, error)
}

// ScreenCodec is the phase 2 Codec: the proto/screen test pattern, tile diff
// and message encoding.
type ScreenCodec struct{}

func (ScreenCodec) Render(dst *image.RGBA, t float64, label string) {
	screen.TestPattern(dst, t, label)
}
func (ScreenCodec) Diff(prev, cur *image.RGBA) []screen.Rect { return screen.Diff(prev, cur) }
func (ScreenCodec) EncodeHello(h screen.Hello) []byte        { return screen.EncodeHello(h) }
func (ScreenCodec) EncodeFrame(f screen.Frame) []byte        { return screen.EncodeFrame(f) }
func (ScreenCodec) DecodeAck(b []byte) (screen.Ack, error)   { return screen.DecodeAck(b) }

// InputSink receives the helper's input messages as raw wire frames.
type InputSink interface {
	Input(typ uint8, payload []byte)
}

// Pattern size limits. Phase 2 frames carry raw RGBA tiles and a wire frame
// holds at most 16 MiB, so the test pattern never exceeds 1920x1080.
const (
	DefaultWidth, DefaultHeight = 1280, 720
	MaxWidth, MaxHeight         = 1920, 1080
)

// PatternRunner is the phase 2 Runner: it streams the test pattern instead
// of the real screen and hands input to Sink without injecting it.
type PatternRunner struct {
	HostKey ssh.Signer   // the app key, host key of the inner server
	Serve   InnerServer  // default ServeInner
	Codec   Codec        // default ScreenCodec
	Sink    InputSink    // may be nil
	Hello   screen.Hello // Width and Height set the pattern size (default 1280x720, at most 1920x1080)
	Label   string       // drawn into the pattern, usually user@machine
	FPS     int          // default 30
	Logger  *slog.Logger
}

// Run implements Runner.
func (r *PatternRunner) Run(ctx context.Context, ch io.ReadWriteCloser, helperKey ssh.PublicKey) error {
	serve := r.Serve
	if serve == nil {
		serve = ServeInner
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Unblock the handshake if the session is ended while it runs.
	stop := context.AfterFunc(ctx, func() { ch.Close() })
	defer stop()

	s, err := serve(ch, r.HostKey, helperKey)
	if err != nil {
		return fmt.Errorf("inner handshake: %w", err)
	}
	defer s.Close()
	context.AfterFunc(ctx, func() { s.Close() })
	go func() {
		s.Wait()
		cancel()
	}()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.readInput(s.Input())
	}()
	err = r.stream(ctx, s.Screen())
	cancel()
	s.Close()
	wg.Wait()
	if ctx.Err() != nil {
		return nil // ended on purpose or by the other side
	}
	return err
}

func (r *PatternRunner) log() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

// readInput passes input frames to the sink until the stream ends.
func (r *PatternRunner) readInput(st inner.Stream) {
	fr := wire.NewReader(st)
	for {
		typ, payload, err := fr.ReadFrame()
		if err != nil {
			return
		}
		if r.Sink != nil {
			r.Sink.Input(typ, payload)
		}
	}
}

// stream sends Hello and then frames at up to FPS, never more than
// screen.MaxUnacked ahead of the helper's acks.
func (r *PatternRunner) stream(ctx context.Context, st inner.Stream) error {
	codec := r.Codec
	if codec == nil {
		codec = ScreenCodec{}
	}
	fps := r.FPS
	if fps <= 0 {
		fps = 30
	}
	hello := r.Hello
	if hello.Width <= 0 || hello.Height <= 0 {
		hello.Width, hello.Height = DefaultWidth, DefaultHeight
	}
	hello.Width, hello.Height = capSize(hello.Width, hello.Height)
	w := wire.NewWriter(st)
	if err := w.WriteFrame(screen.MsgHello, codec.EncodeHello(hello)); err != nil {
		return err
	}

	var win screen.Window
	refresh := make(chan struct{}, 1)
	readErr := make(chan error, 1)
	go func() {
		fr := wire.NewReader(st)
		for {
			typ, payload, err := fr.ReadFrame()
			if err != nil {
				readErr <- err
				return
			}
			switch typ {
			case screen.MsgAck:
				a, err := codec.DecodeAck(payload)
				if err != nil {
					readErr <- err
					return
				}
				win.Acked(a.Seq) // cumulative; stale or bogus acks are ignored
			case screen.MsgRefresh:
				select {
				case refresh <- struct{}{}:
				default:
				}
			}
			// Later phases add messages; ignore unknown ones here.
		}
	}()

	bounds := image.Rect(0, 0, hello.Width, hello.Height)
	bufs := [2]*image.RGBA{image.NewRGBA(bounds), image.NewRGBA(bounds)}
	curIdx, havePrev := 0, false
	var seq uint32
	start := time.Now()
	tick := time.NewTicker(time.Second / time.Duration(fps))
	defer tick.Stop()
	frames := 0

	for {
		select {
		case <-ctx.Done():
			r.log().Info("screen stream ended", "frames", frames)
			return nil
		case err := <-readErr:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case <-refresh:
			havePrev = false
			continue
		case <-tick.C:
		}
		if !win.CanSend() {
			continue // wait for an ack; the next tick checks again
		}
		cur := bufs[curIdx]
		var prev *image.RGBA
		if havePrev {
			prev = bufs[1-curIdx]
		}
		codec.Render(cur, time.Since(start).Seconds(), r.Label)
		rects := codec.Diff(prev, cur)
		if len(rects) == 0 {
			continue
		}
		seq++
		win.Sent(seq)
		if err := w.WriteFrame(screen.MsgFrame, codec.EncodeFrame(screen.Frame{Seq: seq, Rects: rects})); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		frames++
		havePrev = true
		curIdx = 1 - curIdx
	}
}

// capSize scales w x h down to fit MaxWidth x MaxHeight, keeping the aspect
// ratio.
func capSize(w, h int) (int, int) {
	if w <= MaxWidth && h <= MaxHeight {
		return w, h
	}
	if w*MaxHeight > h*MaxWidth { // wider than 16:9
		return MaxWidth, max(1, h*MaxWidth/w)
	}
	return max(1, w*MaxHeight/h), MaxHeight
}
