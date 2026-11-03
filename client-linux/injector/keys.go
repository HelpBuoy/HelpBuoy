package injector

// evdev maps W3C KeyboardEvent.code values to Linux evdev key codes
// (linux/input-event-codes.h). X servers with the evdev or libinput driver
// use the evdev code plus 8 as the X keycode, so keys are injected by
// physical position and the local keyboard layout decides which character a
// key produces, like the scan codes of the Windows client.
//
// Power, Sleep and WakeUp are left out on purpose: a stray press would put
// the user's machine to sleep and end the session.
var evdev = map[string]uint8{
	// Writing system keys.
	"Backquote":     41,
	"Digit1":        2,
	"Digit2":        3,
	"Digit3":        4,
	"Digit4":        5,
	"Digit5":        6,
	"Digit6":        7,
	"Digit7":        8,
	"Digit8":        9,
	"Digit9":        10,
	"Digit0":        11,
	"Minus":         12,
	"Equal":         13,
	"KeyQ":          16,
	"KeyW":          17,
	"KeyE":          18,
	"KeyR":          19,
	"KeyT":          20,
	"KeyY":          21,
	"KeyU":          22,
	"KeyI":          23,
	"KeyO":          24,
	"KeyP":          25,
	"BracketLeft":   26,
	"BracketRight":  27,
	"KeyA":          30,
	"KeyS":          31,
	"KeyD":          32,
	"KeyF":          33,
	"KeyG":          34,
	"KeyH":          35,
	"KeyJ":          36,
	"KeyK":          37,
	"KeyL":          38,
	"Semicolon":     39,
	"Quote":         40,
	"Backslash":     43,
	"IntlHash":      43, // same position as Backslash on ISO boards
	"IntlBackslash": 86,
	"KeyZ":          44,
	"KeyX":          45,
	"KeyC":          46,
	"KeyV":          47,
	"KeyB":          48,
	"KeyN":          49,
	"KeyM":          50,
	"Comma":         51,
	"Period":        52,
	"Slash":         53,
	"IntlRo":        89,
	"IntlYen":       124,

	// Functional keys.
	"Escape":       1,
	"Backspace":    14,
	"Tab":          15,
	"Enter":        28,
	"ControlLeft":  29,
	"ShiftLeft":    42,
	"ShiftRight":   54,
	"AltLeft":      56,
	"Space":        57,
	"CapsLock":     58,
	"ControlRight": 97,
	"AltRight":     100,
	"MetaLeft":     125,
	"MetaRight":    126,
	"ContextMenu":  127,

	// IME and language keys.
	"KanaMode":    93,
	"Convert":     92,
	"NonConvert":  94,
	"Lang1":       122, // Hangul/English
	"Lang2":       123, // Hanja
	"Lang3":       90,  // Katakana
	"Lang4":       91,  // Hiragana
	"Lang5":       85,  // Zenkaku/Hankaku
	"NumpadComma": 121,

	// Function keys.
	"F1":  59,
	"F2":  60,
	"F3":  61,
	"F4":  62,
	"F5":  63,
	"F6":  64,
	"F7":  65,
	"F8":  66,
	"F9":  67,
	"F10": 68,
	"F11": 87,
	"F12": 88,
	"F13": 183,
	"F14": 184,
	"F15": 185,
	"F16": 186,
	"F17": 187,
	"F18": 188,
	"F19": 189,
	"F20": 190,
	"F21": 191,
	"F22": 192,
	"F23": 193,
	"F24": 194,

	"PrintScreen": 99,
	"ScrollLock":  70,
	"Pause":       119,

	// Control pad and arrows.
	"Insert":     110,
	"Home":       102,
	"PageUp":     104,
	"Delete":     111,
	"End":        107,
	"PageDown":   109,
	"ArrowUp":    103,
	"ArrowLeft":  105,
	"ArrowDown":  108,
	"ArrowRight": 106,

	// Numpad.
	"NumLock":        69,
	"NumpadDivide":   98,
	"NumpadMultiply": 55,
	"NumpadSubtract": 74,
	"NumpadAdd":      78,
	"NumpadEnter":    96,
	"NumpadDecimal":  83,
	"NumpadEqual":    117,
	"Numpad0":        82,
	"Numpad1":        79,
	"Numpad2":        80,
	"Numpad3":        81,
	"Numpad4":        75,
	"Numpad5":        76,
	"Numpad6":        77,
	"Numpad7":        71,
	"Numpad8":        72,
	"Numpad9":        73,

	// Media keys.
	"AudioVolumeMute":    113,
	"AudioVolumeDown":    114,
	"AudioVolumeUp":      115,
	"MediaTrackNext":     163,
	"MediaTrackPrevious": 165,
	"MediaStop":          166,
	"MediaPlayPause":     164,
	"LaunchMail":         155,
	"MediaSelect":        226,
	"LaunchApp1":         157, // My Computer
	"LaunchApp2":         140, // Calculator

	// Browser keys.
	"BrowserBack":      158,
	"BrowserForward":   159,
	"BrowserRefresh":   173,
	"BrowserStop":      128,
	"BrowserSearch":    217,
	"BrowserFavorites": 156,
	"BrowserHome":      172,
}

// keycode returns the X keycode of the key with W3C code code.
func keycode(code string) (uint8, bool) {
	k, ok := evdev[code]
	if !ok {
		return 0, false
	}
	return k + 8, true
}
