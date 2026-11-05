// Package session is the techie's side of one remote session: it checks the
// session the server hands over against the registered computer, runs the inner
// SSH client (proto/inner), applies screen frames to an *image.RGBA, acks
// them, and sends input messages.
//
// The stream handling (Run) works on plain io streams and a Codec, so it can
// be tested with pipes and a fake codec. Dial wires it to proto/inner and the
// real proto/screen and proto/input encodings.
package session

import (
	"errors"
	"fmt"
	"image"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/proto/inner"
	"github.com/helpbuoy/helpbuoy/proto/input"
	"github.com/helpbuoy/helpbuoy/proto/screen"
	"github.com/helpbuoy/helpbuoy/proto/wire"
)

// MaxSide bounds the remote screen size accepted from a computer.
const MaxSide = 16384

// Info identifies the computer of a session after Verify.
type Info struct {
	RequestID    string
	ComputerName string // registered user@machine
	UserMachine  string // as the computer currently reports it to the server
	ComputerKey  ssh.PublicKey
	Fingerprint  string
}

// Verify checks a session the server hands over: the techie must have opened
// the request, and ComputerKey must be the registered key of ComputerName in the
// last Presence. The server is not trusted for this; the inner handshake then
// pins the returned key.
func Verify(d control.TechiesessionData, p *control.Presence, opened bool) (Info, error) {
	if !opened {
		return Info{}, fmt.Errorf("session for request %q, which this techie did not open", d.RequestID)
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(d.ComputerKey))
	if err != nil {
		return Info{}, fmt.Errorf("session computer key: %w", err)
	}
	if key.Type() != ssh.KeyAlgoED25519 {
		return Info{}, fmt.Errorf("session computer key is %s, not ssh-ed25519", key.Type())
	}
	fp := ssh.FingerprintSHA256(key)
	if p == nil {
		return Info{}, errors.New("no computer list from the server yet")
	}
	for _, c := range p.Computers {
		if c.Name != d.ComputerName {
			continue
		}
		if c.Fingerprint != fp {
			return Info{}, fmt.Errorf("the server offered key %s for %s, but %s is registered", fp, d.ComputerName, c.Fingerprint)
		}
		return Info{RequestID: d.RequestID, ComputerName: d.ComputerName, UserMachine: d.UserMachine, ComputerKey: key, Fingerprint: fp}, nil
	}
	return Info{}, fmt.Errorf("%s is not a registered computer", d.ComputerName)
}

// Codec encodes and decodes the inner stream messages.
type Codec interface {
	DecodeHello([]byte) (screen.Hello, error)
	DecodeFrame([]byte) (screen.Frame, error)
	DecodeCursorShape([]byte) (screen.CursorShape, error)
	DecodeCursorPos([]byte) (screen.CursorPos, error)
	EncodeAck(screen.Ack) []byte
	EncodeSelectMonitor(screen.SelectMonitor) []byte
	EncodeQuality(screen.Quality) []byte
	EncodeInput(m any) (typ uint8, payload []byte, err error)
	// NewDecoder returns the frame decoder for one session.
	NewDecoder() Decoder
}

// Decoder applies frame parts to an image; screen.Decoder implements it.
// It is used by one goroutine only.
type Decoder interface {
	// Apply applies one frame part. For a part with More set it only buffers
	// and returns complete=false; for the last part it returns the dirty
	// rectangles. On error dst may be partly updated.
	Apply(dst *image.RGBA, f screen.Frame) (dirty []image.Rectangle, complete bool, err error)
	// Reset forgets the tile cache and buffered parts.
	Reset()
}

// ProtoCodec is the real encoding from proto/screen and proto/input.
type ProtoCodec struct{}

func (ProtoCodec) DecodeHello(b []byte) (screen.Hello, error) { return screen.DecodeHello(b) }
func (ProtoCodec) DecodeFrame(b []byte) (screen.Frame, error) { return screen.DecodeFrame(b) }
func (ProtoCodec) DecodeCursorShape(b []byte) (screen.CursorShape, error) {
	return screen.DecodeCursorShape(b)
}
func (ProtoCodec) DecodeCursorPos(b []byte) (screen.CursorPos, error) {
	return screen.DecodeCursorPos(b)
}
func (ProtoCodec) EncodeAck(a screen.Ack) []byte { return screen.EncodeAck(a) }
func (ProtoCodec) EncodeSelectMonitor(m screen.SelectMonitor) []byte {
	return screen.EncodeSelectMonitor(m)
}
func (ProtoCodec) EncodeQuality(q screen.Quality) []byte    { return screen.EncodeQuality(q) }
func (ProtoCodec) EncodeInput(m any) (uint8, []byte, error) { return input.Encode(m) }
func (ProtoCodec) NewDecoder() Decoder                      { return screen.NewDecoder() }

