package screen

// This file is the phase 3/4 contract for encoded frames, the tile cache,
// the cursor and the techie-side decoder. The tile codecs are in tile.go and
// the Decoder in decoder.go.
//
// # Rect operations (OpKind)
//
// Ops of a frame are applied strictly in order to the techie's image:
//
//	OpRaw      RGBA holds W*H*4 bytes.
//	OpSolid    fill with Color.
//	OpPalette  Data = zstd frame (klauspost/compress/zstd) of:
//	             uint16 N (1..256) | N*4 bytes palette RGBA | W*H bytes indices (row-major)
//	           Lossless; used for text, UI and code.
//	OpJPEG     Data = baseline JPEG of exactly W x H pixels (image/jpeg compatible);
//	           decoded pixels get A = 255. Lossy.
//	OpCopy     copy the W x H rect at SrcX,SrcY of the current image to X,Y;
//	           overlapping source and destination behave like memmove (ScrBlt).
//	OpCacheRef draw cache slot Slot at X,Y; the slot's stored size must equal W x H.
//
// # Tile cache
//
// The techie keeps CacheSlots slots of pixels. It never decides anything:
// the computer's encoder owns the LRU and tells the techie with Store/Slot which
// slot to (over)write and which to draw. So both sides can't disagree as long
// as frames are applied in order, which the wire guarantees. A Refresh or a
// new Hello clears the cache on both sides.
//
// # Wire format of a Frame (version 2)
//
//	uint32 Seq | uint8 flags (bit0 = More, bit1 = Full) | uint32 op count | ops
//	op: uint8 Kind | uint8 opflags (bit0 = Store) | uint16 X | uint16 Y | uint16 W | uint16 H | body
//	  OpRaw:      W*H*4 bytes
//	  OpSolid:    4 bytes RGBA
//	  OpPalette:  uint16 Slot (if Store) | uint32 len | Data
//	  OpJPEG:     uint16 Slot (if Store) | uint32 len | Data
//	  OpCopy:     uint16 SrcX | uint16 SrcY
//	  OpCacheRef: uint16 Slot
//	  OpRaw and OpSolid with Store also carry uint16 Slot right after the header.
//
// # Cursor
//
// The captured image never contains the cursor. CursorShape messages carry a
// shape once per ID; CursorPos says where the cursor is (shared-monitor
// pixels, may be outside it) and which shape it has. The techie draws it
// locally, except while its own pointer is over the view and controlling.

import "encoding/binary"

// OpKind says how a Rect carries its pixels.
type OpKind uint8

const (
	OpRaw      OpKind = 0
	OpSolid    OpKind = 1
	OpPalette  OpKind = 2
	OpJPEG     OpKind = 3
	OpCopy     OpKind = 4
	OpCacheRef OpKind = 5
)

// CacheSlots is the number of tile cache slots (64x64 RGBA tiles are 16 KiB,
// so 8192 slots are at most 128 MiB on the techie).
const CacheSlots = 8192

// MaxPart is the payload size above which an encoder splits a frame into parts.
const MaxPart = 8 << 20

// CursorShape is one cursor image. ID is chosen by the computer (e.g. a hash
// of the HCURSOR plus its bitmap); the techie caches shapes by ID.
type CursorShape struct {
	ID         uint32
	HotX, HotY int
	W, H       int    // at most 256 x 256
	RGBA       []byte // W*H*4, straight (non-premultiplied) alpha
}

// CursorPos is the cursor position in shared-monitor pixels. Visible false
// hides it (e.g. while a game hides the cursor or it is on another monitor).
type CursorPos struct {
	X, Y    int // may be negative or beyond the monitor
	ID      uint32
	Visible bool
}

// SelectMonitor asks the computer to share Monitors[Index] from Hello.
type SelectMonitor struct {
	Index int
}

// QualityMode is the techie's preference for lossy coding.
type QualityMode uint8

const (
	QualityAuto  QualityMode = 0 // rate controller decides
	QualitySharp QualityMode = 1 // prefer lossless, accept lower fps
	QualityFast  QualityMode = 2 // prefer fps, lower JPEG quality
)

// Quality is sent by the techie when the user changes the mode.
type Quality struct {
	Mode QualityMode
}

