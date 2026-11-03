package xkeys

import (
	"testing"

	"github.com/jezek/xgb/xproto"
)

func TestLookup(t *testing.T) {
	// keycode 10: a A, 11: 1 exclam, 12: KP_Home KP_7, 13: e E EuroSign (AltGr)
	m := Map{Min: 10, Max: 13, Per: 6, Syms: []xproto.Keysym{
		'a', 0, 0, 0, 0, 0,
		'1', '!', 0, 0, 0, 0,
		KPHome, 0xffb7, 0, 0, 0, 0,
		'e', 'E', 0, 0, 0x20ac, 0,
	}}
	for _, c := range []struct {
		kc    uint8
		state uint16
		want  xproto.Keysym
	}{
		{10, 0, 'a'},
		{10, ModShift, 'A'},
		{10, ModLock, 'A'},
		{10, ModLock | ModShift, 'a'},
		{11, ModLock, '1'},
		{11, ModShift, '!'},
		{12, 0, KPHome},
		{12, ModMod2, 0xffb7},
		{13, ModMod5, 0x20ac},
	} {
		if got := m.Lookup(c.kc, c.state); got != c.want {
			t.Errorf("Lookup(%d, %#x) = %#x, want %#x", c.kc, c.state, got, c.want)
		}
	}
	if r := Rune(0xffb7); r != '7' {
		t.Errorf("Rune(KP_7) = %q", r)
	}
	if ks := Keysym('✓'); Rune(ks) != '✓' {
		t.Errorf("round trip of ✓ gave %q", Rune(ks))
	}
}
