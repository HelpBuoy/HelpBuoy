package injector

// scanKey says how to inject one physical key.
//
// Most keys are sent by PS/2 set 1 scan code with KEYEVENTF_SCANCODE, so the
// remote keyboard layout decides which character a key produces, and Ext adds
// KEYEVENTF_EXTENDEDKEY for keys whose scan code has the E0 prefix. Keys that
// have no plain set 1 make code (Pause sends E1 1D 45) or that Windows layouts
// do not reliably map from a scan code (media and browser keys) set VK and are
// sent by virtual-key code instead, which is layout independent for them;
// Scan and Ext are then only informational for the receiving window.
type scanKey struct {
	Scan uint16
	Ext  bool
	VK   uint16
}

// keyTable maps W3C KeyboardEvent.code values to keys, following the
// Windows column of the UI Events KeyboardEvent code specification.
//
// Power, Sleep and WakeUp are left out on purpose: a stray press would put
// the user's machine to sleep and end the session.
var keyTable = map[string]scanKey{
	// Writing system keys.
	"Backquote":     {Scan: 0x29},
	"Digit1":        {Scan: 0x02},
	"Digit2":        {Scan: 0x03},
	"Digit3":        {Scan: 0x04},
	"Digit4":        {Scan: 0x05},
	"Digit5":        {Scan: 0x06},
	"Digit6":        {Scan: 0x07},
	"Digit7":        {Scan: 0x08},
	"Digit8":        {Scan: 0x09},
	"Digit9":        {Scan: 0x0a},
	"Digit0":        {Scan: 0x0b},
	"Minus":         {Scan: 0x0c},
	"Equal":         {Scan: 0x0d},
	"KeyQ":          {Scan: 0x10},
	"KeyW":          {Scan: 0x11},
	"KeyE":          {Scan: 0x12},
	"KeyR":          {Scan: 0x13},
	"KeyT":          {Scan: 0x14},
	"KeyY":          {Scan: 0x15},
	"KeyU":          {Scan: 0x16},
	"KeyI":          {Scan: 0x17},
	"KeyO":          {Scan: 0x18},
	"KeyP":          {Scan: 0x19},
	"BracketLeft":   {Scan: 0x1a},
	"BracketRight":  {Scan: 0x1b},
	"KeyA":          {Scan: 0x1e},
	"KeyS":          {Scan: 0x1f},
	"KeyD":          {Scan: 0x20},
	"KeyF":          {Scan: 0x21},
	"KeyG":          {Scan: 0x22},
	"KeyH":          {Scan: 0x23},
	"KeyJ":          {Scan: 0x24},
	"KeyK":          {Scan: 0x25},
	"KeyL":          {Scan: 0x26},
	"Semicolon":     {Scan: 0x27},
	"Quote":         {Scan: 0x28},
	"Backslash":     {Scan: 0x2b},
	"IntlHash":      {Scan: 0x2b}, // same position as Backslash on ISO boards
	"IntlBackslash": {Scan: 0x56},
	"KeyZ":          {Scan: 0x2c},
	"KeyX":          {Scan: 0x2d},
	"KeyC":          {Scan: 0x2e},
	"KeyV":          {Scan: 0x2f},
	"KeyB":          {Scan: 0x30},
	"KeyN":          {Scan: 0x31},
	"KeyM":          {Scan: 0x32},
	"Comma":         {Scan: 0x33},
	"Period":        {Scan: 0x34},
	"Slash":         {Scan: 0x35},
	"IntlRo":        {Scan: 0x73},
	"IntlYen":       {Scan: 0x7d},

	// Functional keys.
	"Escape":       {Scan: 0x01},
	"Backspace":    {Scan: 0x0e},
	"Tab":          {Scan: 0x0f},
	"Enter":        {Scan: 0x1c},
	"ControlLeft":  {Scan: 0x1d},
	"ShiftLeft":    {Scan: 0x2a},
	"ShiftRight":   {Scan: 0x36},
	"AltLeft":      {Scan: 0x38},
	"Space":        {Scan: 0x39},
	"CapsLock":     {Scan: 0x3a},
	"ControlRight": {Scan: 0x1d, Ext: true},
	"AltRight":     {Scan: 0x38, Ext: true},
	"MetaLeft":     {Scan: 0x5b, Ext: true},
	"MetaRight":    {Scan: 0x5c, Ext: true},
	"ContextMenu":  {Scan: 0x5d, Ext: true},

	// IME and language keys.
	"KanaMode":    {Scan: 0x70},
	"Convert":     {Scan: 0x79},
	"NonConvert":  {Scan: 0x7b},
	"Lang1":       {Scan: 0x72}, // Hangul/English
	"Lang2":       {Scan: 0x71}, // Hanja
	"Lang3":       {Scan: 0x78}, // Katakana
	"Lang4":       {Scan: 0x77}, // Hiragana
	"Lang5":       {Scan: 0x76}, // Zenkaku/Hankaku, same code as F24
	"NumpadComma": {Scan: 0x7e},

	// Function keys.
	"F1":  {Scan: 0x3b},
	"F2":  {Scan: 0x3c},
	"F3":  {Scan: 0x3d},
	"F4":  {Scan: 0x3e},
	"F5":  {Scan: 0x3f},
	"F6":  {Scan: 0x40},
	"F7":  {Scan: 0x41},
	"F8":  {Scan: 0x42},
	"F9":  {Scan: 0x43},
	"F10": {Scan: 0x44},
	"F11": {Scan: 0x57},
	"F12": {Scan: 0x58},
	"F13": {Scan: 0x64},
	"F14": {Scan: 0x65},
	"F15": {Scan: 0x66},
	"F16": {Scan: 0x67},
	"F17": {Scan: 0x68},
	"F18": {Scan: 0x69},
	"F19": {Scan: 0x6a},
	"F20": {Scan: 0x6b},
	"F21": {Scan: 0x6c},
	"F22": {Scan: 0x6d},
	"F23": {Scan: 0x6e},
	"F24": {Scan: 0x76},

	"PrintScreen": {Scan: 0x37, Ext: true},
	"ScrollLock":  {Scan: 0x46},
	"Pause":       {Scan: 0x45, VK: 0x13}, // E1 1D 45 has no single scan code

	// Control pad and arrows.
	"Insert":     {Scan: 0x52, Ext: true},
	"Home":       {Scan: 0x47, Ext: true},
	"PageUp":     {Scan: 0x49, Ext: true},
	"Delete":     {Scan: 0x53, Ext: true},
	"End":        {Scan: 0x4f, Ext: true},
	"PageDown":   {Scan: 0x51, Ext: true},
	"ArrowUp":    {Scan: 0x48, Ext: true},
	"ArrowLeft":  {Scan: 0x4b, Ext: true},
	"ArrowDown":  {Scan: 0x50, Ext: true},
	"ArrowRight": {Scan: 0x4d, Ext: true},

	// Numpad.
	"NumLock":        {Scan: 0x45, Ext: true},
	"NumpadDivide":   {Scan: 0x35, Ext: true},
	"NumpadMultiply": {Scan: 0x37},
	"NumpadSubtract": {Scan: 0x4a},
	"NumpadAdd":      {Scan: 0x4e},
	"NumpadEnter":    {Scan: 0x1c, Ext: true},
	"NumpadDecimal":  {Scan: 0x53},
	"NumpadEqual":    {Scan: 0x59},
	"Numpad0":        {Scan: 0x52},
	"Numpad1":        {Scan: 0x4f},
	"Numpad2":        {Scan: 0x50},
	"Numpad3":        {Scan: 0x51},
	"Numpad4":        {Scan: 0x4b},
	"Numpad5":        {Scan: 0x4c},
	"Numpad6":        {Scan: 0x4d},
	"Numpad7":        {Scan: 0x47},
	"Numpad8":        {Scan: 0x48},
	"Numpad9":        {Scan: 0x49},

	// Media keys, by virtual-key code.
	"AudioVolumeMute":    {Scan: 0x20, Ext: true, VK: 0xad},
	"AudioVolumeDown":    {Scan: 0x2e, Ext: true, VK: 0xae},
	"AudioVolumeUp":      {Scan: 0x30, Ext: true, VK: 0xaf},
	"MediaTrackNext":     {Scan: 0x19, Ext: true, VK: 0xb0},
	"MediaTrackPrevious": {Scan: 0x10, Ext: true, VK: 0xb1},
	"MediaStop":          {Scan: 0x24, Ext: true, VK: 0xb2},
	"MediaPlayPause":     {Scan: 0x22, Ext: true, VK: 0xb3},
	"LaunchMail":         {Scan: 0x6c, Ext: true, VK: 0xb4},
	"MediaSelect":        {Scan: 0x6d, Ext: true, VK: 0xb5},
	"LaunchApp1":         {Scan: 0x6b, Ext: true, VK: 0xb6},
	"LaunchApp2":         {Scan: 0x21, Ext: true, VK: 0xb7},

	// Browser keys, by virtual-key code.
	"BrowserBack":      {Scan: 0x6a, Ext: true, VK: 0xa6},
	"BrowserForward":   {Scan: 0x69, Ext: true, VK: 0xa7},
	"BrowserRefresh":   {Scan: 0x67, Ext: true, VK: 0xa8},
	"BrowserStop":      {Scan: 0x68, Ext: true, VK: 0xa9},
	"BrowserSearch":    {Scan: 0x65, Ext: true, VK: 0xaa},
	"BrowserFavorites": {Scan: 0x66, Ext: true, VK: 0xab},
	"BrowserHome":      {Scan: 0x32, Ext: true, VK: 0xac},
}

// lookupKey returns how to inject the key with W3C code code.
func lookupKey(code string) (scanKey, bool) {
	k, ok := keyTable[code]
	return k, ok
}

// keyEvent returns the keyboard event that presses or releases k.
func keyEvent(k scanKey, down bool) event {
	e := event{scan: k.Scan}
	if k.VK != 0 {
		e.vk = k.VK
	} else {
		e.flags |= keyeventfScancode
	}
	if k.Ext {
		e.flags |= keyeventfExtendedKey
	}
	if !down {
		e.flags |= keyeventfKeyUp
	}
	return e
}
