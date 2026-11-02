package screen

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// regionOK reports whether src holds a w x h region with the given stride.
func regionOK(src []byte, stride, w, h int) bool {
	if w <= 0 || h <= 0 || w > MaxDimension || h > MaxDimension || stride < w*4 {
		return false
	}
	return len(src) >= (h-1)*stride+w*4
}

// SolidColor reports whether the tile has a single color, and which.
func SolidColor(src []byte, stride, w, h int) (c [4]byte, ok bool) {
	if !regionOK(src, stride, w, h) {
		return c, false
	}
	first := binary.LittleEndian.Uint32(src)
	// Compare 8 bytes at a time against the color repeated twice.
	pair := uint64(first) | uint64(first)<<32
	rowLen := w * 4
	for y := 0; y < h; y++ {
		row := src[y*stride : y*stride+rowLen]
		i := 0
		for ; i+8 <= len(row); i += 8 {
			if binary.LittleEndian.Uint64(row[i:]) != pair {
				return c, false
			}
		}
		if i < len(row) && binary.LittleEndian.Uint32(row[i:]) != first {
			return c, false
		}
	}
	binary.LittleEndian.PutUint32(c[:], first)
	return c, true
}

const (
	maxPalette = 256
	hashBits   = 10 // 1024 entries for at most 256 colors keeps probes short
	hashSize   = 1 << hashBits
)

type paletteEncoder struct {
	keys [hashSize]uint32
	vals [hashSize]uint16 // palette index + 1, 0 = empty
	raw  []byte           // N | palette | indices
	out  []byte
	enc  *zstd.Encoder
}

var paletteEncoders = sync.Pool{New: func() any {
	enc, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedFastest),
		zstd.WithEncoderConcurrency(1),
		zstd.WithEncoderCRC(false),
		zstd.WithLowerEncoderMem(true),
	)
	if err != nil {
		panic(err) // the options are constant, so this cannot happen
	}
	return &paletteEncoder{enc: enc}
}}

// EncodePalette codes a tile with at most 256 colors as described for
// OpPalette: palette indices in row-major order, compressed with zstd at the
// fastest level. It returns ok=false as soon as a 257th color shows up or if
// src does not hold a w x h region.
func EncodePalette(src []byte, stride, w, h int) (data []byte, ok bool) {
	if !regionOK(src, stride, w, h) {
		return nil, false
	}
	pe := paletteEncoders.Get().(*paletteEncoder)
	defer paletteEncoders.Put(pe)
	clear(pe.vals[:])

	head := 2 + maxPalette*4 // indices start after the largest palette, moved down below
	need := head + w*h
	if cap(pe.raw) < need {
		pe.raw = make([]byte, need)
	}
	raw := pe.raw[:need]
	pal := raw[2:head]
	idx := raw[head:]

	n := 0
	last := binary.LittleEndian.Uint32(src) ^ 1 // differs from the first pixel
	var lastIdx byte
	o := 0
	rowLen := w * 4
	for y := 0; y < h; y++ {
		row := src[y*stride : y*stride+rowLen]
		for x := 0; x < len(row); x += 4 {
			c := binary.LittleEndian.Uint32(row[x:])
			if c != last {
				hp := (c * 0x9E3779B1) >> (32 - hashBits)
				for {
					v := pe.vals[hp]
					if v == 0 {
						if n == maxPalette {
							return nil, false
						}
						pe.keys[hp] = c
						pe.vals[hp] = uint16(n + 1)
						binary.LittleEndian.PutUint32(pal[n*4:], c)
						lastIdx = byte(n)
						n++
						break
					}
					if pe.keys[hp] == c {
						lastIdx = byte(v - 1)
						break
					}
					hp = (hp + 1) & (hashSize - 1)
				}
				last = c
			}
			idx[o] = lastIdx
			o++
		}
	}
	// Move the header next to the indices: N | palette | indices.
	start := head - 2 - n*4
	copy(raw[start+2:], pal[:n*4])
	binary.BigEndian.PutUint16(raw[start:], uint16(n))
	pe.out = pe.enc.EncodeAll(raw[start:], pe.out[:0])
	return bytes.Clone(pe.out), true
}

var jpegBuffers = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// EncodeJPEG encodes a tile at quality 1..100 (clamped) as a baseline JPEG
// with 4:2:0 chroma subsampling. Alpha is ignored. It returns nil if src does
// not hold a w x h region.
func EncodeJPEG(src []byte, stride, w, h, quality int) []byte {
	if !regionOK(src, stride, w, h) {
		return nil
	}
	img := &image.RGBA{Pix: src[:(h-1)*stride+w*4], Stride: stride, Rect: image.Rect(0, 0, w, h)}
	buf := jpegBuffers.Get().(*bytes.Buffer)
	defer jpegBuffers.Put(buf)
	buf.Reset()
	if err := jpeg.Encode(buf, img, &jpeg.Options{Quality: clampInt(quality, 1, 100)}); err != nil {
		return nil
	}
	return bytes.Clone(buf.Bytes())
}

