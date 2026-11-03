//go:build !cgo

package keymap

import (
	"testing"

	rl "github.com/gen2brain/raylib-go/raylib"
)

// TestRaylibConstants checks that every raylib-go keyboard constant is mapped
// to the expected code, so the numeric table above matches raylib. It only
// builds without cgo (the purego raylib), so -race runs skip it.
func TestRaylibConstants(t *testing.T) {
	want := map[int32]string{
		rl.KeySpace: "Space", rl.KeyEscape: "Escape", rl.KeyEnter: "Enter", rl.KeyTab: "Tab",
		rl.KeyBackspace: "Backspace", rl.KeyInsert: "Insert", rl.KeyDelete: "Delete",
		rl.KeyRight: "ArrowRight", rl.KeyLeft: "ArrowLeft", rl.KeyDown: "ArrowDown", rl.KeyUp: "ArrowUp",
		rl.KeyPageUp: "PageUp", rl.KeyPageDown: "PageDown", rl.KeyHome: "Home", rl.KeyEnd: "End",
		rl.KeyCapsLock: "CapsLock", rl.KeyScrollLock: "ScrollLock", rl.KeyNumLock: "NumLock",
		rl.KeyPrintScreen: "PrintScreen", rl.KeyPause: "Pause",
		rl.KeyF1: "F1", rl.KeyF2: "F2", rl.KeyF3: "F3", rl.KeyF4: "F4", rl.KeyF5: "F5", rl.KeyF6: "F6",
		rl.KeyF7: "F7", rl.KeyF8: "F8", rl.KeyF9: "F9", rl.KeyF10: "F10", rl.KeyF11: "F11", rl.KeyF12: "F12",
		rl.KeyLeftShift: "ShiftLeft", rl.KeyLeftControl: "ControlLeft", rl.KeyLeftAlt: "AltLeft",
		rl.KeyLeftSuper: "MetaLeft", rl.KeyRightShift: "ShiftRight", rl.KeyRightControl: "ControlRight",
		rl.KeyRightAlt: "AltRight", rl.KeyRightSuper: "MetaRight", rl.KeyKbMenu: "ContextMenu",
		rl.KeyLeftBracket: "BracketLeft", rl.KeyBackSlash: "Backslash", rl.KeyRightBracket: "BracketRight",
		rl.KeyGrave: "Backquote",
		rl.KeyKp0:   "Numpad0", rl.KeyKp1: "Numpad1", rl.KeyKp2: "Numpad2", rl.KeyKp3: "Numpad3",
		rl.KeyKp4: "Numpad4", rl.KeyKp5: "Numpad5", rl.KeyKp6: "Numpad6", rl.KeyKp7: "Numpad7",
		rl.KeyKp8: "Numpad8", rl.KeyKp9: "Numpad9", rl.KeyKpDecimal: "NumpadDecimal",
		rl.KeyKpDivide: "NumpadDivide", rl.KeyKpMultiply: "NumpadMultiply",
		rl.KeyKpSubtract: "NumpadSubtract", rl.KeyKpAdd: "NumpadAdd", rl.KeyKpEnter: "NumpadEnter",
		rl.KeyKpEqual:    "NumpadEqual",
		rl.KeyApostrophe: "Quote", rl.KeyComma: "Comma", rl.KeyMinus: "Minus", rl.KeyPeriod: "Period",
		rl.KeySlash: "Slash", rl.KeySemicolon: "Semicolon", rl.KeyEqual: "Equal",
		rl.KeyZero: "Digit0", rl.KeyOne: "Digit1", rl.KeyTwo: "Digit2", rl.KeyThree: "Digit3",
		rl.KeyFour: "Digit4", rl.KeyFive: "Digit5", rl.KeySix: "Digit6", rl.KeySeven: "Digit7",
		rl.KeyEight: "Digit8", rl.KeyNine: "Digit9",
		rl.KeyA: "KeyA", rl.KeyB: "KeyB", rl.KeyC: "KeyC", rl.KeyD: "KeyD", rl.KeyE: "KeyE",
		rl.KeyF: "KeyF", rl.KeyG: "KeyG", rl.KeyH: "KeyH", rl.KeyI: "KeyI", rl.KeyJ: "KeyJ",
		rl.KeyK: "KeyK", rl.KeyL: "KeyL", rl.KeyM: "KeyM", rl.KeyN: "KeyN", rl.KeyO: "KeyO",
		rl.KeyP: "KeyP", rl.KeyQ: "KeyQ", rl.KeyR: "KeyR", rl.KeyS: "KeyS", rl.KeyT: "KeyT",
		rl.KeyU: "KeyU", rl.KeyV: "KeyV", rl.KeyW: "KeyW", rl.KeyX: "KeyX", rl.KeyY: "KeyY",
		rl.KeyZ: "KeyZ",
	}
	for k, c := range want {
		if got, _ := Code(k); got != c {
			t.Errorf("raylib key %d: got %q, want %q", k, got, c)
		}
	}
	// Android-only keys must stay unmapped.
	for _, k := range []int32{rl.KeyBack, rl.KeyMenu, rl.KeyVolumeUp, rl.KeyVolumeDown} {
		if c, ok := Code(k); ok {
			t.Errorf("android key %d mapped to %q", k, c)
		}
	}
}
