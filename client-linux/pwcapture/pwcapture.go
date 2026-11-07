// Package pwcapture captures a Wayland desktop: the PipeWire stream of a
// portal session (see package portal), read by a gst-launch-1.0 process
// that writes raw BGRx frames of a fixed size to a pipe. Speaking PipeWire
// directly would need libpipewire; GStreamer's pipewiresrc already comes
// with most Wayland desktops (gstreamer1.0-pipewire) and keeps this binary
// free of C libraries.
//
// The cursor is drawn into the frames by the compositor, so Cursor reports
// no cursor of its own.
package pwcapture

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/helpbuoy/helpbuoy/client-linux/portal"
	wcapture "github.com/helpbuoy/helpbuoy/client-windows/capture"
)

// gstLaunch is the GStreamer command line tool.
const gstLaunch = "gst-launch-1.0"

// firstFrame is how long Capture waits for the first frame of a stream.
const firstFrame = 10 * time.Second

// ErrNoGStreamer is returned by Check when gst-launch-1.0 or its PipeWire
// plugin is missing.
var ErrNoGStreamer = errors.New("pwcapture: gst-launch-1.0 with the pipewiresrc plugin is not installed")

// Check reports whether the GStreamer tools and the pipewiresrc plugin are
// installed.
func Check() error {
	if _, err := exec.LookPath(gstLaunch); err != nil {
		return ErrNoGStreamer
	}
	inspect, err := exec.LookPath("gst-inspect-1.0")
	if err != nil {
		return ErrNoGStreamer
	}
	if err := exec.Command(inspect, "--exists", "pipewiresrc").Run(); err != nil {
		return ErrNoGStreamer
	}
	return nil
}

// Screen implements capture.Screen for the one stream of a portal session.
// It owns the session: Close ends it.
type Screen struct {
	sess   *portal.Session
	stream portal.Stream
	log    *slog.Logger

	mu   sync.Mutex
	open []*Capturer
}

// New returns the screen of sess's first stream.
func New(sess *portal.Session, log *slog.Logger) (*Screen, error) {
	if len(sess.Streams) == 0 {
		return nil, errors.New("pwcapture: the session has no stream")
	}
	st := sess.Streams[0]
	if st.Rect.Empty() {
		return nil, errors.New("pwcapture: the stream has no size")
	}
	if log == nil {
		log = slog.Default()
	}
	return &Screen{sess: sess, stream: st, log: log}, nil
}

// Session returns the portal session, for the injector.
func (s *Screen) Session() *portal.Session { return s.sess }

// Stream returns the shared stream.
func (s *Screen) Stream() portal.Stream { return s.stream }

// Monitors returns the shared monitor at the origin, in the stream's
// logical size: input coordinates are relative to the stream.
func (s *Screen) Monitors() ([]wcapture.Monitor, error) {
	select {
	case <-s.sess.Done():
		return nil, errors.New("pwcapture: sharing was stopped")
	default:
	}
	return []wcapture.Monitor{{
		Rect:    image.Rectangle{Max: s.stream.Rect.Size()},
		Primary: true,
		Name:    "portal-" + strconv.FormatUint(uint64(s.stream.Node), 10),
	}}, nil
}

// Open starts reading the stream.
func (s *Screen) Open(m wcapture.Monitor) (wcapture.Capturer, error) {
	fd, err := s.sess.OpenPipeWireRemote()
	if err != nil {
		return nil, err
	}
	defer fd.Close() // the child has its own copy
	c, err := Start(fd, s.stream.Node, m.Rect.Dx(), m.Rect.Dy(), s.log)
	if err != nil {
		return nil, err
	}
	c.done = s.sess.Done()
	s.mu.Lock()
	s.open = append(s.open, c)
	s.mu.Unlock()
	return c, nil
}

// Cursor reports no cursor: it is part of the frames.
func (s *Screen) Cursor() (wcapture.CursorInfo, error) { return wcapture.CursorInfo{}, nil }

// CursorShape is never needed, since Cursor has no handle.
func (s *Screen) CursorShape(uintptr) (wcapture.Shape, error) {
	return wcapture.Shape{}, errors.New("pwcapture: no cursor shapes")
}

// Close stops every capture and ends the portal session.
func (s *Screen) Close() error {
	s.mu.Lock()
	open := s.open
	s.open = nil
	s.mu.Unlock()
	for _, c := range open {
		c.Close()
	}
	return s.sess.Close()
}

// Args returns the gst-launch-1.0 arguments that read node through the
// PipeWire connection on fd and write w by h BGRx frames to stdout.
func Args(fd int, node uint32, w, h int) []string {
	return []string{
		"-q",
		"pipewiresrc", "fd=" + strconv.Itoa(fd), "path=" + strconv.FormatUint(uint64(node), 10),
		"always-copy=true", "do-timestamp=false",
		"!", "videoconvert",
		"!", "videoscale",
		"!", fmt.Sprintf("video/x-raw,format=BGRx,width=%d,height=%d", w, h),
		"!", "fdsink", "fd=1", "sync=false",
	}
}