// maxShapes bounds the cursor shape cache; the oldest shape is dropped first.
const maxShapes = 256

// moveInterval is the coalescing interval for mouse moves (PLAN.md 5.5).
const moveInterval = 4 * time.Millisecond

// Session is one running session. Its methods are meant for the UI thread,
// except where noted.
//
// Frames are decoded on the screen reader goroutine into a private image
// (the decoder needs the previous pixels for copies), and the dirty rects of
// each complete frame are then copied into the shared image under mu. The UI
// thread uploads from the shared image in Update. So neither side waits for
// the other longer than a memory copy of the changed pixels.
type Session struct {
	Info Info

	codec Codec
	input chan any
	done  chan struct{}
	once  sync.Once
	err   error

	closeMu sync.Mutex
	closer  io.Closer
	rtt     func() time.Duration

	// Reader goroutine only.
	dec      Decoder
	back     *image.RGBA
	skipping bool // dropping the remaining parts of a frame that failed
	skipSeq  uint32
	// awaitFull is set after a frame failed and a Refresh was requested:
	// frames still in flight were encoded against the old cache, so they are
	// acked but not applied until the computer's Full frame arrives.
	awaitFull   bool
	fullSeq     uint32 // Seq of the Full frame being received in parts
	fullStarted bool   // a Full frame's earlier parts were already passed to the decoder

	// Messages to the computer on the screen channel.
	acks  chan uint32
	kick  chan struct{}
	ctlMu sync.Mutex
	ctl   requests

	mu         sync.Mutex
	img        *image.RGBA
	hello      screen.Hello
	helloOK    bool
	dirty      []image.Rectangle
	resized    bool
	cursor     screen.CursorPos
	cursorOK   bool
	shapes     map[uint32]*screen.CursorShape
	shapeOrder []uint32
	lastErr    error // last decode error that led to a Refresh

	frames    atomic.Int64
	bytes     atomic.Int64
	refreshes atomic.Int64
	dropped   atomic.Int64

	// UI thread only.
	quality screen.QualityMode
	statsAt time.Time
	statsN  int64
	statsB  int64
	fps     float64
	mbps    float64
}

// requests holds the pending techie-to-computer requests, merged so the UI
// thread never blocks: only the latest monitor choice and quality matter.
type requests struct {
	refresh bool
	monitor *screen.SelectMonitor
	quality *screen.Quality
}

func newSession(info Info, codec Codec, closer io.Closer) *Session {
	return &Session{
		Info:    info,
		codec:   codec,
		input:   make(chan any, 1024),
		done:    make(chan struct{}),
		closer:  closer,
		dec:     codec.NewDecoder(),
		acks:    make(chan uint32, 8),
		kick:    make(chan struct{}, 1),
		shapes:  map[uint32]*screen.CursorShape{},
		statsAt: time.Now(),
	}
}

// Dial starts the inner SSH client on the server's session channel ch, with
// the techie's key and the verified computer key. It returns at once; the
// handshake runs in the background and its failure ends the session.
func Dial(ch io.ReadWriteCloser, signer ssh.Signer, info Info) *Session {
	s := newSession(info, ProtoCodec{}, ch)
	go func() {
		cs, err := inner.Dial(ch, signer, info.ComputerKey)
		if err != nil {
			ch.Close()
			s.finish(fmt.Errorf("inner connection: %w", err))
			return
		}
		s.closeMu.Lock()
		s.closer = cs
		s.rtt = cs.RTT
		s.closeMu.Unlock()
		select {
		case <-s.done: // closed during the handshake
			cs.Close()
			return
		default:
		}
		s.start(cs.Screen(), cs.Input())
	}()
	return s
}

// Run starts a session on already open streams: screenRW carries screen
// messages in and acks and requests out, inputW carries input messages out.
// closer ends the whole connection. It returns at once.
func Run(info Info, screenRW io.ReadWriter, inputW io.Writer, closer io.Closer, codec Codec) *Session {
	s := newSession(info, codec, closer)
	s.start(screenRW, inputW)
	return s
}

