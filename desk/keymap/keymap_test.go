package keymap

import (
	"fmt"
	"testing"

	"github.com/helpbuoy/helpbuoy/proto/input"
)

// w3c lists every W3C code the techie must be able to send: the full
// 104/105-key PC keyboard plus F13 to F24.
func w3c() []string {
	var s []string
	for c := 'A'; c <= 'Z'; c++ {
		s = append(s, "Key"+string(c))
	}
	for d := 0; d <= 9; d++ {
		s = append(s, fmt.Sprintf("Digit%d", d), fmt.Sprintf("Numpad%d", d))
	}
	for f := 1; f <= 24; f++ {
		s = append(s, fmt.Sprintf("F%d", f))
	}
	return append(s,
		"Backquote", "Minus", "Equal", "BracketLeft", "BracketRight", "Backslash",
		"Semicolon", "Quote", "Comma", "Period", "Slash", "IntlBackslash", "Space",
		"Escape", "Enter", "Tab", "Backspace", "CapsLock",
		"ShiftLeft", "ShiftRight", "ControlLeft", "ControlRight", "AltLeft", "AltRight",
		"MetaLeft", "MetaRight", "ContextMenu",
		"Insert", "Delete", "Home", "End", "PageUp", "PageDown",
		"ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight",
		"PrintScreen", "ScrollLock", "Pause", "NumLock",
		"NumpadDivide", "NumpadMultiply", "NumpadSubtract", "NumpadAdd",
		"NumpadEnter", "NumpadDecimal", "NumpadEqual",
	)
}

func TestComplete(t *testing.T) {
	have := map[string]bool{}
	for _, k := range Keys() {
		c, _ := Code(k)
		have[c] = true
	}
	want := map[string]bool{}
	for _, c := range w3c() {
		want[c] = true
		if !have[c] {
			t.Errorf("no raylib key maps to %s", c)
		}
	}
	for c := range have {
		if !want[c] {
			t.Errorf("unexpected code %s", c)
		}
	}
}

func TestUnique(t *testing.T) {
	seen := map[string]int32{}
	for _, k := range Keys() {
		c, ok := Code(k)
		if !ok || c == "" {
			t.Fatalf("Keys() contains unmapped %d", k)
		}
		if prev, dup := seen[c]; dup {
			if Aliases[k] != prev && Aliases[prev] != k {
				t.Errorf("%s mapped from both %d and %d", c, prev, k)
			}
			continue
		}
		seen[c] = k
	}
}

func TestKeysSorted(t *testing.T) {
	ks := Keys()
	for i := 1; i < len(ks); i++ {
		if ks[i] <= ks[i-1] {
			t.Fatalf("Keys not sorted at %d", i)
		}
	}
	if _, ok := Code(0); ok {
		t.Error("KeyNull mapped")
	}
}

func TestModifier(t *testing.T) {
	n := 0
	for _, k := range Keys() {
		c, _ := Code(k)
		if Modifier(c) {
			n++
		}
	}
	if n != 8 {
		t.Errorf("%d modifiers, want 8", n)
	}
}

// TestCodesAreKnown checks that every code this key map produces is in
// input.Codes, the list the computer's injector is tested against.
func TestCodesAreKnown(t *testing.T) {
	for _, k := range Keys() {
		c, _ := Code(k)
		if !input.KnownCode(c) {
			t.Errorf("key %d maps to %q, which is not in input.Codes", k, c)
		}
	}
}