// zstdDecoder is shared by all Decoders; DecodeAll is safe for concurrent
// use. The cap limit bounds every output by the buffer it is given.
var zstdDecoder = sync.OnceValue(func() *zstd.Decoder {
	d, err := zstd.NewReader(nil,
		zstd.WithDecoderConcurrency(0),
		zstd.WithDecodeAllCapLimit(true),
		zstd.WithDecoderMaxWindow(8<<20),
		zstd.WithDecoderMaxMemory(64<<20),
	)
	if err != nil {
		panic(err) // the options are constant, so this cannot happen
	}
	return d
})

// paletteBufs holds decompression buffers for decodePalette.
var paletteBufs = sync.Pool{New: func() any { return new([]byte) }}

// decodePalette decodes OpPalette data of a w x h tile into out (w*h*4 bytes,
// tightly packed).
func decodePalette(data []byte, w, h int, out []byte) error {
	maxLen := 2 + maxPalette*4 + w*h
	bp := paletteBufs.Get().(*[]byte)
	defer paletteBufs.Put(bp)
	if cap(*bp) < maxLen {
		*bp = make([]byte, 0, maxLen)
	}
	raw, err := zstdDecoder().DecodeAll(data, (*bp)[:0:maxLen])
	if err != nil {
		return malformed("palette tile: %v", err)
	}
	if len(raw) < 2 {
		return malformed("palette tile: %d bytes", len(raw))
	}
	n := int(binary.BigEndian.Uint16(raw))
	if n < 1 || n > maxPalette || len(raw) != 2+n*4+w*h {
		return malformed("palette tile: %d colors in %d bytes for %dx%d", n, len(raw), w, h)
	}
	var lut [maxPalette]uint32
	for i := 0; i < n; i++ {
		lut[i] = binary.LittleEndian.Uint32(raw[2+i*4:])
	}
	idx := raw[2+n*4:]
	if n < maxPalette {
		var bad byte
		for _, v := range idx {
			if int(v) >= n {
				bad = 1
				break
			}
		}
		if bad != 0 {
			return malformed("palette tile: index out of range")
		}
	}
	out = out[:len(idx)*4]
	for i, v := range idx {
		binary.LittleEndian.PutUint32(out[i*4:], lut[v])
	}
	return nil
}

// decodeJPEG decodes OpJPEG data of exactly w x h pixels into out (w*h*4
// bytes, tightly packed) with A = 255.
func decodeJPEG(data []byte, w, h int, out []byte) error {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return malformed("jpeg tile: %v", err)
	}
	if cfg.Width != w || cfg.Height != h {
		return malformed("jpeg tile of %dx%d for %dx%d", cfg.Width, cfg.Height, w, h)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return malformed("jpeg tile: %v", err)
	}
	if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
		return malformed("jpeg tile of %dx%d for %dx%d", b.Dx(), b.Dy(), w, h)
	}
	switch m := img.(type) {
	case *image.YCbCr:
		ycbcrToRGBA(m, w, h, out)
	case *image.Gray:
		for y := 0; y < h; y++ {
			row := m.Pix[y*m.Stride : y*m.Stride+w]
			o := out[y*w*4 : (y+1)*w*4]
			for x, v := range row {
				o[x*4], o[x*4+1], o[x*4+2], o[x*4+3] = v, v, v, 255
			}
		}
	default:
		b := m.Bounds()
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				c := color.RGBAModel.Convert(m.At(b.Min.X+x, b.Min.Y+y)).(color.RGBA)
				i := (y*w + x) * 4
				out[i], out[i+1], out[i+2], out[i+3] = c.R, c.G, c.B, 255
			}
		}
	}
	return nil
}

func ycbcrToRGBA(m *image.YCbCr, w, h int, out []byte) {
	var hs, vs uint
	switch m.SubsampleRatio {
	case image.YCbCrSubsampleRatio422:
		hs = 1
	case image.YCbCrSubsampleRatio420:
		hs, vs = 1, 1
	case image.YCbCrSubsampleRatio440:
		vs = 1
	case image.YCbCrSubsampleRatio411:
		hs = 2
	case image.YCbCrSubsampleRatio410:
		hs, vs = 2, 1
	}
	// The jpeg decoder returns images whose bounds start at 0,0, so the
	// chroma offsets are simple shifts.
	for y := 0; y < h; y++ {
		yrow := m.Y[y*m.YStride : y*m.YStride+w]
		crow := (y >> vs) * m.CStride
		o := out[y*w*4 : (y+1)*w*4]
		for x, yy := range yrow {
			ci := crow + x>>hs
			r, g, b := color.YCbCrToRGB(yy, m.Cb[ci], m.Cr[ci])
			o[x*4], o[x*4+1], o[x*4+2], o[x*4+3] = r, g, b, 255
		}
	}
}
