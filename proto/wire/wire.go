// Package wire is the length-prefixed framing used on every help-me-win
// stream: the sv-control channel and the inner screen and input channels.
//
// A frame is: uint32 length (big-endian, counts type + payload) | uint8 type | payload.
package wire

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
)

// MaxFrame is the largest accepted frame (type byte + payload). Screen frames
// carry many tiles, so this is generous; control frames are tiny.
const MaxFrame = 16 << 20

// ErrTooLarge is returned for frames above MaxFrame.
var ErrTooLarge = errors.New("wire: frame too large")

// Writer writes frames. It is safe for concurrent use.
type Writer struct {
	mu sync.Mutex
	w  io.Writer
}

// NewWriter returns a Writer on w.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// WriteFrame writes one frame. The header and payload go out in a single Write.
func (fw *Writer) WriteFrame(typ uint8, payload []byte) error {
	n := 1 + len(payload)
	if n > MaxFrame {
		return ErrTooLarge
	}
	buf := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(buf, uint32(n))
	buf[4] = typ
	copy(buf[5:], payload)
	fw.mu.Lock()
	defer fw.mu.Unlock()
	_, err := fw.w.Write(buf)
	return err
}

// Reader reads frames. It is not safe for concurrent use.
type Reader struct {
	r   *bufio.Reader
	hdr [5]byte
}

// NewReader returns a Reader on r.
func NewReader(r io.Reader) *Reader { return &Reader{r: bufio.NewReaderSize(r, 64<<10)} }

// ReadFrame reads one frame. The returned payload is a new slice owned by the caller.
// At a clean end of stream it returns io.EOF.
func (fr *Reader) ReadFrame() (typ uint8, payload []byte, err error) {
	if _, err := io.ReadFull(fr.r, fr.hdr[:4]); err != nil {
		return 0, nil, err // io.EOF only if nothing was read
	}
	n := binary.BigEndian.Uint32(fr.hdr[:4])
	if n == 0 {
		return 0, nil, fmt.Errorf("wire: empty frame")
	}
	if n > MaxFrame {
		return 0, nil, ErrTooLarge
	}
	if _, err := io.ReadFull(fr.r, fr.hdr[4:5]); err != nil {
		return 0, nil, unexpected(err)
	}
	payload = make([]byte, n-1)
	if _, err := io.ReadFull(fr.r, payload); err != nil {
		return 0, nil, unexpected(err)
	}
	return fr.hdr[4], payload, nil
}

func unexpected(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}
