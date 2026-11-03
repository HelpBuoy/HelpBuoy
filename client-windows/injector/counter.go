// Package injector receives the agent's input. In phase 2 it only counts and
// logs the messages; injecting them with SendInput is phase 4. The Esc hook
// that lets the user end a session locally is already real (PLAN.md 6.4).
package injector

import (
	"log/slog"
	"sync"

	"github.com/mevdschee/helpbuoy/proto/input"
)

// Counter is the phase 2 input sink: it decodes and counts every message and
// logs it at debug level, but injects nothing.
type Counter struct {
	Logger *slog.Logger

	mu     sync.Mutex
	counts map[uint8]int
	bad    int
}

// Input implements session.InputSink.
func (c *Counter) Input(typ uint8, payload []byte) {
	m, err := input.Decode(typ, payload)
	c.mu.Lock()
	if c.counts == nil {
		c.counts = make(map[uint8]int)
	}
	c.counts[typ]++
	if err != nil {
		c.bad++
	}
	c.mu.Unlock()
	log := c.Logger
	if log == nil {
		log = slog.Default()
	}
	if err != nil {
		log.Debug("input message not decoded", "type", typ, "bytes", len(payload), "err", err)
		return
	}
	log.Debug("input message (not injected in phase 2)", "type", typ, "msg", m)
}

// Counts returns how many messages of each type arrived, and how many of
// them could not be decoded.
func (c *Counter) Counts() (byType map[uint8]int, undecoded int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	byType = make(map[uint8]int, len(c.counts))
	for k, v := range c.counts {
		byType[k] = v
	}
	return byType, c.bad
}
