// Package screen defines the messages on the inner "screen" channel, the
// tile codecs, the helper-side Decoder and the phase 2 test pattern. Frames
// are wire frames (proto/wire) with a binary payload.
//
// # Encodings
//
// All integers are big-endian. Strings are a uint16 byte length followed by
// UTF-8 bytes, at most MaxString bytes.
//
//	Hello:   Hostname, UserMachine, OS (strings) | uint16 Width | uint16 Height |
//	         uint8 monitor count (at most MaxMonitors) | monitors | uint8 Current
//	  monitor: int32 X | int32 Y | uint16 W | uint16 H | uint8 flags (bit0 = Primary) | Name (string)
//	Frame:   uint32 Seq | uint8 flags (bit0 = More, bit1 = Full) | uint32 op count | ops
//	  op:    uint8 Kind | uint8 opflags (bit0 = Store) | uint16 X | uint16 Y | uint16 W | uint16 H |
//	         [uint16 Slot, if Store] | body
//	  body:  OpRaw      W*H*4 bytes RGBA
//	         OpSolid    4 bytes RGBA
//	         OpPalette  uint32 len | Data
//	         OpJPEG     uint32 len | Data
//	         OpCopy     uint16 SrcX | uint16 SrcY
//	         OpCacheRef uint16 Slot (Store must not be set)
//	Ack:     uint32 Seq
//	Refresh: empty payload
//	Cursor:  uint32 ID | uint16 HotX | uint16 HotY | uint16 W | uint16 H | W*H*4 bytes RGBA
//	CursorPos: int32 X | int32 Y | uint32 ID | uint8 flags (bit0 = Visible)
//	SelectMonitor: uint8 Index
//	Quality: uint8 Mode
//
// Unknown flag bits, kinds and modes are rejected. Rect coordinates are
// pixels relative to the top-left corner of the shared monitor (the image's
// Bounds().Min). See ops.go for the meaning of the ops and the tile cache.
package screen

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"math"
	"unicode/utf8"

	"github.com/mevdschee/help-me-win/proto/wire"
)

// Message type bytes.
const (
	MsgHello         uint8 = 1 // client -> helper
	MsgFrame         uint8 = 2 // client -> helper
	MsgAck           uint8 = 3 // helper -> client
	MsgRefresh       uint8 = 4 // helper -> client: send a full frame (lossless)
	MsgCursor        uint8 = 5 // client -> helper: a cursor shape (CursorShape)
	MsgCursorPos     uint8 = 6 // client -> helper: cursor position and shape id (CursorPos)
	MsgSelectMonitor uint8 = 7 // helper -> client: share another monitor (SelectMonitor)
	MsgQuality       uint8 = 8 // helper -> client: quality preference (Quality)
)

// Limits enforced by the decoders.
const (
	MaxDimension = 16384   // largest accepted width, height or coordinate
	MaxRects     = 1 << 16 // most rects in one frame
	MaxString    = 1024    // longest string in Hello, in bytes
	MaxMonitors  = 16      // most monitors in a Hello
	MaxCursor    = 256     // largest cursor width or height
	MaxSlotSize  = 256     // largest width or height of a tile stored in the cache
)

// ErrMalformed is wrapped by every decode error.
var ErrMalformed = errors.New("screen: malformed message")

// Hello describes the client's screen. It is sent at session start and again
// whenever the shared monitor or its size changes; the helper then resizes its
// image and the next frame is a full frame.
type Hello struct {
	Hostname    string
	UserMachine string
	OS          string
	Width       int // of the shared monitor, in pixels
	Height      int
	Monitors    []Monitor // all monitors, in virtual-desktop pixels (phase 3)
	Current     int       // index into Monitors of the shared one
}

// Monitor is one display of the client, in virtual-desktop pixel coordinates.
type Monitor struct {
	X, Y, W, H int
	Primary    bool
	Name       string
}