func (s *Session) start(screenRW io.ReadWriter, inputW io.Writer) {
	go func() { s.finish(s.readScreen(screenRW)) }()
	go s.writeScreen(screenRW)
	go s.writeInput(inputW)
}

// finish ends the session once with err (nil for a local Close).
func (s *Session) finish(err error) {
	s.once.Do(func() {
		s.err = err
		close(s.done)
		s.closeMu.Lock()
		c := s.closer
		s.closeMu.Unlock()
		if c != nil {
			c.Close()
		}
	})
}

// Close ends the session. Safe from any goroutine.
func (s *Session) Close() { s.finish(nil) }

// Done is closed when the session has ended, by either side.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err returns why the session ended: nil after Close or a clean end of the
// stream by the computer.
func (s *Session) Err() error {
	select {
	case <-s.done:
		return s.err
	default:
		return nil
	}
}

// readScreen reads the computer's screen messages and applies them.
// Malformed messages end the session; a frame that decodes but can't be
// applied (a bad rect, an unknown cache slot) only asks for a Refresh.
func (s *Session) readScreen(rw io.ReadWriter) error {
	r := wire.NewReader(rw)
	for {
		typ, b, err := r.ReadFrame()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			select {
			case <-s.done:
				return nil
			default:
			}
			return err
		}
		s.bytes.Add(int64(len(b)) + 5)
		switch typ {
		case screen.MsgHello:
			h, err := s.codec.DecodeHello(b)
			if err != nil {
				return fmt.Errorf("screen hello: %w", err)
			}
			if err := s.setHello(h); err != nil {
				return err
			}
		case screen.MsgFrame:
			f, err := s.codec.DecodeFrame(b)
			if err != nil {
				return fmt.Errorf("screen frame: %w", err)
			}
			if err := s.frame(f); err != nil {
				return err
			}
		case screen.MsgCursor:
			c, err := s.codec.DecodeCursorShape(b)
			if err != nil {
				return fmt.Errorf("cursor shape: %w", err)
			}
			s.addShape(c)
		case screen.MsgCursorPos:
			p, err := s.codec.DecodeCursorPos(b)
			if err != nil {
				return fmt.Errorf("cursor position: %w", err)
			}
			s.mu.Lock()
			s.cursor, s.cursorOK = p, true
			s.mu.Unlock()
		}
	}
}

