// Package limits implements the server's abuse limits (PLAN 3.4). There are
// no bans and no long windows; every limit is either a rate of one attempt
// per interval or a cap on what is open at the same time:
//
//  1. Per key, after its signature is proven: at most one connection attempt
//     per KeyInterval (1 s). A faster attempt is refused and nothing else
//     happens; the key's existing connection is not touched.
//  2. Per key: one connection. The registry closes the older connection when
//     the same key connects again, so a computer whose network dropped gets
//     back in at once.
//  3. Per source IP, before any key is proven: at most HandshakesPerIP
//     handshakes at the same time; further connections wait for a slot (up
//     to HandshakeWaiters, then they are refused).
//  4. Pending connections (unknown keys): at most PendingPerIP per source IP
//     and PendingTotal for the whole server at the same time, so fresh keys
//     from one place can't take every pending slot.
package limits

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

// Config holds the limits. Zero fields take the defaults from Defaults.
type Config struct {
	KeyInterval time.Duration // minimum time between connection attempts of one key; negative disables the check

	HandshakesPerIP  int // concurrent handshakes per IP
	HandshakeWaiters int // connections per IP that may wait for a handshake slot

	PendingPerIP int // concurrent pending connections per IP
	PendingTotal int // concurrent pending connections in total

	Now func() time.Time
}

// Defaults returns the limits from PLAN 3.4.
func Defaults() Config {
	return Config{
		KeyInterval:      time.Second,
		HandshakesPerIP:  10,
		HandshakeWaiters: 500,
		PendingPerIP:     10,
		PendingTotal:     200,
		Now:              time.Now,
	}
}

func (c *Config) fill() {
	d := Defaults()
	if c.KeyInterval == 0 {
		c.KeyInterval = d.KeyInterval
	}
	if c.HandshakesPerIP == 0 {
		c.HandshakesPerIP = d.HandshakesPerIP
	}
	if c.HandshakeWaiters == 0 {
		c.HandshakeWaiters = d.HandshakeWaiters
	}
	if c.PendingPerIP == 0 {
		c.PendingPerIP = d.PendingPerIP
	}
	if c.PendingTotal == 0 {
		c.PendingTotal = d.PendingTotal
	}
	if c.Now == nil {
		c.Now = d.Now
	}
}

// Errors returned when a limit refuses a connection.
var (
	ErrHandshakeBusy = errors.New("too many concurrent handshakes from this address")
	ErrKeyRate       = errors.New("this key tried to connect less than a second ago")
	ErrIPPending     = errors.New("too many pending connections from this address")
	ErrPendingTotal  = errors.New("too many pending connections")
)

type ipState struct {
	sem     chan struct{} // handshake slots
	waiters int
	pending int
}

// Limiter tracks the counters. It is safe for concurrent use.
type Limiter struct {
	cfg     Config
	mu      sync.Mutex
	ips     map[string]*ipState
	lastTry map[string]time.Time // key fingerprint -> last connection attempt
	pending int
}

// New returns a Limiter for cfg.
func New(cfg Config) *Limiter {
	cfg.fill()
	return &Limiter{cfg: cfg, ips: map[string]*ipState{}, lastTry: map[string]time.Time{}}
}

// IPOf returns the limit key for a remote address: the IP for IPv4 and the
// /64 prefix for IPv6, since one host usually owns a whole /64.
func IPOf(a net.Addr) string {
	var ip net.IP
	switch v := a.(type) {
	case *net.TCPAddr:
		ip = v.IP
	default:
		host, _, err := net.SplitHostPort(a.String())
		if err != nil {
			return a.String()
		}
		ip = net.ParseIP(host)
		if ip == nil {
			return host
		}
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return (&net.IPNet{IP: ip.Mask(net.CIDRMask(64, 128)), Mask: net.CIDRMask(64, 128)}).String()
}

func (l *Limiter) ip(ip string) *ipState {
	s := l.ips[ip]
	if s == nil {
		s = &ipState{sem: make(chan struct{}, l.cfg.HandshakesPerIP)}
		l.ips[ip] = s
	}
	return s
}

// AcquireHandshake reserves one handshake slot for ip. If all slots are busy
// it waits until one frees up or ctx ends. The returned release must be
// called once when the handshake is over; failed is accepted for the
// caller's convenience and doesn't change anything (there is no backoff).
func (l *Limiter) AcquireHandshake(ctx context.Context, ip string) (release func(failed bool), err error) {
	l.mu.Lock()
	s := l.ip(ip)
	if s.waiters >= l.cfg.HandshakeWaiters {
		l.mu.Unlock()
		return nil, ErrHandshakeBusy
	}
	s.waiters++
	l.mu.Unlock()

	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		l.mu.Lock()
		s.waiters--
		l.mu.Unlock()
		return nil, ErrHandshakeBusy
	}
	l.mu.Lock()
	s.waiters--
	l.mu.Unlock()

	var once sync.Once
	return func(bool) {
		once.Do(func() {
			l.mu.Lock()
			<-s.sem
			l.mu.Unlock()
		})
	}, nil
}

// AdmitKey records a connection attempt of a proven key and refuses it when
// the same key tried less than KeyInterval ago. Every role is subject to it.
func (l *Limiter) AdmitKey(fp string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.cfg.Now()
	if last, ok := l.lastTry[fp]; ok && now.Sub(last) < l.cfg.KeyInterval {
		return ErrKeyRate
	}
	l.lastTry[fp] = now
	return nil
}

// AdmitPending decides whether a proven but unknown key may hold a pending
// connection from ip. On success the returned release must be called when the
// connection ends or is promoted to a registered computer.
func (l *Limiter) AdmitPending(fp, ip string) (release func(), err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.ip(ip)
	if s.pending >= l.cfg.PendingPerIP {
		return nil, ErrIPPending
	}
	if l.pending >= l.cfg.PendingTotal {
		return nil, ErrPendingTotal
	}
	s.pending++
	l.pending++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			s.pending--
			l.pending--
			l.mu.Unlock()
		})
	}, nil
}

// Pending returns the number of admitted pending connections.
func (l *Limiter) Pending() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pending
}

// Sweep drops counters that no longer matter, to bound memory.
func (l *Limiter) Sweep() {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := l.cfg.Now().Add(-l.cfg.KeyInterval)
	for fp, t := range l.lastTry {
		if t.Before(cutoff) {
			delete(l.lastTry, fp)
		}
	}
	for ip, s := range l.ips {
		if len(s.sem) == 0 && s.waiters == 0 && s.pending == 0 {
			delete(l.ips, ip)
		}
	}
}