// Rect is one operation of a frame, applied in order to the helper's image.
// Kind selects which fields are used; see OpKind. The zero Kind is OpRaw, so
// phase 2 code that only fills RGBA keeps working.
type Rect struct {
	X, Y, W, H int     // destination rectangle in shared-monitor pixels
	Kind       OpKind  // how the pixels are carried
	RGBA       []byte  // OpRaw: W*H*4 bytes, row-major, A = 255
	Color      [4]byte // OpSolid: R, G, B, A (A = 255)
	Data       []byte  // OpPalette, OpJPEG: the encoded tile (formats in ops.go)
	SrcX, SrcY int     // OpCopy: top-left of the source rect in the image as it is when this op runs
	Slot       uint16  // OpCacheRef: cache slot to draw; for other kinds see Store
	Store      bool    // after drawing this op, store its W*H pixels in cache slot Slot (OpRaw, OpSolid, OpPalette, OpJPEG)
}

// Frame is one screen update; it is applied atomically by the helper. A
// frame too large for one wire frame is sent as several parts with the same
// Seq: every part but the last has More set, and the helper applies nothing
// until the last part arrived. Only the last part is acked.
//
// Full marks the first frame after the encoder was reset (session start,
// Refresh, new Hello): it repaints the whole screen and uses no cache slot it
// didn't store itself. After the helper asks for a Refresh it resets its
// decoder and skips (but still acks) frames until one with Full arrives, so
// frames already in flight can't hit the cleared cache.
type Frame struct {
	Seq   uint32
	More  bool // more parts of this frame follow
	Full  bool // full repaint after an encoder reset; set on every part
	Rects []Rect
}

// Ack acknowledges a frame; the client keeps at most 2 frames unacked.
type Ack struct {
	Seq uint32
}

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMalformed, fmt.Sprintf(format, args...))
}

// clampDim limits v to 0..MaxDimension so encoders never produce values the
// decoder would misread.
func clampDim(v int) uint16 { return uint16(clampInt(v, 0, MaxDimension)) }

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampInt32(v int) uint32 { return uint32(int32(clampInt(v, math.MinInt32, math.MaxInt32))) }