func (s *Session) setHello(h screen.Hello) error {
	if h.Width <= 0 || h.Height <= 0 || h.Width > MaxSide || h.Height > MaxSide {
		return fmt.Errorf("screen hello: bad size %dx%d", h.Width, h.Height)
	}
	if h.Current < 0 || h.Current >= len(h.Monitors) {
		h.Current = -1 // unknown, e.g. a phase 2 computer
	}
	s.dec.Reset()
	s.skipping = false
	s.awaitFull = false
	if s.back == nil || s.back.Rect.Dx() != h.Width || s.back.Rect.Dy() != h.Height {
		s.back = image.NewRGBA(image.Rect(0, 0, h.Width, h.Height))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hello, s.helloOK = h, true
	if s.img == nil || s.img.Rect != s.back.Rect {
		s.img = image.NewRGBA(s.back.Rect)
		s.resized = true
		s.dirty = append(s.dirty[:0], s.img.Rect)
	}
	return nil
}

// frame applies one frame part, publishes the frame once it is complete and
// acks it. Only the last part of a frame is acked.
func (s *Session) frame(f screen.Frame) error {
	if s.back == nil {
		return errors.New("screen frame before hello")
	}
	if s.skipping && f.Seq == s.skipSeq {
		if !f.More {
			s.skipping = false
			return s.ack(f.Seq)
		}
		return nil
	}
	s.skipping = false
	if f.Full {
		if !s.fullStarted || f.Seq != s.fullSeq {
			// First part of a full frame: the computer reset its encoder and
			// cache, so start from a clean decoder too.
			s.dec.Reset()
		}
		s.awaitFull = false
		s.fullSeq, s.fullStarted = f.Seq, f.More
	} else {
		s.fullStarted = false
		if s.awaitFull {
			if f.More {
				return nil
			}
			return s.ack(f.Seq)
		}
	}
	dirty, complete, err := s.dec.Apply(s.back, f)
	if err != nil {
		// The image may be partly updated and the tile cache out of step
		// with the computer: start over from a full frame.
		s.dec.Reset()
		s.refreshes.Add(1)
		s.mu.Lock()
		s.lastErr = fmt.Errorf("frame %d: %w", f.Seq, err)
		s.mu.Unlock()
		s.request(func(c *requests) { c.refresh = true })
		s.awaitFull = true
		if f.More {
			s.skipping, s.skipSeq = true, f.Seq
			return nil
		}
		return s.ack(f.Seq)
	}
	if !complete {
		return nil
	}
	s.publish(dirty)
	s.frames.Add(1)
	return s.ack(f.Seq)
}

// publish copies the dirty rects of the private image into the shared one.
func (s *Session) publish(dirty []image.Rectangle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.back.Rect
	for _, r := range dirty {
		r = r.Intersect(b)
		if r.Empty() {
			continue
		}
		row := r.Dx() * 4
		off := s.back.PixOffset(r.Min.X, r.Min.Y)
		for y := r.Min.Y; y < r.Max.Y; y++ {
			copy(s.img.Pix[off:off+row], s.back.Pix[off:off+row])
			off += s.back.Stride
		}
		s.dirty = append(s.dirty, r)
	}
	if len(s.dirty) > maxPending {
		s.dirty = []image.Rectangle{union(s.dirty)}
	}
}

// maxPending bounds the dirty rects kept between two uploads.
const maxPending = 4096

func (s *Session) ack(seq uint32) error {
	select {
	case s.acks <- seq:
		return nil
	case <-s.done:
		return nil
	}
}

// request changes the pending control messages and wakes the writer.
func (s *Session) request(f func(*requests)) {
	s.ctlMu.Lock()
	f(&s.ctl)
	s.ctlMu.Unlock()
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// writeScreen sends acks and the techie's requests on the screen channel.
func (s *Session) writeScreen(w io.Writer) {
	fw := wire.NewWriter(w)
	for {
		var err error
		select {
		case <-s.done:
			return
		case seq := <-s.acks:
			err = fw.WriteFrame(screen.MsgAck, s.codec.EncodeAck(screen.Ack{Seq: seq}))
		case <-s.kick:
			s.ctlMu.Lock()
			c := s.ctl
			s.ctl = requests{}
			s.ctlMu.Unlock()
			if c.monitor != nil && err == nil {
				err = fw.WriteFrame(screen.MsgSelectMonitor, s.codec.EncodeSelectMonitor(*c.monitor))
			}
			if c.quality != nil && err == nil {
				err = fw.WriteFrame(screen.MsgQuality, s.codec.EncodeQuality(*c.quality))
			}
			if c.refresh && err == nil {
				err = fw.WriteFrame(screen.MsgRefresh, nil)
			}
		}
		if err != nil {
			s.finish(err)
			return
		}
	}
}

func (s *Session) addShape(c screen.CursorShape) {
	if c.W <= 0 || c.H <= 0 || c.W > screen.MaxCursor || c.H > screen.MaxCursor || len(c.RGBA) != c.W*c.H*4 {
		return // a shape the decoder should not have let through; keep the old one
	}
	c.RGBA = slices.Clone(c.RGBA) // the decoded pixels alias the message buffer
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.shapes[c.ID]; !ok {
		s.shapeOrder = append(s.shapeOrder, c.ID)
	}
	s.shapes[c.ID] = &c
	for len(s.shapeOrder) > maxShapes {
		delete(s.shapes, s.shapeOrder[0])
		s.shapeOrder = s.shapeOrder[1:]
	}
}

// Cursor returns the latest remote cursor position and the shape it names
// (nil if that shape is not known). ok is false until the computer sent a
// position. Shapes are never modified after they arrive. Safe from any
// goroutine.
func (s *Session) Cursor() (pos screen.CursorPos, shape *screen.CursorShape, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cursorOK {
		return screen.CursorPos{}, nil, false
	}
	return s.cursor, s.shapes[s.cursor.ID], true
}

// SelectMonitor asks the computer to share Hello().Monitors[i]. The computer
// answers with a new Hello.
func (s *Session) SelectMonitor(i int) {
	s.request(func(c *requests) { c.monitor = &screen.SelectMonitor{Index: i} })
}

// SetQuality sends the user's quality preference to the computer.
func (s *Session) SetQuality(m screen.QualityMode) {
	s.quality = m
	s.request(func(c *requests) { c.quality = &screen.Quality{Mode: m} })
}

// Quality returns the preference last set with SetQuality (QualityAuto by
// default).
func (s *Session) Quality() screen.QualityMode { return s.quality }

// Refresh asks the computer for a full lossless frame.
func (s *Session) Refresh() {
	s.request(func(c *requests) { c.refresh = true })
}

// Refreshes returns how many times a frame could not be applied and a
// Refresh was requested, and the last such error. Safe from any goroutine.
func (s *Session) Refreshes() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshes.Load(), s.lastErr
}

// Update calls upload with the screen image and the regions changed since
// the last call, planned for upload (see PlanUpload), if anything changed,
// while holding the image lock. resized is true when the image was created
// or changed size since the last call.
func (s *Session) Update(upload func(img *image.RGBA, dirty []image.Rectangle, resized bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.img == nil || (len(s.dirty) == 0 && !s.resized) {
		return
	}
	d := PlanUpload(s.dirty, s.img.Rect)
	resized := s.resized
	s.dirty = s.dirty[:0]
	s.resized = false
	upload(s.img, d, resized)
}

// Hello returns the computer's latest screen hello once one arrived.
func (s *Session) Hello() (screen.Hello, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hello, s.helloOK
}

// NameMismatch reports the user@machine the computer's screen hello reports if
// it differs from the registered name (key copied to another PC or account,
// or the PC was renamed), or "" if it matches or is not known yet.
func (s *Session) NameMismatch() string {
	h, ok := s.Hello()
	if !ok || h.UserMachine == "" || strings.EqualFold(h.UserMachine, s.Info.ComputerName) {
		return ""
	}
	return h.UserMachine
}

// RTT returns the round trip time of the inner connection's keepalive, or 0
// if not known (yet). Safe from any goroutine.
func (s *Session) RTT() time.Duration {
	s.closeMu.Lock()
	f := s.rtt
	s.closeMu.Unlock()
	if f == nil {
		return 0
	}
	return f()
}

// Frames returns the number of complete frames applied so far. Safe from
// any goroutine.
func (s *Session) Frames() int64 { return s.frames.Load() }

// Bytes returns the number of screen channel bytes received so far. Safe
// from any goroutine.
func (s *Session) Bytes() int64 { return s.bytes.Load() }

// Stats returns the frame rate and the received screen data rate in Mbit/s,
// averaged over the last second or so. UI thread only.
func (s *Session) Stats(now time.Time) (fps, mbps float64) {
	if d := now.Sub(s.statsAt); d >= time.Second {
		n, b := s.frames.Load(), s.bytes.Load()
		s.fps = float64(n-s.statsN) / d.Seconds()
		s.mbps = float64(b-s.statsB) * 8 / 1e6 / d.Seconds()
		s.statsN, s.statsB, s.statsAt = n, b, now
	}
	return s.fps, s.mbps
}

// Send queues an input message (input.MouseMove, input.Key, ...). It never
// blocks: if the queue is full, mouse moves are dropped, and any other
// message ends the session because the connection is stuck.
func (s *Session) Send(m any) {
	select {
	case <-s.done:
		return
	default:
	}
	select {
	case s.input <- m:
	default:
		if _, ok := m.(input.MouseMove); ok {
			s.dropped.Add(1)
			return
		}
		s.finish(errors.New("input queue full"))
	}
}

// writeInput sends queued input in order, with mouse moves coalesced.
func (s *Session) writeInput(w io.Writer) {
	fw := wire.NewWriter(w)
	co := Coalescer{Interval: moveInterval}
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	var timerC <-chan time.Time
	for {
		var out []any
		select {
		case <-s.done:
			return
		case m := <-s.input:
			out = co.Push(m, time.Now())
		case <-timerC:
			timerC = nil
			out = co.Tick(time.Now())
		}
		for _, m := range out {
			typ, b, err := s.codec.EncodeInput(m)
			if err != nil {
				s.finish(fmt.Errorf("encode input: %w", err))
				return
			}
			if err := fw.WriteFrame(typ, b); err != nil {
				s.finish(err)
				return
			}
		}
		if due, ok := co.Due(); ok && timerC == nil {
			timer.Reset(max(time.Until(due), 0))
			timerC = timer.C
		}
	}
}
