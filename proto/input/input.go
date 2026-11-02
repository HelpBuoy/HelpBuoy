// Package input defines the messages on the inner "input" channel, sent by
// the helper and injected by the client. Coordinates are normalized to
// 0..65535 over the shared monitor so the two sides need not agree on size.
//
// # Encodings
//
// Each message is one wire frame whose type byte is its Msg constant. All
// integers are big-endian; payloads have a fixed length except Key and Text.
//
//	MouseMove:   uint16 X | uint16 Y
//	MouseButton: uint8 Button (0..4) | uint8 Down (0 or 1)
//	Wheel:       int16 DX | int16 DY
//	Key:         uint8 Down (0 or 1) | Code, 1..MaxCode ASCII letters and digits
//	Text:        UTF-8, at most MaxText bytes
//	ReleaseAll:  empty
package input

import (
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf8"
)

// Message type bytes.
const (
	MsgMouseMove   uint8 = 1
	MsgMouseButton uint8 = 2
	MsgWheel       uint8 = 3
	MsgKey         uint8 = 4
	MsgText        uint8 = 5
	MsgReleaseAll  uint8 = 6
)

// Limits on variable-length fields.
const (
	MaxText = 4096 // bytes of UTF-8 in one Text message
	MaxCode = 32   // bytes in a Key code
)

// ErrMalformed is wrapped by every Decode error for a bad payload.
var ErrMalformed = errors.New("input: malformed message")

// Mouse buttons.
const (
	ButtonLeft    uint8 = 0
	ButtonRight   uint8 = 1
	ButtonMiddle  uint8 = 2
	ButtonBack    uint8 = 3
	ButtonForward uint8 = 4
)

// MouseMove moves the pointer to an absolute, normalized position.
type MouseMove struct{ X, Y uint16 }

// MouseButton presses or releases one mouse button.
type MouseButton struct {
	Button uint8
	Down   bool
}

// Wheel scrolls; positive DY is away from the user, positive DX to the right.
type Wheel struct{ DX, DY int16 } // in 1/120 notches, like WHEEL_DELTA

// Key is a physical key by W3C KeyboardEvent.code ("KeyA", "Enter", "ShiftLeft").
type Key struct {
	Code string
	Down bool
}

// Text types characters without a physical key.
type Text struct{ Text string } // characters without a physical key

// ReleaseAll releases every key and button the client holds down.
type ReleaseAll struct{}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

func validCode(s string) bool {
	if len(s) == 0 || len(s) > MaxCode {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9') {
			return false
		}
	}
	return true
}

// Encode returns the type byte and payload for one message value. It accepts
// the message structs above by value or by pointer, and returns an error for
// other types, nil pointers, unknown buttons, invalid key codes and text that
// is not UTF-8 or longer than MaxText.
func Encode(m any) (uint8, []byte, error) {
	switch v := m.(type) {
	case MouseMove:
		b := binary.BigEndian.AppendUint16(make([]byte, 0, 4), v.X)
		return MsgMouseMove, binary.BigEndian.AppendUint16(b, v.Y), nil
	case MouseButton:
		if v.Button > ButtonForward {
			return 0, nil, fmt.Errorf("input: unknown mouse button %d", v.Button)
		}
		return MsgMouseButton, []byte{v.Button, boolByte(v.Down)}, nil
	case Wheel:
		b := binary.BigEndian.AppendUint16(make([]byte, 0, 4), uint16(v.DX))
		return MsgWheel, binary.BigEndian.AppendUint16(b, uint16(v.DY)), nil
	case Key:
		if !validCode(v.Code) {
			return 0, nil, fmt.Errorf("input: invalid key code %q", v.Code)
		}
		return MsgKey, append([]byte{boolByte(v.Down)}, v.Code...), nil
	case Text:
		if len(v.Text) > MaxText {
			return 0, nil, fmt.Errorf("input: text of %d bytes exceeds %d", len(v.Text), MaxText)
		}
		if !utf8.ValidString(v.Text) {
			return 0, nil, errors.New("input: text is not UTF-8")
		}
		return MsgText, []byte(v.Text), nil
	case ReleaseAll:
		return MsgReleaseAll, []byte{}, nil

	case *MouseMove:
		return encodePtr(v)
	case *MouseButton:
		return encodePtr(v)
	case *Wheel:
		return encodePtr(v)
	case *Key:
		return encodePtr(v)
	case *Text:
		return encodePtr(v)
	case *ReleaseAll:
		return encodePtr(v)
	}
	return 0, nil, fmt.Errorf("input: cannot encode %T", m)
}

func encodePtr[T any](p *T) (uint8, []byte, error) {
	if p == nil {
		return 0, nil, fmt.Errorf("input: cannot encode nil %T", p)
	}
	return Encode(*p)
}

func malformed(typ uint8, why string) error {
	return fmt.Errorf("%w: type %d: %s", ErrMalformed, typ, why)
}

// Decode parses a payload into one of the message structs above (by value).
// It never panics; an unknown type or invalid payload returns an error.
func Decode(typ uint8, b []byte) (any, error) {
	fixed := func(n int) error {
		if len(b) != n {
			return malformed(typ, fmt.Sprintf("%d bytes, want %d", len(b), n))
		}
		return nil
	}
	flag := func(c byte) (bool, error) {
		if c > 1 {
			return false, malformed(typ, "bad down flag")
		}
		return c == 1, nil
	}
	switch typ {
	case MsgMouseMove:
		if err := fixed(4); err != nil {
			return nil, err
		}
		return MouseMove{X: binary.BigEndian.Uint16(b), Y: binary.BigEndian.Uint16(b[2:])}, nil
	case MsgMouseButton:
		if err := fixed(2); err != nil {
			return nil, err
		}
		if b[0] > ButtonForward {
			return nil, malformed(typ, "unknown button")
		}
		down, err := flag(b[1])
		if err != nil {
			return nil, err
		}
		return MouseButton{Button: b[0], Down: down}, nil
	case MsgWheel:
		if err := fixed(4); err != nil {
			return nil, err
		}
		return Wheel{DX: int16(binary.BigEndian.Uint16(b)), DY: int16(binary.BigEndian.Uint16(b[2:]))}, nil
	case MsgKey:
		if len(b) < 2 || len(b) > 1+MaxCode {
			return nil, malformed(typ, "bad length")
		}
		down, err := flag(b[0])
		if err != nil {
			return nil, err
		}
		code := string(b[1:])
		if !validCode(code) {
			return nil, malformed(typ, "invalid key code")
		}
		return Key{Code: code, Down: down}, nil
	case MsgText:
		if len(b) > MaxText {
			return nil, malformed(typ, "text too long")
		}
		if !utf8.Valid(b) {
			return nil, malformed(typ, "text is not UTF-8")
		}
		return Text{Text: string(b)}, nil
	case MsgReleaseAll:
		if err := fixed(0); err != nil {
			return nil, err
		}
		return ReleaseAll{}, nil
	}
	return nil, fmt.Errorf("input: unknown message type %d", typ)
}
