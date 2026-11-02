package session

import (
	"time"

	"github.com/mevdschee/help-me-win/proto/input"
)

// Coalescer limits mouse moves to one per Interval while keeping every other
// input message, and the order of all of them: a held move is sent before
// any later button, wheel, key or text message. It is a pure function of the
// times it is given, so it can be tested with a fake clock.
type Coalescer struct {
	Interval time.Duration

	last    time.Time // when the last move went out
	sent    bool      // a move went out at last
	pending input.MouseMove
	held    bool
}

// Push takes one message at time now and returns the messages to send now,
// in order.
func (c *Coalescer) Push(m any, now time.Time) []any {
	mv, ok := m.(input.MouseMove)
	if !ok {
		if c.held {
			c.held = false
			c.last, c.sent = now, true
			return []any{c.pending, m}
		}
		return []any{m}
	}
	if !c.held && (!c.sent || now.Sub(c.last) >= c.Interval) {
		c.last, c.sent = now, true
		return []any{mv}
	}
	c.pending, c.held = mv, true
	return nil
}

// Due returns when the held move must go out, if there is one.
func (c *Coalescer) Due() (time.Time, bool) {
	if !c.held {
		return time.Time{}, false
	}
	return c.last.Add(c.Interval), true
}

// Tick returns the held move if it is due at now.
func (c *Coalescer) Tick(now time.Time) []any {
	if due, ok := c.Due(); !ok || now.Before(due) {
		return nil
	}
	c.held = false
	c.last, c.sent = now, true
	return []any{c.pending}
}
