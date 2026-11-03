// Package xkeys reads the X keyboard mapping and converts between keysyms
// and characters.
package xkeys

import (
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// Keysyms used by the client.
const (
	ShiftL     = 0xffe1
	Escape     = 0xff1b
	BackSpace  = 0xff08
	Tab        = 0xff09
	ISOLeftTab = 0xfe20
	Return     = 0xff0d
	KPEnter    = 0xff8d
	Delete     = 0xffff
	KPDelete   = 0xff9f
	Home       = 0xff50
	Left       = 0xff51
	Up         = 0xff52
	Right      = 0xff53
	Down       = 0xff54
	End        = 0xff57
	KPHome     = 0xff95
	KPLeft     = 0xff96
	KPRight    = 0xff98
	KPEnd      = 0xff9c
	Insert     = 0xff63
)

// Modifier bits of the core event state.
const (
	ModShift   = 1
	ModLock    = 2
	ModControl = 4
	ModMod1    = 8
	ModMod2    = 16 // usually Num Lock
	ModMod5    = 128
)

// Map is the keyboard mapping of a display: keysyms per keycode.
type Map struct {
	Min, Max uint8
	Per      int
	Syms     []xproto.Keysym
}

// Load reads the keyboard mapping.
func Load(c *xgb.Conn) (Map, error) {
	s := xproto.Setup(c)
	n := byte(int(s.MaxKeycode) - int(s.MinKeycode) + 1)
	r, err := xproto.GetKeyboardMapping(c, s.MinKeycode, n).Reply()
	if err != nil {
		return Map{}, err
	}
	return Map{Min: uint8(s.MinKeycode), Max: uint8(s.MaxKeycode), Per: int(r.KeysymsPerKeycode), Syms: r.Keysyms}, nil
}

// Sym returns the keysym of keycode kc at column col, or 0.
func (m Map) Sym(kc uint8, col int) xproto.Keysym {
	if kc < m.Min || kc > m.Max || col < 0 || col >= m.Per {
		return 0
	}
	i := int(kc-m.Min)*m.Per + col
	if i >= len(m.Syms) {
		return 0
	}
	return m.Syms[i]
}

// Find returns a keycode and shift level (0 or 1) that produce ks in the
// first group.
func (m Map) Find(ks xproto.Keysym) (kc uint8, level int, ok bool) {
	for level := 0; level < 2; level++ {
		for k := int(m.Min); k <= int(m.Max); k++ {
			if m.Sym(uint8(k), level) == ks {
				return uint8(k), level, true
			}
		}
	}
	return 0, 0, false
}

// Keycodes returns every keycode that produces ks without or with Shift.
func (m Map) Keycodes(ks xproto.Keysym) []uint8 {
	var out []uint8
	for k := int(m.Min); k <= int(m.Max); k++ {
		if m.Sym(uint8(k), 0) == ks || m.Sym(uint8(k), 1) == ks {
			out = append(out, uint8(k))
		}
	}
	return out
}

// Spare returns the keycodes without any keysym, highest first.
func (m Map) Spare() []uint8 {
	var out []uint8
	for k := int(m.Max); k >= int(m.Min); k-- {
		empty := true
		for col := 0; col < m.Per; col++ {
			if m.Sym(uint8(k), col) != 0 {
				empty = false
				break
			}
		}
		if empty {
			out = append(out, uint8(k))
		}
	}
	return out
}

// Lookup returns the keysym a key press produces with the modifier state,
// following the core protocol rules for Shift, Caps Lock and Num Lock, with
// Mod5 as the third level (AltGr).
func (m Map) Lookup(kc uint8, state uint16) xproto.Keysym {
	base := 0
	if state&ModMod5 != 0 && m.Per > 4 && (m.Sym(kc, 4) != 0 || m.Sym(kc, 5) != 0) {
		base = 4
	}
	lo, hi := m.Sym(kc, base), m.Sym(kc, base+1)
	if hi == 0 {
		hi = upper(lo)
	}
	shift := state&ModShift != 0
	if isKeypad(hi) && state&ModMod2 != 0 {
		if shift {
			return lo
		}
		return hi
	}
	if state&ModLock != 0 && isLower(lo) {
		shift = !shift
	}
	if shift {
		return hi
	}
	return lo
}

func isKeypad(ks xproto.Keysym) bool { return ks >= 0xff80 && ks <= 0xffbd }

func isLower(ks xproto.Keysym) bool { return upper(ks) != ks }

// upper returns the upper case keysym of a Latin-1 letter.
func upper(ks xproto.Keysym) xproto.Keysym {
	switch {
	case ks >= 'a' && ks <= 'z', ks >= 0xe0 && ks <= 0xfe && ks != 0xf7:
		return ks - 0x20
	}
	return ks
}

// Rune returns the character of a keysym, or 0 for keys without one.
func Rune(ks xproto.Keysym) rune {
	switch {
	case ks >= 0x20 && ks <= 0x7e, ks >= 0xa0 && ks <= 0xff:
		return rune(ks)
	case ks >= 0x01000100 && ks <= 0x0110ffff:
		return rune(ks - 0x01000000)
	case ks >= 0xffb0 && ks <= 0xffb9:
		return rune('0' + ks - 0xffb0)
	}
	switch ks {
	case 0xffaa:
		return '*'
	case 0xffab:
		return '+'
	case 0xffad:
		return '-'
	case 0xffae:
		return '.'
	case 0xffaf:
		return '/'
	case 0xff80:
		return ' '
	case 0x20ac:
		return '€'
	}
	return 0
}

// Keysym returns the keysym of a character: Latin-1 keysyms are the code
// point itself, everything else uses the Unicode keysym range.
func Keysym(r rune) xproto.Keysym {
	if r >= 0x20 && r <= 0x7e || r >= 0xa0 && r <= 0xff {
		return xproto.Keysym(r)
	}
	return xproto.Keysym(0x01000000 | r)
}
