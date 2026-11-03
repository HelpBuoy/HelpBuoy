package screen

import (
	"sync"
	"time"
)

// MaxUnacked is the most frames the client keeps in flight (PLAN.md 5.5).
const MaxUnacked = 2

// Window is the client's flow control for frames: it tracks frames sent but
// not yet acknowledged and allows at most MaxUnacked of them. While the window
// is full the client keeps accumulating damage and sends the newest pixels
// once a slot frees up, so latency never builds up in SSH or TCP buffers.
//
// Acks are cumulative: the agent applies frames in order, so an Ack for Seq
// n also acknowledges every older frame still in flight. Sequence numbers are
// compared with wraparound.
//
// The zero value is ready to use. A Window is safe for concurrent use, for
// example a sender goroutine calling CanSend and Sent and a reader goroutine
// calling Acked.
//
// Typical use on the client:
//
//	for {
//		if !win.CanSend() {
//			<-win.Freed() // or select on it together with other events
//			continue
//		}
//		f := screen.Frame{Seq: seq, Rects: rects}
//		win.Sent(seq)
//		w.WriteFrame(screen.MsgFrame, screen.EncodeFrame(f))
//		seq++
//	}
type Window struct {
	mu       sync.Mutex
	inflight []sentFrame // oldest first
	freed    chan struct{}
}

type sentFrame struct {
	seq uint32
	at  time.Time
}

// seqBefore reports whether a comes before b, with wraparound.
func seqBefore(a, b uint32) bool { return int32(a-b) < 0 }

func (w *Window) freedLocked() chan struct{} {
	if w.freed == nil {
		w.freed = make(chan struct{}, 1)
	}
	return w.freed
}

// CanSend reports whether another frame may be sent now.
func (w *Window) CanSend() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.inflight) < MaxUnacked
}

// InFlight returns the number of frames sent and not yet acknowledged.
func (w *Window) InFlight() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.inflight)
}

// Sent records that the frame with seq was written. Seqs must increase. If
// the window is already full the frame is still recorded; callers are
// expected to check CanSend first.
func (w *Window) Sent(seq uint32) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.inflight = append(w.inflight, sentFrame{seq: seq, at: time.Now()})
}

// Acked records an Ack for seq and every older frame. It returns the time
// since seq was sent and true, or 0 and false if seq was not in flight (a
// duplicate, stale or unknown ack, which is otherwise ignored).
func (w *Window) Acked(seq uint32) (time.Duration, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var (
		rtt   time.Duration
		found bool
		n     int
	)
	for n < len(w.inflight) && !seqBefore(seq, w.inflight[n].seq) {
		if w.inflight[n].seq == seq {
			rtt, found = time.Since(w.inflight[n].at), true
		}
		n++
	}
	if n == 0 {
		return 0, false
	}
	w.inflight = append(w.inflight[:0], w.inflight[n:]...)
	select {
	case w.freedLocked() <- struct{}{}:
	default:
	}
	return rtt, found
}

// Reset forgets all frames in flight, for example after the stream was
// restarted. It also signals Freed.
func (w *Window) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.inflight = w.inflight[:0]
	select {
	case w.freedLocked() <- struct{}{}:
	default:
	}
}

// Freed returns a channel that receives a value after Acked or Reset frees at
// least one slot. It holds at most one pending signal, so a receiver should
// check CanSend again after waking up.
func (w *Window) Freed() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.freedLocked()
}