// Capturer implements capture.Capturer. A goroutine reads every frame the
// process writes; Capture copies the newest one.
type Capturer struct {
	w, h int
	cmd  *exec.Cmd
	done <-chan struct{} // the session ended; nil when there is no session
	log  *slog.Logger

	mu     sync.Mutex
	cond   *sync.Cond
	latest []byte // newest complete frame, or nil
	err    error  // why reading stopped
	closed bool

	out  [2]*image.RGBA
	next int

	stderr bytes.Buffer
	exited chan struct{}
}

// Start runs the pipeline on the PipeWire connection fd (which it does not
// close) for node, with frames of w by h pixels.
func Start(fd *os.File, node uint32, w, h int, log *slog.Logger) (*Capturer, error) {
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("pwcapture: bad size %dx%d", w, h)
	}
	if log == nil {
		log = slog.Default()
	}
	c := &Capturer{w: w, h: h, log: log, exited: make(chan struct{})}
	c.cond = sync.NewCond(&c.mu)
	c.cmd = exec.Command(gstLaunch, Args(3, node, w, h)...)
	c.cmd.ExtraFiles = []*os.File{fd}
	c.cmd.Stderr = &lockedWriter{w: &c.stderr, mu: &c.mu}
	c.cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	out, err := c.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := c.cmd.Start(); err != nil {
		return nil, fmt.Errorf("pwcapture: %w", err)
	}
	log.Info("PipeWire capture started", "node", node, "size", fmt.Sprintf("%dx%d", w, h), "pid", c.cmd.Process.Pid)
	go c.read(out)
	return c, nil
}

// read reads frames until the process ends.
func (c *Capturer) read(r io.Reader) {
	size := c.w * c.h * 4
	bufs := [2][]byte{make([]byte, size), make([]byte, size)}
	i := 0
	var err error
	for {
		if _, err = io.ReadFull(r, bufs[i]); err != nil {
			break
		}
		c.mu.Lock()
		c.latest = bufs[i]
		c.cond.Broadcast()
		c.mu.Unlock()
		// Capture copies latest under mu, so once latest moved on no copy
		// reads the other buffer any more.
		i ^= 1
	}
	werr := c.cmd.Wait()
	c.mu.Lock()
	msg := bytes.TrimSpace(c.stderr.Bytes())
	switch {
	case c.closed:
		c.err = errors.New("pwcapture: closed")
	case len(msg) > 0:
		c.err = fmt.Errorf("pwcapture: %s", gstError(msg))
	case werr != nil:
		c.err = fmt.Errorf("pwcapture: %s: %w", gstLaunch, werr)
	default:
		c.err = fmt.Errorf("pwcapture: the stream ended (%v)", err)
	}
	if !c.closed {
		c.log.Warn("PipeWire capture stopped", "err", c.err)
	}
	c.cond.Broadcast()
	c.mu.Unlock()
	close(c.exited)
}

// Capture returns the newest frame. It waits for the first one. Frames are
// BGRX, as from the X11 capture.
func (c *Capturer) Capture() (*image.RGBA, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.latest == nil && c.err == nil && !c.closed {
		timer := time.AfterFunc(firstFrame, func() {
			c.mu.Lock()
			if c.latest == nil && c.err == nil {
				c.err = errors.New("pwcapture: no frame from the stream")
			}
			c.cond.Broadcast()
			c.mu.Unlock()
		})
		for c.latest == nil && c.err == nil && !c.closed {
			c.cond.Wait()
		}
		timer.Stop()
	}
	if c.closed {
		return nil, errors.New("pwcapture: closed")
	}
	if c.done != nil {
		select {
		case <-c.done:
			return nil, errors.New("pwcapture: sharing was stopped")
		default:
		}
	}
	if c.err != nil {
		return nil, c.err
	}
	img := c.out[c.next]
	if img == nil {
		img = image.NewRGBA(image.Rect(0, 0, c.w, c.h))
		c.out[c.next] = img
	}
	c.next ^= 1
	copy(img.Pix, c.latest)
	return img, nil
}

// Close stops the process. It is safe to call more than once.
func (c *Capturer) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.cond.Broadcast()
	c.mu.Unlock()
	c.cmd.Process.Kill()
	select {
	case <-c.exited:
	case <-time.After(2 * time.Second):
	}
	return nil
}

// gstError returns the first ERROR line of gst-launch's output, which
// names what failed, or else its last line.
func gstError(b []byte) string {
	lines := bytes.Split(b, []byte("\n"))
	for _, l := range lines {
		if s, ok := bytes.CutPrefix(l, []byte("ERROR: ")); ok {
			return string(s)
		}
	}
	return string(lines[len(lines)-1])
}

// lockedWriter writes to w under mu, so stderr can be read while the
// process runs.
type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.w.Len() > 64<<10 {
		return len(p), nil // enough to explain a failure
	}
	return l.w.Write(p)
}
