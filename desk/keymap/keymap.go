// Package keymap maps raylib key codes to W3C KeyboardEvent.code strings
// ("KeyA", "Enter", "ShiftLeft"), which name physical keys independent of the
// keyboard layout. The client injects them by scan code, so the remote layout
// decides which character a key produces.
//
// The package does not import raylib, so it builds and tests with cgo and
// -race; the key code values are raylib's (GLFW's) and are checked against
// the raylib constants in a test.
package keymap

// Raylib key codes for keys that raylib-go has no constant for, as reported
// by GLFW: the ISO key between left Shift and Z, and F13 to F24.
const (
	KeyWorld1 = 161 // ISO key next to left Shift on X11 and macOS
	KeyWorld2 = 162 // ISO key next to left Shift on Windows
	KeyF13    = 302
	KeyF24    = 313
)

// codes maps raylib key codes to W3C codes.
var codes = map[int32]string{
	32: "Space",
	39: "Quote",
	44: "Comma",
	45: "Minus",
	46: "Period",
	47: "Slash",
	48: "Digit0", 49: "Digit1", 50: "Digit2", 51: "Digit3", 52: "Digit4",
	53: "Digit5", 54: "Digit6", 55: "Digit7", 56: "Digit8", 57: "Digit9",
	59: "Semicolon",
	61: "Equal",
	65: "KeyA", 66: "KeyB", 67: "KeyC", 68: "KeyD", 69: "KeyE", 70: "KeyF",
	71: "KeyG", 72: "KeyH", 73: "KeyI", 74: "KeyJ", 75: "KeyK", 76: "KeyL",
	77: "KeyM", 78: "KeyN", 79: "KeyO", 80: "KeyP", 81: "KeyQ", 82: "KeyR",
	83: "KeyS", 84: "KeyT", 85: "KeyU", 86: "KeyV", 87: "KeyW", 88: "KeyX",
	89: "KeyY", 90: "KeyZ",
	91: "BracketLeft",
	92: "Backslash",
	93: "BracketRight",
	96: "Backquote",

	KeyWorld1: "IntlBackslash",
	KeyWorld2: "IntlBackslash",

	256: "Escape",
	257: "Enter",
	258: "Tab",
	259: "Backspace",
	260: "Insert",
	261: "Delete",
	262: "ArrowRight",
	263: "ArrowLeft",
	264: "ArrowDown",
	265: "ArrowUp",
	266: "PageUp",
	267: "PageDown",
	268: "Home",
	269: "End",
	280: "CapsLock",
	281: "ScrollLock",
	282: "NumLock",
	283: "PrintScreen",
	284: "Pause",

	290: "F1", 291: "F2", 292: "F3", 293: "F4", 294: "F5", 295: "F6",
	296: "F7", 297: "F8", 298: "F9", 299: "F10", 300: "F11", 301: "F12",
	302: "F13", 303: "F14", 304: "F15", 305: "F16", 306: "F17", 307: "F18",
	308: "F19", 309: "F20", 310: "F21", 311: "F22", 312: "F23", 313: "F24",

	320: "Numpad0", 321: "Numpad1", 322: "Numpad2", 323: "Numpad3", 324: "Numpad4",
	325: "Numpad5", 326: "Numpad6", 327: "Numpad7", 328: "Numpad8", 329: "Numpad9",
	330: "NumpadDecimal",
	331: "NumpadDivide",
	332: "NumpadMultiply",
	333: "NumpadSubtract",
	334: "NumpadAdd",
	335: "NumpadEnter",
	336: "NumpadEqual",

	340: "ShiftLeft",
	341: "ControlLeft",
	342: "AltLeft",
	343: "MetaLeft",
	344: "ShiftRight",
	345: "ControlRight",
	346: "AltRight",
	347: "MetaRight",
	348: "ContextMenu",
}

// Aliases lists raylib keys that share a W3C code with another key because
// GLFW reports the same physical key differently per OS.
var Aliases = map[int32]int32{KeyWorld2: KeyWorld1}

// keys is the sorted list of mapped key codes.
var keys []int32

func init() {
	keys = make([]int32, 0, len(codes))
	for k := range codes {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
}

// Code returns the W3C code for a raylib key code.
func Code(key int32) (string, bool) {
	c, ok := codes[key]
	return c, ok
}

// Keys returns every mapped raylib key code in ascending order. The viewer
// polls each of them every frame. The slice must not be modified.
func Keys() []int32 { return keys }

// Modifier reports whether a W3C code is a modifier key.
func Modifier(code string) bool {
	switch code {
	case "ShiftLeft", "ShiftRight", "ControlLeft", "ControlRight",
		"AltLeft", "AltRight", "MetaLeft", "MetaRight":
		return true
	}
	return false
}