// EncodeCursorShape returns the payload of a CursorShape. W and H are
// clamped to 0..MaxCursor and the hotspot into the shape (0 for an empty
// one); RGBA is zero-padded or cut to W*H*4 bytes.
func EncodeCursorShape(c CursorShape) []byte {
	w, h := clampInt(c.W, 0, MaxCursor), clampInt(c.H, 0, MaxCursor)
	hx, hy := clampInt(c.HotX, 0, max(w-1, 0)), clampInt(c.HotY, 0, max(h-1, 0))
	b := make([]byte, 0, 12+w*h*4)
	b = binary.BigEndian.AppendUint32(b, c.ID)
	b = binary.BigEndian.AppendUint16(b, uint16(hx))
	b = binary.BigEndian.AppendUint16(b, uint16(hy))
	b = binary.BigEndian.AppendUint16(b, uint16(w))
	b = binary.BigEndian.AppendUint16(b, uint16(h))
	return appendPixels(b, c.RGBA, w*h*4)
}

// DecodeCursorShape parses a CursorShape payload. RGBA aliases b. The shape
// is at most MaxCursor x MaxCursor (it may be empty) and the hotspot lies
// within it.
func DecodeCursorShape(b []byte) (CursorShape, error) {
	r := reader{b: b}
	c := CursorShape{ID: r.u32(), HotX: r.u16(), HotY: r.u16(), W: r.u16(), H: r.u16()}
	if r.err != nil {
		return CursorShape{}, r.err
	}
	if c.W > MaxCursor || c.H > MaxCursor {
		return CursorShape{}, malformed("cursor of %dx%d", c.W, c.H)
	}
	if c.HotX > max(c.W-1, 0) || c.HotY > max(c.H-1, 0) {
		return CursorShape{}, malformed("cursor hotspot %d,%d outside %dx%d", c.HotX, c.HotY, c.W, c.H)
	}
	c.RGBA = r.take(c.W * c.H * 4)
	if err := r.end(); err != nil {
		return CursorShape{}, err
	}
	return c, nil
}

// EncodeCursorPos returns the payload of a CursorPos; X and Y are clamped to
// the int32 range.
func EncodeCursorPos(c CursorPos) []byte {
	b := make([]byte, 0, 13)
	b = binary.BigEndian.AppendUint32(b, clampInt32(c.X))
	b = binary.BigEndian.AppendUint32(b, clampInt32(c.Y))
	b = binary.BigEndian.AppendUint32(b, c.ID)
	var flags byte
	if c.Visible {
		flags |= 1
	}
	return append(b, flags)
}

// DecodeCursorPos parses a CursorPos payload.
func DecodeCursorPos(b []byte) (CursorPos, error) {
	r := reader{b: b}
	c := CursorPos{X: r.i32(), Y: r.i32(), ID: r.u32()}
	flags := r.u8()
	if err := r.end(); err != nil {
		return CursorPos{}, err
	}
	if flags&^1 != 0 {
		return CursorPos{}, malformed("cursor flags %#x", flags)
	}
	c.Visible = flags&1 != 0
	return c, nil
}

// EncodeSelectMonitor returns the payload of a SelectMonitor; Index is
// clamped to 0..MaxMonitors-1.
func EncodeSelectMonitor(s SelectMonitor) []byte {
	return []byte{byte(clampInt(s.Index, 0, MaxMonitors-1))}
}

// DecodeSelectMonitor parses a SelectMonitor payload. Whether Index exists
// is up to the computer, which knows its monitors.
func DecodeSelectMonitor(b []byte) (SelectMonitor, error) {
	if len(b) != 1 {
		return SelectMonitor{}, malformed("select monitor of %d bytes", len(b))
	}
	if int(b[0]) >= MaxMonitors {
		return SelectMonitor{}, malformed("monitor %d", b[0])
	}
	return SelectMonitor{Index: int(b[0])}, nil
}

// EncodeQuality returns the payload of a Quality.
func EncodeQuality(q Quality) []byte { return []byte{byte(q.Mode)} }

// DecodeQuality parses a Quality payload.
func DecodeQuality(b []byte) (Quality, error) {
	if len(b) != 1 {
		return Quality{}, malformed("quality of %d bytes", len(b))
	}
	if QualityMode(b[0]) > QualityFast {
		return Quality{}, malformed("quality mode %d", b[0])
	}
	return Quality{Mode: QualityMode(b[0])}, nil
}