// truncString cuts s to at most MaxString bytes on a rune boundary.
func truncString(s string) string {
	if len(s) <= MaxString {
		return s
	}
	s = s[:MaxString]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func appendString(b []byte, s string) []byte {
	s = truncString(s)
	b = binary.BigEndian.AppendUint16(b, uint16(len(s)))
	return append(b, s...)
}

// appendPixels appends exactly n bytes of px, zero-padded or cut.
func appendPixels(b, px []byte, n int) []byte {
	if len(px) >= n {
		return append(b, px[:n]...)
	}
	b = append(b, px...)
	return append(b, make([]byte, n-len(px))...)
}

// reader is a bounds-checked cursor over a payload.
type reader struct {
	b   []byte
	err error
}

func (r *reader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || n > len(r.b) {
		r.err = malformed("short payload")
		return nil
	}
	p := r.b[:n:n]
	r.b = r.b[n:]
	return p
}

func (r *reader) u8() int {
	p := r.take(1)
	if p == nil {
		return 0
	}
	return int(p[0])
}

func (r *reader) u16() int {
	p := r.take(2)
	if p == nil {
		return 0
	}
	return int(binary.BigEndian.Uint16(p))
}

func (r *reader) u32() uint32 {
	p := r.take(4)
	if p == nil {
		return 0
	}
	return binary.BigEndian.Uint32(p)
}

func (r *reader) i32() int { return int(int32(r.u32())) }

func (r *reader) str() string {
	n := r.u16()
	if r.err == nil && n > MaxString {
		r.err = malformed("string too long")
		return ""
	}
	p := r.take(n)
	if r.err == nil && !utf8.Valid(p) {
		r.err = malformed("string is not UTF-8")
		return ""
	}
	return string(p)
}

func (r *reader) end() error {
	if r.err == nil && len(r.b) != 0 {
		r.err = malformed("trailing bytes")
	}
	return r.err
}

// EncodeHello returns the payload of a Hello. Strings longer than MaxString
// are truncated, dimensions are clamped to 0..MaxDimension, monitor
// positions to the int32 range, only the first MaxMonitors monitors are
// sent, and a Current outside Monitors is sent as 0.
func EncodeHello(h Hello) []byte {
	mons := h.Monitors[:min(len(h.Monitors), MaxMonitors)]
	n := 12 + len(h.Hostname) + len(h.UserMachine) + len(h.OS)
	for _, m := range mons {
		n += 15 + len(m.Name)
	}
	b := make([]byte, 0, n)
	b = appendString(b, h.Hostname)
	b = appendString(b, h.UserMachine)
	b = appendString(b, h.OS)
	b = binary.BigEndian.AppendUint16(b, clampDim(h.Width))
	b = binary.BigEndian.AppendUint16(b, clampDim(h.Height))
	b = append(b, byte(len(mons)))
	for _, m := range mons {
		b = binary.BigEndian.AppendUint32(b, clampInt32(m.X))
		b = binary.BigEndian.AppendUint32(b, clampInt32(m.Y))
		b = binary.BigEndian.AppendUint16(b, clampDim(m.W))
		b = binary.BigEndian.AppendUint16(b, clampDim(m.H))
		var flags byte
		if m.Primary {
			flags |= 1
		}
		b = append(b, flags)
		b = appendString(b, m.Name)
	}
	cur := h.Current
	if cur < 0 || cur >= len(mons) {
		cur = 0
	}
	return append(b, byte(cur))
}

// DecodeHello parses a Hello payload. A Hello without monitors has nil
// Monitors and Current 0.
func DecodeHello(b []byte) (Hello, error) {
	r := reader{b: b}
	h := Hello{
		Hostname:    r.str(),
		UserMachine: r.str(),
		OS:          r.str(),
		Width:       r.u16(),
		Height:      r.u16(),
	}
	count := r.u8()
	if r.err != nil {
		return Hello{}, r.err
	}
	if count > MaxMonitors {
		return Hello{}, malformed("%d monitors", count)
	}
	if count > 0 {
		h.Monitors = make([]Monitor, count)
	}
	for i := range h.Monitors {
		m := Monitor{X: r.i32(), Y: r.i32(), W: r.u16(), H: r.u16()}
		flags := r.u8()
		m.Name = r.str()
		if r.err != nil {
			return Hello{}, r.err
		}
		if flags&^1 != 0 {
			return Hello{}, malformed("monitor %d: flags %#x", i, flags)
		}
		if m.W > MaxDimension || m.H > MaxDimension {
			return Hello{}, malformed("monitor %d: size %dx%d too large", i, m.W, m.H)
		}
		m.Primary = flags&1 != 0
		h.Monitors[i] = m
	}
	h.Current = r.u8()
	if err := r.end(); err != nil {
		return Hello{}, err
	}
	if h.Width > MaxDimension || h.Height > MaxDimension {
		return Hello{}, malformed("screen size %dx%d too large", h.Width, h.Height)
	}
	if h.Current != 0 && h.Current >= count {
		return Hello{}, malformed("current monitor %d of %d", h.Current, count)
	}
	return h, nil
}

const (
	frameHeader = 9  // Seq, flags, op count
	opHeader    = 10 // Kind, opflags, X, Y, W, H
	minOp       = opHeader + 2
)

// storable reports whether ops of kind k may carry Store.
func storable(k OpKind) bool { return k <= OpJPEG }

// EncodedSize returns the number of bytes r takes in an encoded Frame. An
// encoder can use it to split a frame into parts of about MaxPart bytes.
func (r Rect) EncodedSize() int {
	n := opHeader
	if r.Store && storable(r.Kind) {
		n += 2
	}
	switch r.Kind {
	case OpRaw:
		n += int(clampDim(r.W)) * int(clampDim(r.H)) * 4
	case OpSolid, OpCopy:
		n += 4
	case OpPalette, OpJPEG:
		n += 4 + len(r.Data)
	case OpCacheRef:
		n += 2
	}
	return n
}

// EncodeFrame returns the payload of a Frame. Each OpRaw rect's pixel data
// is written as exactly W*H*4 bytes, zero-padded or cut if RGBA has another
// length, so the stream stays parseable; callers should not rely on that.
// Coordinates are clamped to 0..MaxDimension, Store is dropped for kinds that
// cannot carry it, and an unknown Kind is written without a body (which
// DecodeFrame rejects). The caller keeps the total under wire.MaxFrame, if
// necessary by splitting the frame into parts (see Frame.More and MaxPart).
func EncodeFrame(f Frame) []byte {
	n := frameHeader
	for _, r := range f.Rects {
		n += r.EncodedSize()
	}
	b := make([]byte, 0, n)
	b = binary.BigEndian.AppendUint32(b, f.Seq)
	var flags byte
	if f.More {
		flags |= 1
	}
	if f.Full {
		flags |= 2
	}
	b = append(b, flags)
	b = binary.BigEndian.AppendUint32(b, uint32(len(f.Rects)))
	for _, r := range f.Rects {
		w, h := clampDim(r.W), clampDim(r.H)
		store := r.Store && storable(r.Kind)
		var opflags byte
		if store {
			opflags |= 1
		}
		b = append(b, byte(r.Kind), opflags)
		b = binary.BigEndian.AppendUint16(b, clampDim(r.X))
		b = binary.BigEndian.AppendUint16(b, clampDim(r.Y))
		b = binary.BigEndian.AppendUint16(b, w)
		b = binary.BigEndian.AppendUint16(b, h)
		if store {
			b = binary.BigEndian.AppendUint16(b, r.Slot)
		}
		switch r.Kind {
		case OpRaw:
			b = appendPixels(b, r.RGBA, int(w)*int(h)*4)
		case OpSolid:
			b = append(b, r.Color[:]...)
		case OpPalette, OpJPEG:
			b = binary.BigEndian.AppendUint32(b, uint32(len(r.Data)))
			b = append(b, r.Data...)
		case OpCopy:
			b = binary.BigEndian.AppendUint16(b, clampDim(r.SrcX))
			b = binary.BigEndian.AppendUint16(b, clampDim(r.SrcY))
		case OpCacheRef:
			b = binary.BigEndian.AppendUint16(b, r.Slot)
		}
	}
	return b
}

// DecodeFrame parses a Frame payload. The pixel and data slices of the
// returned rects alias b, so b must not be modified while the frame is in
// use (payloads from wire.Reader.ReadFrame are owned by the caller, so this
// costs no copy).
//
// Every rect must be non-empty and lie within MaxDimension in both axes, as
// must the source of an OpCopy, and slots must be below CacheSlots; the
// position within the actual screen and the tile payloads are checked by
// Apply and Decoder.Apply.
func DecodeFrame(b []byte) (Frame, error) {
	if len(b) > wire.MaxFrame {
		return Frame{}, malformed("frame too large")
	}
	r := reader{b: b}
	f := Frame{Seq: r.u32()}
	flags := r.u8()
	count := r.u32()
	if r.err != nil {
		return Frame{}, r.err
	}
	if flags&^3 != 0 {
		return Frame{}, malformed("frame flags %#x", flags)
	}
	f.More = flags&1 != 0
	f.Full = flags&2 != 0
	if count > MaxRects {
		return Frame{}, malformed("%d rects", count)
	}
	// Every op needs at least minOp bytes, which bounds the allocation by
	// the payload size.
	if uint64(count)*minOp > uint64(len(r.b)) {
		return Frame{}, malformed("short payload for %d rects", count)
	}
	if count > 0 {
		f.Rects = make([]Rect, count)
	}
	for i := range f.Rects {
		kind := OpKind(r.u8())
		opflags := r.u8()
		x, y, w, h := r.u16(), r.u16(), r.u16(), r.u16()
		if r.err != nil {
			return Frame{}, r.err
		}
		if kind > OpCacheRef {
			return Frame{}, malformed("rect %d: kind %d", i, kind)
		}
		if opflags&^1 != 0 {
			return Frame{}, malformed("rect %d: flags %#x", i, opflags)
		}
		if w == 0 || h == 0 || x+w > MaxDimension || y+h > MaxDimension {
			return Frame{}, malformed("rect %d: %dx%d at %d,%d", i, w, h, x, y)
		}
		rc := Rect{X: x, Y: y, W: w, H: h, Kind: kind, Store: opflags&1 != 0}
		if rc.Store {
			if !storable(kind) {
				return Frame{}, malformed("rect %d: store on kind %d", i, kind)
			}
			rc.Slot = uint16(r.u16())
		}
		switch kind {
		case OpRaw:
			rc.RGBA = r.take(w * h * 4)
		case OpSolid:
			copy(rc.Color[:], r.take(4))
		case OpPalette, OpJPEG:
			n := r.u32()
			if uint64(n) > uint64(len(r.b)) {
				return Frame{}, malformed("rect %d: %d bytes of data", i, n)
			}
			rc.Data = r.take(int(n))
		case OpCopy:
			rc.SrcX, rc.SrcY = r.u16(), r.u16()
			if rc.SrcX+w > MaxDimension || rc.SrcY+h > MaxDimension {
				return Frame{}, malformed("rect %d: copy source %d,%d", i, rc.SrcX, rc.SrcY)
			}
		case OpCacheRef:
			rc.Slot = uint16(r.u16())
		}
		if r.err != nil {
			return Frame{}, r.err
		}
		if (rc.Store || kind == OpCacheRef) && rc.Slot >= CacheSlots {
			return Frame{}, malformed("rect %d: slot %d", i, rc.Slot)
		}
		f.Rects[i] = rc
	}
	if err := r.end(); err != nil {
		return Frame{}, err
	}
	return f, nil
}

// EncodeAck returns the payload of an Ack.
func EncodeAck(a Ack) []byte { return binary.BigEndian.AppendUint32(nil, a.Seq) }

// DecodeAck parses an Ack payload.
func DecodeAck(b []byte) (Ack, error) {
	if len(b) != 4 {
		return Ack{}, malformed("ack of %d bytes", len(b))
	}
	return Ack{Seq: binary.BigEndian.Uint32(b)}, nil
}

// inBounds reports whether the w x h rect at x,y lies within a bw x bh image.
func inBounds(x, y, w, h, bw, bh int) bool {
	return x >= 0 && y >= 0 && w > 0 && h > 0 && w <= bw && h <= bh && x <= bw-w && y <= bh-h
}

// Apply writes the rects of a phase 2 frame, which only has OpRaw rects,
// into dst; Store flags are ignored. Use a Decoder for all other kinds. All
// rects are checked first, so on error dst is left unchanged.
func Apply(dst *image.RGBA, f Frame) error {
	if dst == nil {
		return errors.New("screen: nil image")
	}
	bw, bh := dst.Rect.Dx(), dst.Rect.Dy()
	for i, r := range f.Rects {
		if r.Kind != OpRaw {
			return fmt.Errorf("screen: rect %d has kind %d, Apply only takes raw rects", i, r.Kind)
		}
		if !inBounds(r.X, r.Y, r.W, r.H, bw, bh) {
			return fmt.Errorf("screen: rect %d (%dx%d at %d,%d) outside %dx%d screen", i, r.W, r.H, r.X, r.Y, bw, bh)
		}
		if len(r.RGBA) != r.W*r.H*4 {
			return fmt.Errorf("screen: rect %d has %d pixel bytes, want %d", i, len(r.RGBA), r.W*r.H*4)
		}
	}
	for _, r := range f.Rects {
		blit(dst, r.X, r.Y, r.W, r.H, r.RGBA)
	}
	return nil
}

// blit copies the w x h tightly packed pixels px into dst at x,y (relative to
// dst.Rect.Min). The rect must be in bounds.
func blit(dst *image.RGBA, x, y, w, h int, px []byte) {
	rowLen := w * 4
	off := y*dst.Stride + x*4
	for row := 0; row < h; row++ {
		copy(dst.Pix[off:off+rowLen], px[row*rowLen:(row+1)*rowLen])
		off += dst.Stride
	}
}
