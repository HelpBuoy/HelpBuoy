// Package refdec is a small, sequential reference implementation of the
// techie side of the frame format (proto/screen/ops.go), written straight
// from the documented semantics. The encoder tests and helpbuoy-bench use it to
// check the encoder independently of screen.Decoder. It is not optimized.
package refdec

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/jpeg"

	"github.com/klauspost/compress/zstd"

	"github.com/helpbuoy/helpbuoy/proto/screen"
)

type slot struct {
	w, h int
	pix  []byte
}

// Decoder applies frames to an image and keeps the tile cache.
type Decoder struct {
	slots   [screen.CacheSlots]*slot
	pending []screen.Rect
	zd      *zstd.Decoder
	// Stored counts the ops that stored a slot; Refs the cache refs.
	Stored, Refs int
}

// New returns a decoder with an empty cache.
func New() *Decoder {
	zd, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		panic(err)
	}
	return &Decoder{zd: zd}
}

// Reset clears the cache and buffered parts.
func (d *Decoder) Reset() {
	d.slots = [screen.CacheSlots]*slot{}
	d.pending = nil
}

// Apply applies one frame part; it returns true when a whole frame was
// applied.
func (d *Decoder) Apply(dst *image.RGBA, f screen.Frame) (bool, error) {
	d.pending = append(d.pending, f.Rects...)
	if f.More {
		return false, nil
	}
	ops := d.pending
	d.pending = nil
	for i, r := range ops {
		if err := d.op(dst, r); err != nil {
			return true, fmt.Errorf("op %d (kind %d, %dx%d at %d,%d): %w", i, r.Kind, r.W, r.H, r.X, r.Y, err)
		}
	}
	return true, nil
}

func (d *Decoder) op(dst *image.RGBA, r screen.Rect) error {
	b := dst.Rect
	if r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > b.Dx() || r.Y+r.H > b.Dy() {
		return fmt.Errorf("outside the %dx%d screen", b.Dx(), b.Dy())
	}
	var pix []byte // W*H*4 pixels to draw, unless the op draws itself
	switch r.Kind {
	case screen.OpRaw:
		if len(r.RGBA) != r.W*r.H*4 {
			return fmt.Errorf("raw has %d bytes", len(r.RGBA))
		}
		pix = r.RGBA
	case screen.OpSolid:
		pix = make([]byte, r.W*r.H*4)
		for i := 0; i < len(pix); i += 4 {
			copy(pix[i:], r.Color[:])
		}
	case screen.OpPalette:
		raw, err := d.zd.DecodeAll(r.Data, nil)
		if err != nil {
			return err
		}
		if len(raw) < 2 {
			return fmt.Errorf("short palette")
		}
		n := int(binary.BigEndian.Uint16(raw))
		if n < 1 || n > 256 || len(raw) != 2+n*4+r.W*r.H {
			return fmt.Errorf("palette of %d colors in %d bytes", n, len(raw))
		}
		pal, idx := raw[2:2+n*4], raw[2+n*4:]
		pix = make([]byte, r.W*r.H*4)
		for i, c := range idx {
			if int(c) >= n {
				return fmt.Errorf("index %d of %d colors", c, n)
			}
			copy(pix[i*4:i*4+4], pal[int(c)*4:])
		}
	case screen.OpJPEG:
		img, err := jpeg.Decode(bytes.NewReader(r.Data))
		if err != nil {
			return err
		}
		if img.Bounds().Dx() != r.W || img.Bounds().Dy() != r.H {
			return fmt.Errorf("jpeg of %v", img.Bounds())
		}
		pix = make([]byte, r.W*r.H*4)
		ib := img.Bounds()
		for y := range r.H {
			for x := range r.W {
				cr, cg, cb, _ := img.At(ib.Min.X+x, ib.Min.Y+y).RGBA()
				o := (y*r.W + x) * 4
				pix[o], pix[o+1], pix[o+2], pix[o+3] = uint8(cr>>8), uint8(cg>>8), uint8(cb>>8), 255
			}
		}
	case screen.OpCopy:
		if r.SrcX < 0 || r.SrcY < 0 || r.SrcX+r.W > b.Dx() || r.SrcY+r.H > b.Dy() {
			return fmt.Errorf("copy source outside the screen")
		}
		tmp := make([]byte, r.W*r.H*4)
		read(dst, r.SrcX, r.SrcY, r.W, r.H, tmp)
		write(dst, r.X, r.Y, r.W, r.H, tmp)
		return nil
	case screen.OpCacheRef:
		s := d.slots[r.Slot]
		if s == nil {
			return fmt.Errorf("empty slot %d", r.Slot)
		}
		if s.w != r.W || s.h != r.H {
			return fmt.Errorf("slot %d holds %dx%d", r.Slot, s.w, s.h)
		}
		d.Refs++
		write(dst, r.X, r.Y, r.W, r.H, s.pix)
		return nil
	default:
		return fmt.Errorf("unknown kind")
	}
	write(dst, r.X, r.Y, r.W, r.H, pix)
	if r.Store {
		d.Stored++
		d.slots[r.Slot] = &slot{w: r.W, h: r.H, pix: bytes.Clone(pix)}
	}
	return nil
}

func read(img *image.RGBA, x, y, w, h int, out []byte) {
	for row := range h {
		off := img.PixOffset(img.Rect.Min.X+x, img.Rect.Min.Y+y+row)
		copy(out[row*w*4:(row+1)*w*4], img.Pix[off:off+w*4])
	}
}

func write(img *image.RGBA, x, y, w, h int, pix []byte) {
	for row := range h {
		off := img.PixOffset(img.Rect.Min.X+x, img.Rect.Min.Y+y+row)
		copy(img.Pix[off:off+w*4], pix[row*w*4:(row+1)*w*4])
	}
}
