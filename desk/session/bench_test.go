package session

import (
	"fmt"
	"image"
	"math/rand/v2"
	"testing"

	"github.com/helpbuoy/helpbuoy/proto/screen"
)

// benchFrame builds a frame of 64x64 tile ops covering share (0..1] of a
// w x h screen, as palette (text-like) or JPEG (photo-like) tiles.
func benchFrame(w, h int, share float64, jpeg bool, seed uint64) screen.Frame {
	rng := rand.New(rand.NewPCG(seed, 1))
	tile := image.NewRGBA(image.Rect(0, 0, 64, 64))
	var f screen.Frame
	for y := 0; y+64 <= h; y += 64 {
		for x := 0; x+64 <= w; x += 64 {
			if rng.Float64() >= share {
				continue
			}
			for i := 0; i < len(tile.Pix); i += 4 {
				px := i / 4
				tx, ty := px%64, px/64
				if jpeg {
					tile.Pix[i] = byte(tx*4 + rng.IntN(16))
					tile.Pix[i+1] = byte(ty*4 + rng.IntN(16))
					tile.Pix[i+2] = byte((tx + ty) * 2)
				} else {
					v := byte(255)
					if (tx/3+ty/5+x)%7 == 0 {
						v = 30
					}
					tile.Pix[i], tile.Pix[i+1], tile.Pix[i+2] = v, v, v
				}
				tile.Pix[i+3] = 255
			}
			r := screen.Rect{X: x, Y: y, W: 64, H: 64}
			if jpeg {
				r.Kind, r.Data = screen.OpJPEG, screen.EncodeJPEG(tile.Pix, tile.Stride, 64, 64, 70)
			} else {
				data, ok := screen.EncodePalette(tile.Pix, tile.Stride, 64, 64)
				if !ok {
					panic("palette tile does not qualify")
				}
				r.Kind, r.Data = screen.OpPalette, data
			}
			f.Rects = append(f.Rects, r)
		}
	}
	return f
}

// BenchmarkDecodeAndPrepareUpload measures the CPU side of one frame on the
// techie with the real screen.Decoder: decoding into the private image,
// publishing into the shared image, planning the upload, and packing the
// rows each texture update needs. Only the GPU calls are left out.
func BenchmarkDecodeAndPrepareUpload(b *testing.B) {
	sizes := []struct{ w, h int }{{1920, 1080}, {2560, 1440}}
	scenes := []struct {
		name  string
		share float64
		jpeg  bool
	}{
		{"typing", 0.004, false},
		{"menus", 0.05, false},
		{"scroll", 0.4, false},
		{"text-full", 1, false},
		{"video-full", 1, true},
	}
	for _, sz := range sizes {
		for _, sc := range scenes {
			b.Run(fmt.Sprintf("%dx%d/%s", sz.w, sz.h, sc.name), func(b *testing.B) {
				s := newSession(Info{}, ProtoCodec{}, nil)
				if err := s.setHello(screen.Hello{Width: sz.w, Height: sz.h}); err != nil {
					b.Fatal(err)
				}
				s.Update(func(*image.RGBA, []image.Rectangle, bool) {})
				frames := make([]screen.Frame, 4)
				pixels := 0
				for i := range frames {
					frames[i] = benchFrame(sz.w, sz.h, sc.share, sc.jpeg, uint64(i))
					if len(frames[i].Rects) == 0 { // typing: at least the caret tile
						frames[i] = screen.Frame{Rects: []screen.Rect{{X: 64 * i, Y: 64, W: 64, H: 64, Kind: screen.OpSolid, Color: [4]byte{0, 0, 0, 255}}}}
					}
					pixels += len(frames[i].Rects) * 64 * 64
				}
				var buf []byte
				uploads := 0
				b.ReportAllocs()
				b.ResetTimer()
				for i := range b.N {
					f := frames[i%len(frames)]
					f.Seq = uint32(i)
					dirty, complete, err := s.dec.Apply(s.back, f)
					if err != nil || !complete {
						b.Fatal(err, complete)
					}
					s.publish(dirty)
					s.Update(func(img *image.RGBA, rs []image.Rectangle, _ bool) {
						for _, r := range rs {
							p := SubImageBytes(img, r, buf)
							if r.Dx() != img.Rect.Dx() && cap(p) > cap(buf) {
								buf = p
							}
							uploads++
						}
					})
				}
				b.ReportMetric(float64(uploads)/float64(b.N), "uploads/frame")
				b.ReportMetric(float64(pixels)/float64(len(frames))/1e6, "Mpx/frame")
			})
		}
	}
}
