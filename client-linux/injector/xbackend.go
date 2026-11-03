package injector

import (
	"errors"
	"fmt"
	"image"
	"log/slog"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"

	"github.com/mevdschee/helpbuoy/client-linux/xkeys"
)

// xBackend injects with XTEST on its own X connection.
type xBackend struct {
	c    *xgb.Conn
	root xproto.Window
	log  *slog.Logger

	mu      sync.Mutex
	km      xkeys.Map
	stale   bool    // a MappingNotify arrived
	spare   []uint8 // spare keycodes for remapped characters
	remap   map[uint8]xproto.Keysym
	nextIdx int
}

func newXBackend(log *slog.Logger) (*xBackend, error) {
	c, err := xgb.NewConn()
	if err != nil {
		return nil, err
	}
	if err := xtest.Init(c); err != nil {
		c.Close()
		return nil, fmt.Errorf("the X server has no XTEST extension: %w", err)
	}
	km, err := xkeys.Load(c)
	if err != nil {
		c.Close()
		return nil, err
	}
	b := &xBackend{c: c, root: xproto.Setup(c).DefaultScreen(c).Root, log: log, km: km,
		spare: km.Spare(), remap: make(map[uint8]xproto.Keysym)}
	go b.events()
	return b, nil
}

// events drains the connection (xgb stops reading replies when its event
// queue is full) and notes keyboard mapping changes.
func (b *xBackend) events() {
	for {
		ev, err := b.c.WaitForEvent()
		if ev == nil && err == nil {
			return
		}
		if err != nil {
			b.log.Debug("X error on the input connection", "err", err)
			continue
		}
		if _, ok := ev.(xproto.MappingNotifyEvent); ok {
			b.mu.Lock()
			b.stale = true
			b.mu.Unlock()
		}
	}
}

func (b *xBackend) fake(typ, detail uint8, x, y int) error {
	xtest.FakeInput(b.c, typ, detail, 0, b.root, int16(x), int16(y), 0)
	return nil
}

func (b *xBackend) screen() image.Rectangle {
	s := xproto.Setup(b.c).DefaultScreen(b.c)
	return image.Rect(0, 0, int(s.WidthInPixels), int(s.HeightInPixels))
}

func (b *xBackend) flush() error {
	_, err := xproto.GetInputFocus(b.c).Reply()
	return err
}

func (b *xBackend) keymap() xkeys.Map {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stale {
		if km, err := xkeys.Load(b.c); err == nil {
			b.km = km
		}
		b.stale = false
	}
	return b.km
}

func (b *xBackend) typeRune(r rune) error {
	ks := xkeys.Keysym(r)
	km := b.keymap()
	kc, level, ok := km.Find(ks)
	if !ok {
		var err error
		if kc, err = b.remapSpare(ks); err != nil {
			return err
		}
		level = 0
	}
	var shift uint8
	if level == 1 {
		s, _, ok := km.Find(xkeys.ShiftL)
		if !ok {
			s = 42 + 8
		}
		shift = s
		b.fake(xKeyPress, shift, 0, 0)
	}
	b.fake(xKeyPress, kc, 0, 0)
	b.fake(xKeyRelease, kc, 0, 0)
	if shift != 0 {
		b.fake(xKeyRelease, shift, 0, 0)
	}
	return nil
}

// remapSpare maps ks to a spare keycode, reusing them in turn, and waits a
// moment so applications can read the new mapping before the key arrives.
func (b *xBackend) remapSpare(ks xproto.Keysym) (uint8, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for kc, s := range b.remap {
		if s == ks {
			return kc, nil
		}
	}
	if len(b.spare) == 0 {
		return 0, errors.New("no free keycode to type a character the keyboard layout lacks")
	}
	kc := b.spare[b.nextIdx%len(b.spare)]
	b.nextIdx++
	if err := b.setKeysym(kc, ks); err != nil {
		return 0, err
	}
	b.remap[kc] = ks
	time.Sleep(20 * time.Millisecond)
	return kc, nil
}

func (b *xBackend) setKeysym(kc uint8, ks xproto.Keysym) error {
	per := b.km.Per
	syms := make([]xproto.Keysym, per)
	for i := range min(per, 2) {
		syms[i] = ks
	}
	return xproto.ChangeKeyboardMappingChecked(b.c, 1, xproto.Keycode(kc), byte(per), syms).Check()
}

// close restores the remapped keycodes and closes the connection.
func (b *xBackend) close() {
	b.mu.Lock()
	for kc := range b.remap {
		b.setKeysym(kc, 0)
	}
	b.remap = nil
	b.mu.Unlock()
	b.c.Close()
}
