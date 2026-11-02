package wire

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	frames := []struct {
		typ uint8
		p   []byte
	}{{1, nil}, {2, []byte("hello")}, {255, bytes.Repeat([]byte{7}, 100000)}}
	for _, f := range frames {
		if err := w.WriteFrame(f.typ, f.p); err != nil {
			t.Fatal(err)
		}
	}
	r := NewReader(&buf)
	for _, f := range frames {
		typ, p, err := r.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		if typ != f.typ || !bytes.Equal(p, f.p) {
			t.Fatalf("got type %d len %d, want type %d len %d", typ, len(p), f.typ, len(f.p))
		}
	}
	if _, _, err := r.ReadFrame(); err != io.EOF {
		t.Fatalf("end: got %v, want io.EOF", err)
	}
}

func TestTruncated(t *testing.T) {
	var buf bytes.Buffer
	NewWriter(&buf).WriteFrame(3, []byte("abcdef"))
	b := buf.Bytes()[:buf.Len()-2]
	if _, _, err := NewReader(bytes.NewReader(b)).ReadFrame(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("got %v, want ErrUnexpectedEOF", err)
	}
}

func TestTooLarge(t *testing.T) {
	b := []byte{0xff, 0xff, 0xff, 0xff, 1}
	if _, _, err := NewReader(bytes.NewReader(b)).ReadFrame(); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v, want ErrTooLarge", err)
	}
}

func FuzzReadFrame(f *testing.F) {
	f.Add([]byte{0, 0, 0, 2, 1, 'x'})
	f.Add([]byte{0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, b []byte) {
		r := NewReader(bytes.NewReader(b))
		for i := 0; i < 8; i++ {
			if _, _, err := r.ReadFrame(); err != nil {
				return
			}
		}
	})
}
