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

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newLimiter() (*Limiter, *clock) {
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	cfg := Defaults()
	cfg.Now = c.Now
	return New(cfg), c
}

func TestPerKeyAttempts(t *testing.T) {
	l, c := newLimiter()
	for i := range 10 {
		rel, err := l.AdmitPending("k", "1.1.1.1")
		if err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		rel()
		rel() // idempotent
	}
	if _, err := l.AdmitPending("k", "1.1.1.1"); !errors.Is(err, ErrKeyAttempts) {
		t.Fatalf("11th: %v", err)
	}
	if _, err := l.AdmitPending("k", "2.2.2.2"); !errors.Is(err, ErrKeyBlocked) {
		t.Fatalf("blocked key from another IP: %v", err)
	}
	c.Add(time.Hour)
	if _, err := l.AdmitPending("k", "1.1.1.1"); err != nil {
		t.Fatalf("after block: %v", err)
	}
}

func TestPerIPUnknownKeys(t *testing.T) {
	l, c := newLimiter()
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		if _, err := l.AdmitPending(k, "1.1.1.1"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.AdmitPending("f", "1.1.1.1"); !errors.Is(err, ErrIPKeys) {
		t.Fatalf("6th key: %v", err)
	}
	if _, err := l.AdmitPending("g", "1.1.1.1"); !errors.Is(err, ErrIPBlocked) {
		t.Fatalf("while blocked: %v", err)
	}
	if _, err := l.AdmitPending("a", "1.1.1.1"); err != nil {
		t.Fatalf("seen key while blocked: %v", err)
	}
	if _, err := l.AdmitPending("f", "3.3.3.3"); err != nil {
		t.Fatalf("other IP: %v", err)
	}
	c.Add(time.Hour + time.Second)
	if _, err := l.AdmitPending("h", "1.1.1.1"); err != nil {
		t.Fatalf("after an hour: %v", err)
	}
}

func TestPerIPPending(t *testing.T) {
	l, c := newLimiter()
	var rels []func()
	// Spread keys over time so the distinct-keys rule doesn't trigger first.
	for i := range 10 {
		rel, err := l.AdmitPending(string(rune('a'+i)), "1.1.1.1")
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		rels = append(rels, rel)
		c.Add(13 * time.Minute)
	}
	if _, err := l.AdmitPending("z", "1.1.1.1"); !errors.Is(err, ErrIPPending) {
		t.Fatalf("11th pending: %v", err)
	}
	if l.Pending() != 10 {
		t.Errorf("pending %d", l.Pending())
	}
	for _, r := range rels {
		r()
	}
	if l.Pending() != 0 {
		t.Errorf("pending after release %d", l.Pending())
	}
}

func TestPendingTotal(t *testing.T) {
	c := &clock{t: time.Unix(0, 0)}
	l := New(Config{PendingTotal: 2, Now: c.Now})
	l.AdmitPending("a", "1.1.1.1")
	l.AdmitPending("b", "2.2.2.2")
	if _, err := l.AdmitPending("c", "3.3.3.3"); !errors.Is(err, ErrPendingTotal) {
		t.Fatal(err)
	}
}

func TestHandshakeSlotsWait(t *testing.T) {
	l, _ := newLimiter()
	var rels []func(bool)
	for range 10 {
		rel, err := l.AcquireHandshake(context.Background(), "1.1.1.1")
		if err != nil {
			t.Fatal(err)
		}
		rels = append(rels, rel)
	}
	// The 11th waits; with a short context it gives up.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if _, err := l.AcquireHandshake(ctx, "1.1.1.1"); !errors.Is(err, ErrHandshakeBusy) {
		t.Fatalf("11th: %v", err)
	}
	cancel()
	// Another IP is not affected.
	if rel, err := l.AcquireHandshake(context.Background(), "2.2.2.2"); err != nil {
		t.Fatal(err)
	} else {
		rel(false)
	}
	// It gets a slot as soon as one frees up.
	got := make(chan error, 1)
	go func() {
		rel, err := l.AcquireHandshake(context.Background(), "1.1.1.1")
		if err == nil {
			rel(false)
		}
		got <- err
	}()
	time.Sleep(10 * time.Millisecond)
	rels[0](false)
	if err := <-got; err != nil {
		t.Fatal(err)
	}
}

func TestHandshakeFailuresBackoff(t *testing.T) {
	l, c := newLimiter()
	for range 30 {
		rel, err := l.AcquireHandshake(context.Background(), "1.1.1.1")
		if err != nil {
			t.Fatal(err)
		}
		rel(true)
	}
	if _, err := l.AcquireHandshake(context.Background(), "1.1.1.1"); !errors.Is(err, ErrBackoff) {
		t.Fatalf("after 30 failures: %v", err)
	}
	c.Add(time.Minute)
	rel, err := l.AcquireHandshake(context.Background(), "1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	rel(true) // fails again: the backoff doubles
	c.Add(time.Minute)
	if _, err := l.AcquireHandshake(context.Background(), "1.1.1.1"); !errors.Is(err, ErrBackoff) {
		t.Fatalf("second backoff too short: %v", err)
	}
	c.Add(time.Minute)
	if rel, err := l.AcquireHandshake(context.Background(), "1.1.1.1"); err != nil {
		t.Fatal(err)
	} else {
		rel(false)
	}
}

func TestSweepForgets(t *testing.T) {
	l, c := newLimiter()
	rel, _ := l.AdmitPending("a", "1.1.1.1")
	rel()
	c.Add(2 * time.Hour)
	l.Sweep()
	if len(l.ips) != 0 || len(l.keys) != 0 {
		t.Errorf("ips %d keys %d", len(l.ips), len(l.keys))
	}
}

func TestIPOf(t *testing.T) {
	for in, want := range map[string]string{
		"1.2.3.4:5":               "1.2.3.4",
		"[2001:db8:1:2:3::4]:22":  "2001:db8:1:2::/64",
		"[::ffff:1.2.3.4]:22":     "1.2.3.4",
		"[2001:db8:1:2:ffff::]:1": "2001:db8:1:2::/64",
	} {
		a, _ := net.ResolveTCPAddr("tcp", in)
		if got := IPOf(a); got != want {
			t.Errorf("IPOf(%s) = %s, want %s", in, got, want)
		}
	}
}
