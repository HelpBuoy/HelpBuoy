// Package limits implements the relay's abuse limits (PLAN 3.4). They are
// keyed on the proven key first and on the source IP only after that:
//
//  1. Known keys (helpers, registered clients) are never rate limited here.
//  2. Per unknown key: one connection (enforced by the caller, which
//     replaces an older one), at most AttemptsPerKey connection attempts per
//     Window; exceeding that blocks the key for KeyBlock.
//  3. Per source IP, for unknown keys only: at most UnknownKeysPerIP
//     distinct unknown keys per Window and PendingPerIP pending connections;
//     beyond that new unknown keys from the IP are refused for IPBlock.
//  4. Per source IP before any key is proven: at most HandshakesPerIP
//     concurrent handshakes (further ones wait for a slot) and FailuresPerIP
//     failed handshakes per Window, after which the IP backs off.
//  5. Relay-wide: at most PendingTotal pending connections.
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
	Window time.Duration // sliding window for the per-hour counters

	AttemptsPerKey int
	KeyBlock       time.Duration

	UnknownKeysPerIP int
	PendingPerIP     int
	IPBlock          time.Duration

	HandshakesPerIP  int
	HandshakeWaiters int // connections per IP that may wait for a handshake slot
	FailuresPerIP    int
	BackoffBase      time.Duration
	BackoffMax       time.Duration

	PendingTotal int

	Now func() time.Time
}

// Defaults returns the limits from PLAN 3.4.
func Defaults() Config {
	return Config{
		Window:           time.Hour,
		AttemptsPerKey:   10,
		KeyBlock:         time.Hour,
		UnknownKeysPerIP: 5,
		PendingPerIP:     10,
		IPBlock:          time.Hour,
		HandshakesPerIP:  10,
		HandshakeWaiters: 500,
		FailuresPerIP:    30,
		BackoffBase:      time.Minute,
		BackoffMax:       time.Hour,
		PendingTotal:     200,
		Now:              time.Now,
	}
}

func (c *Config) fill() {
	d := Defaults()
	set := func(v *int, def int) {
		if *v == 0 {
			*v = def
		}
	}
	setD := func(v *time.Duration, def time.Duration) {
		if *v == 0 {
			*v = def
		}
	}
	setD(&c.Window, d.Window)
	set(&c.AttemptsPerKey, d.AttemptsPerKey)
	setD(&c.KeyBlock, d.KeyBlock)
	set(&c.UnknownKeysPerIP, d.UnknownKeysPerIP)
	set(&c.PendingPerIP, d.PendingPerIP)
	setD(&c.IPBlock, d.IPBlock)
	set(&c.HandshakesPerIP, d.HandshakesPerIP)
	set(&c.HandshakeWaiters, d.HandshakeWaiters)
	set(&c.FailuresPerIP, d.FailuresPerIP)
	setD(&c.BackoffBase, d.BackoffBase)
	setD(&c.BackoffMax, d.BackoffMax)
	set(&c.PendingTotal, d.PendingTotal)
	if c.Now == nil {
		c.Now = d.Now
	}
}

// Errors returned when a limit refuses a connection.
var (
	ErrBackoff       = errors.New("too many failed handshakes from this address")
	ErrHandshakeBusy = errors.New("too many concurrent handshakes from this address")
	ErrKeyBlocked    = errors.New("key is blocked")
	ErrKeyAttempts   = errors.New("too many connection attempts for this key")
	ErrIPBlocked     = errors.New("address is blocked for unknown keys")
	ErrIPKeys        = errors.New("too many unknown keys from this address")
	ErrIPPending     = errors.New("too many pending connections from this address")
	ErrPendingTotal  = errors.New("too many pending connections")
)

type ipState struct {
	sem          chan struct{} // handshake slots
	waiters      int
	fails        []time.Time
	backoffUntil time.Time
	backoffLevel int

	unknown      map[string]time.Time // unknown key fingerprint -> first seen
	blockedUntil time.Time
	pending      int
}

type keyState struct {
	attempts     []time.Time
	blockedUntil time.Time
}

// Limiter tracks the counters. It is safe for concurrent use.
type Limiter struct {
	cfg     Config
	mu      sync.Mutex
	ips     map[string]*ipState
	keys    map[string]*keyState
	pending int
}

