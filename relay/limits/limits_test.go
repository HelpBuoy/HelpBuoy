package limits

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)} }

func TestKeyRate(t *testing.T) {
	c := newClock()
	l := New(Config{Now: c.Now})
	if err := l.AdmitKey("A"); err != nil {
		t.Fatal(err)
	}
	// A second attempt within a second is refused, another key is not.
	c.Add(999 * time.Millisecond)
	if err := l.AdmitKey("A"); !errors.Is(err, ErrKeyRate) {
		t.Fatalf("second attempt: %v, want ErrKeyRate", err)
	}
	if err := l.AdmitKey("B"); err != nil {
		t.Fatalf("other key: %v", err)
	}
	// A refused attempt doesn't extend anything: one second after the last
	// admitted attempt the key gets in again. There is no block.
	c.Add(time.Millisecond)
	if err := l.AdmitKey("A"); err != nil {
		t.Fatalf("after a second: %v", err)
	}
	// Hammering for a minute never blocks the key for longer than a second.
	for range 600 {
		c.Add(100 * time.Millisecond)
		l.AdmitKey("A")
	}
	c.Add(time.Second)
	if err := l.AdmitKey("A"); err != nil {
		t.Fatalf("after hammering: %v", err)
	}
}

func TestPendingCaps(t *testing.T) {
	l := New(Config{PendingPerIP: 2, PendingTotal: 3})
	r1, err := l.AdmitPending("k1", "1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.AdmitPending("k2", "1.1.1.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.AdmitPending("k3", "1.1.1.1"); !errors.Is(err, ErrIPPending) {
		t.Fatalf("third from one IP: %v", err)
	}
	if _, err := l.AdmitPending("k4", "2.2.2.2"); err != nil {
		t.Fatalf("other IP: %v", err)
	}
	if _, err := l.AdmitPending("k5", "3.3.3.3"); !errors.Is(err, ErrPendingTotal) {
		t.Fatalf("over the total: %v", err)
	}
	// Closing a pending connection frees its slot right away: no block.
	r1()
	r1() // harmless twice
	if _, err := l.AdmitPending("k3", "1.1.1.1"); err != nil {
		t.Fatalf("after release: %v", err)
	}
	if l.Pending() != 3 {
		t.Fatalf("pending %d", l.Pending())
	}
}

func TestHandshakeSlots(t *testing.T) {
	l := New(Config{HandshakesPerIP: 2, HandshakeWaiters: 3})
	ctx := context.Background()
	rel1, err := l.AcquireHandshake(ctx, "ip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.AcquireHandshake(ctx, "ip"); err != nil {
		t.Fatal(err)
	}
	// The third waits until a slot frees, and gives up with its context.
	short, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if _, err := l.AcquireHandshake(short, "ip"); !errors.Is(err, ErrHandshakeBusy) {
		t.Fatalf("no slot: %v", err)
	}
	got := make(chan error, 1)
	go func() {
		_, err := l.AcquireHandshake(ctx, "ip")
		got <- err
	}()
	time.Sleep(20 * time.Millisecond)
	rel1(true) // a failed handshake changes nothing but the slot
	if err := <-got; err != nil {
		t.Fatalf("waiter: %v", err)
	}
	// Another IP has its own slots.
	if _, err := l.AcquireHandshake(ctx, "other"); err != nil {
		t.Fatalf("other ip: %v", err)
	}
}

func TestSweep(t *testing.T) {
	c := newClock()
	l := New(Config{Now: c.Now})
	l.AdmitKey("A")
	rel, _ := l.AcquireHandshake(context.Background(), "ip")
	rel(false)
	c.Add(2 * time.Second)
	l.Sweep()
	if len(l.lastTry) != 0 || len(l.ips) != 0 {
		t.Fatalf("not swept: %d keys, %d ips", len(l.lastTry), len(l.ips))
	}
}

func TestIPOf(t *testing.T) {
	for _, tt := range []struct {
		addr net.Addr
		want string
	}{
		{&net.TCPAddr{IP: net.ParseIP("192.0.2.7"), Port: 1}, "192.0.2.7"},
		{&net.TCPAddr{IP: net.ParseIP("2001:db8:1:2:3:4:5:6"), Port: 1}, "2001:db8:1:2::/64"},
	} {
		if got := IPOf(tt.addr); got != tt.want {
			t.Errorf("IPOf(%v) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}
