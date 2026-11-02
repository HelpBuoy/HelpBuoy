package input

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

var samples = []any{
	MouseMove{X: 0, Y: 65535},
	MouseMove{X: 32768, Y: 1},
	MouseButton{Button: ButtonLeft, Down: true},
	MouseButton{Button: ButtonForward, Down: false},
	Wheel{DX: -120, DY: 240},
	Wheel{DX: -32768, DY: 32767},
	Key{Code: "KeyA", Down: true},
	Key{Code: "ShiftLeft", Down: false},
	Key{Code: strings.Repeat("A", MaxCode), Down: true},
	Text{Text: ""},
	Text{Text: "héllo, 世界 🙂"},
	Text{Text: strings.Repeat("x", MaxText)},
	ReleaseAll{},
}

func TestRoundTrip(t *testing.T) {
	for _, m := range samples {
		typ, b, err := Encode(m)
		if err != nil {
			t.Fatalf("%#v: %v", m, err)
		}
		got, err := Decode(typ, b)
		if err != nil {
			t.Fatalf("%#v: %v", m, err)
		}
		if !reflect.DeepEqual(got, m) {
			t.Fatalf("got %#v, want %#v", got, m)
		}
		// Pointers encode identically.
		p := reflect.New(reflect.TypeOf(m))
		p.Elem().Set(reflect.ValueOf(m))
		typ2, b2, err := Encode(p.Interface())
		if err != nil || typ2 != typ || !bytes.Equal(b, b2) {
			t.Fatalf("pointer to %#v: %d %x %v", m, typ2, b2, err)
		}
	}
}

func TestEncodeRejects(t *testing.T) {
	for _, m := range []any{
		nil, 42, "x", struct{}{},
		(*MouseMove)(nil), (*Key)(nil), (*ReleaseAll)(nil),
		MouseButton{Button: 5},
		Key{Code: ""},
		Key{Code: "Key A"},
		Key{Code: "Ä"},
		Key{Code: strings.Repeat("A", MaxCode+1)},
		Text{Text: strings.Repeat("x", MaxText+1)},
		Text{Text: "\xff"},
	} {
		if _, _, err := Encode(m); err == nil {
			t.Errorf("%#v accepted", m)
		}
	}
}

func TestDecodeRejects(t *testing.T) {
	cases := []struct {
		typ uint8
		b   []byte
	}{
		{0, nil},
		{7, nil},
		{MsgMouseMove, []byte{1, 2, 3}},
		{MsgMouseMove, []byte{1, 2, 3, 4, 5}},
		{MsgMouseButton, []byte{5, 1}},
		{MsgMouseButton, []byte{0, 2}},
		{MsgMouseButton, []byte{0}},
		{MsgWheel, nil},
		{MsgKey, []byte{1}},
		{MsgKey, []byte{2, 'A'}},
		{MsgKey, []byte{1, ' '}},
		{MsgKey, append([]byte{1}, strings.Repeat("A", MaxCode+1)...)},
		{MsgText, []byte{0xc3}},
		{MsgText, make([]byte, MaxText+1)},
		{MsgReleaseAll, []byte{0}},
	}
	for _, c := range cases {
		if _, err := Decode(c.typ, c.b); err == nil {
			t.Errorf("type %d %x accepted", c.typ, c.b)
		}
	}
	if _, err := Decode(MsgMouseMove, nil); !errors.Is(err, ErrMalformed) {
		t.Errorf("short MouseMove: %v", err)
	}
}

func FuzzDecode(f *testing.F) {
	for _, m := range samples {
		typ, b, _ := Encode(m)
		f.Add(typ, b)
	}
	f.Add(uint8(99), []byte{1, 2})
	f.Fuzz(func(t *testing.T, typ uint8, b []byte) {
		m, err := Decode(typ, b)
		if err != nil {
			return
		}
		typ2, b2, err := Encode(m)
		if err != nil {
			t.Fatalf("decoded %#v does not encode: %v", m, err)
		}
		if typ2 != typ || !bytes.Equal(b2, b) {
			t.Fatalf("re-encoding differs for %#v", m)
		}
	})
}