// New returns a Limiter for cfg.
func New(cfg Config) *Limiter {
	cfg.fill()
	return &Limiter{cfg: cfg, ips: map[string]*ipState{}, keys: map[string]*keyState{}}
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
		s = &ipState{sem: make(chan struct{}, l.cfg.HandshakesPerIP), unknown: map[string]time.Time{}}
		l.ips[ip] = s
	}
	return s
}

func prune(ts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(ts) && !ts[i].After(cutoff) {
		i++
	}
	return ts[i:]
}

// AcquireHandshake reserves one handshake slot for ip. If all slots are busy
// it waits until one frees up or ctx ends. The returned release must be
// called once with whether the handshake failed.
func (l *Limiter) AcquireHandshake(ctx context.Context, ip string) (release func(failed bool), err error) {
	l.mu.Lock()
	s := l.ip(ip)
	now := l.cfg.Now()
	if now.Before(s.backoffUntil) {
		l.mu.Unlock()
		return nil, ErrBackoff
	}
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
	return func(failed bool) {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			<-s.sem
			if failed {
				l.failLocked(s)
			}
		})
	}, nil
}

func (l *Limiter) failLocked(s *ipState) {
	now := l.cfg.Now()
	s.fails = append(prune(s.fails, now.Add(-l.cfg.Window)), now)
	if len(s.fails) < l.cfg.FailuresPerIP {
		return
	}
	s.backoffLevel++
	d := l.cfg.BackoffBase
	for i := 1; i < s.backoffLevel && d < l.cfg.BackoffMax; i++ {
		d *= 2
	}
	d = min(d, l.cfg.BackoffMax)
	s.backoffUntil = now.Add(d)
	// Keep the list bounded; only the count within the window matters.
	if len(s.fails) > 2*l.cfg.FailuresPerIP {
		s.fails = s.fails[len(s.fails)-l.cfg.FailuresPerIP:]
	}
}

// AdmitPending decides whether a proven but unknown key may hold a pending
// connection from ip. On success the returned release must be called when the
// connection ends or is promoted to a registered client.
func (l *Limiter) AdmitPending(fp, ip string) (release func(), err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.cfg.Now()

	// 2. Per key.
	k := l.keys[fp]
	if k == nil {
		k = &keyState{}
		l.keys[fp] = k
	}
	if now.Before(k.blockedUntil) {
		return nil, ErrKeyBlocked
	}
	k.attempts = append(prune(k.attempts, now.Add(-l.cfg.Window)), now)
	if len(k.attempts) > l.cfg.AttemptsPerKey {
		k.blockedUntil = now.Add(l.cfg.KeyBlock)
		k.attempts = nil
		return nil, ErrKeyAttempts
	}

	// 3. Per IP, unknown keys only.
	s := l.ip(ip)
	for f, t := range s.unknown {
		if !t.After(now.Add(-l.cfg.Window)) {
			delete(s.unknown, f)
		}
	}
	_, seen := s.unknown[fp]
	if !seen {
		if now.Before(s.blockedUntil) {
			return nil, ErrIPBlocked
		}
		if len(s.unknown) >= l.cfg.UnknownKeysPerIP {
			s.blockedUntil = now.Add(l.cfg.IPBlock)
			return nil, ErrIPKeys
		}
	}
	if s.pending >= l.cfg.PendingPerIP {
		s.blockedUntil = now.Add(l.cfg.IPBlock)
		return nil, ErrIPPending
	}

	// 5. Relay-wide.
	if l.pending >= l.cfg.PendingTotal {
		return nil, ErrPendingTotal
	}

	if !seen {
		s.unknown[fp] = now
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
	now := l.cfg.Now()
	cutoff := now.Add(-l.cfg.Window)
	for fp, k := range l.keys {
		k.attempts = prune(k.attempts, cutoff)
		if len(k.attempts) == 0 && !now.Before(k.blockedUntil) {
			delete(l.keys, fp)
		}
	}
	for ip, s := range l.ips {
		s.fails = prune(s.fails, cutoff)
		if len(s.fails) < l.cfg.FailuresPerIP {
			s.backoffLevel = 0
		}
		for f, t := range s.unknown {
			if !t.After(cutoff) {
				delete(s.unknown, f)
			}
		}
		if len(s.sem) == 0 && s.waiters == 0 && s.pending == 0 && len(s.fails) == 0 &&
			len(s.unknown) == 0 && !now.Before(s.blockedUntil) && !now.Before(s.backoffUntil) {
			delete(l.ips, ip)
		}
	}
}
