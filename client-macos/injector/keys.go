package injector

// vk maps W3C KeyboardEvent.code values to macOS virtual key codes
// (kVK_* in Carbon's Events.h). They name physical positions, so the local
// keyboard layout decides which character a key produces, as on the other
// clients. The PC keys map by position: AltLeft is Option and MetaLeft (the
// Windows key) is Command. Keys a Mac keyboard lacks go where macOS expects
// them: PrintScreen, ScrollLock and Pause are F13 to F15, Insert is Help.
//
// Power is left out on purpose: a stray press would ask to shut down.
var vk = map[string]uint16{
	// Writing system keys.
	"KeyA":          0x00,
	"KeyS":          0x01,
	"KeyD":          0x02,
	"KeyF":          0x03,
	"KeyH":          0x04,
	"KeyG":          0x05,
	"KeyZ":          0x06,
	"KeyX":          0x07,
	"KeyC":          0x08,
	"KeyV":          0x09,
	"IntlBackslash": 0x0A, // ISO section key
	"KeyB":          0x0B,
	"KeyQ":          0x0C,
	"KeyW":          0x0D,
	"KeyE":          0x0E,
	"KeyR":          0x0F,
	"KeyY":          0x10,
	"KeyT":          0x11,
	"Digit1":        0x12,
	"Digit2":        0x13,
	"Digit3":        0x14,
	"Digit4":        0x15,
	"Digit6":        0x16,
	"Digit5":        0x17,
	"Equal":         0x18,
	"Digit9":        0x19,
	"Digit7":        0x1A,
	"Minus":         0x1B,
	"Digit8":        0x1C,
	"Digit0":        0x1D,
	"BracketRight":  0x1E,
	"KeyO":          0x1F,
	"KeyU":          0x20,
	"BracketLeft":   0x21,
	"KeyI":          0x22,
	"KeyP":          0x23,
	"KeyL":          0x25,
	"KeyJ":          0x26,
	"Quote":         0x27,
	"KeyK":          0x28,
	"Semicolon":     0x29,
	"Backslash":     0x2A,
	"IntlHash":      0x2A, // same position as Backslash on ISO boards
	"Comma":         0x2B,
	"Slash":         0x2C,
	"KeyN":          0x2D,
	"KeyM":          0x2E,
	"Period":        0x2F,
	"Backquote":     0x32,
	"IntlYen":       0x5D,
	"IntlRo":        0x5E,

	// Functional keys.
	"Enter":        0x24,
	"Tab":          0x30,
	"Space":        0x31,
	"Backspace":    0x33,
	"Escape":       0x35,
	"MetaRight":    0x36,
	"MetaLeft":     0x37,
	"ShiftLeft":    0x38,
	"CapsLock":     0x39,
	"AltLeft":      0x3A,
	"ControlLeft":  0x3B,
	"ShiftRight":   0x3C,
	"AltRight":     0x3D,
	"ControlRight": 0x3E,
	"Fn":           0x3F,
	"ContextMenu":  0x6E,

	// IME and language keys.
	"Lang1":       0x68, // Kana
	"Lang2":       0x66, // Eisu
	"KanaMode":    0x68,
	"NumpadComma": 0x5F,

	// Function keys.
	"F1":  0x7A,
	"F2":  0x78,
	"F3":  0x63,
	"F4":  0x76,
	"F5":  0x60,
	"F6":  0x61,
	"F7":  0x62,
	"F8":  0x64,
	"F9":  0x65,
	"F10": 0x6D,
	"F11": 0x67,
	"F12": 0x6F,
	"F13": 0x69,
	"F14": 0x6B,
	"F15": 0x71,
	"F16": 0x6A,
	"F17": 0x40,
	"F18": 0x4F,
	"F19": 0x50,
	"F20": 0x5A,

	"PrintScreen": 0x69, // F13
	"ScrollLock":  0x6B, // F14
	"Pause":       0x71, // F15

	// Control pad and arrows.
	"Insert":     0x72, // Help
	"Home":       0x73,
	"PageUp":     0x74,
	"Delete":     0x75, // forward delete
	"End":        0x77,
	"PageDown":   0x79,
	"ArrowLeft":  0x7B,
	"ArrowRight": 0x7C,
	"ArrowDown":  0x7D,
	"ArrowUp":    0x7E,

	// Numpad.
	"NumLock":        0x47, // Clear
	"NumpadDivide":   0x4B,
	"NumpadMultiply": 0x43,
	"NumpadSubtract": 0x4E,
	"NumpadAdd":      0x45,
	"NumpadEnter":    0x4C,
	"NumpadDecimal":  0x41,
	"NumpadEqual":    0x51,
	"Numpad0":        0x52,
	"Numpad1":        0x53,
	"Numpad2":        0x54,
	"Numpad3":        0x55,
	"Numpad4":        0x56,
	"Numpad5":        0x57,
	"Numpad6":        0x58,
	"Numpad7":        0x59,
	"Numpad8":        0x5B,
	"Numpad9":        0x5C,

	// Volume keys; the other media keys are not keyboard events on macOS.
	"AudioVolumeMute": 0x4A,
	"AudioVolumeDown": 0x49,
	"AudioVolumeUp":   0x48,
}

const vkEscape = 0x35

// CGEventFlags.
const (
	flagCapsLock    = 0x00010000
	flagShift       = 0x00020000
	flagControl     = 0x00040000
	flagOption      = 0x00080000
	flagCommand     = 0x00100000
	flagNumericPad  = 0x00200000
	flagSecondaryFn = 0x00800000
)

// modifierFlag returns the flag a modifier key holds, or 0 for other keys.
func modifierFlag(code uint16) uint64 {
	switch code {
	case 0x38, 0x3C:
		return flagShift
	case 0x3B, 0x3E:
		return flagControl
	case 0x3A, 0x3D:
		return flagOption
	case 0x37, 0x36:
		return flagCommand
	case 0x3F:
		return flagSecondaryFn
	}
	return 0
}

// keyFlags returns the flags a real keyboard adds to a key's events: the
// arrows count as numeric pad keys, and the arrows, the control pad and the
// function keys as Fn keys. Some applications look at these.
func keyFlags(code uint16) uint64 {
	switch {
	case code >= 0x7B && code <= 0x7E:
		return flagNumericPad | flagSecondaryFn
	case code >= 0x72 && code <= 0x79, code == 0x60, code == 0x61, code == 0x62, code == 0x63,
		code == 0x64, code == 0x65, code == 0x67, code == 0x69, code == 0x6A, code == 0x6B,
		code == 0x6D, code == 0x6F, code == 0x71, code == 0x40, code == 0x4F, code == 0x50, code == 0x5A:
		return flagSecondaryFn
	case code == 0x41, code == 0x43, code == 0x45, code == 0x47, code == 0x4B, code == 0x4C, code == 0x4E,
		code >= 0x51 && code <= 0x59, code == 0x5B, code == 0x5C:
		return flagNumericPad
	}
	return 0
}
