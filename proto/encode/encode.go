// Package encode is the client-side screen encoder (PLAN.md 5.3 and 5.4):
// damage detection against what the agent already has, scroll/move
// detection (OpCopy), the tile cache LRU (OpCacheRef / Store), per-tile
// classification (solid, palette+zstd, JPEG), lossless refine of regions that
// stopped changing, the rate controller, and splitting into frame parts.
// It is platform neutral; client-windows feeds it GDI captures.
//
// # How a frame is built
//
// The encoder keeps a shadow copy of the agent's image in the input byte
// order (BGRA when Config.SwapRB is set). For lossy tiles the shadow holds
// the source pixels and the tile is flagged lossy until it has been resent
// losslessly, so damage is always measured against what the agent should
// converge to.
//
//  1. Damage: whole rows of the capture are compared with the shadow first;
//     only differing rows are compared per 64x64 tile.
//  2. Moves: column bands of damaged tiles are searched for a dominant
//     vertical shift (scrolling) using row hashes; the remaining damaged
//     regions are searched for a 2D shift (horizontal scrolling, window
//     drags) by locating sample segments in the shadow. Every hit is
//     verified pixel by pixel and becomes an OpCopy, which is applied to the
//     shadow before damage is computed again, so only the exposed strips and
//     the tiles that really differ are encoded.
//  3. Cache: every damaged tile is hashed (64-bit maphash of its RGBA pixels
//     and size). A hit in the LRU of screen.CacheSlots slots becomes an
//     OpCacheRef; every lossless palette or raw op is stored in the least
//     recently used slot.
//  4. Classification: a tile that changed only in a small area is trimmed to
//     the changed rectangle. Then SolidColor gives OpSolid, EncodePalette
//     gives OpPalette, and anything else is JPEG at the rate controller's
//     quality. JPEG tiles of one frame are merged into larger rectangles to
//     save per-image headers.
//  5. Refine: a lossy tile that stayed unchanged for RefineAfter is resent
//     losslessly (palette, or palette/raw per 32x32 quadrant).
//
// Full frames (the first one and after Reset) are lossless and marked
// Frame.Full, so a Refresh always gives a pixel-exact image. Their cache refs
// only point to slots stored earlier in the same frame, and they carry no
// copies. Input alpha is ignored: every pixel is treated as A = 255.
package encode

import (
	"encoding/binary"
	"hash/maphash"
	"image"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mevdschee/helpbuoy/proto/screen"
)

// TileSize is the edge length of the tiles used for damage, cache and refine.
const TileSize = 64

// Config tunes an Encoder. Zero values pick defaults.
type Config struct {
	// SwapRB says the input images are BGRA (as captured by GDI) instead of
	// RGBA; the encoder swaps R and B only for the pixels it actually sends.
	SwapRB bool
	// Workers encodes tiles in parallel; default runtime.NumCPU().
	Workers int
	// RefineAfter is how long a lossy region must stay unchanged before it is
	// resent losslessly; default 500 ms.
	RefineAfter time.Duration
}

// Encoder turns successive captures of one monitor into frames. Not safe for
// concurrent use.
type Encoder struct {
	cfg  Config
	seed maphash.Seed

	w, h           int
	tilesX, tilesY int
	valid          bool   // shadow holds the agent's image
	shadow         []byte // w*h*4, stride w*4, input byte order, A = 255
	in             []byte // the current capture with A = 255, same layout

	lossy    []bool      // per tile: agent has lossy pixels there
	refineAt []time.Time // per tile, when lossy: when to refine
	dirty    []bool      // scratch: damaged tiles of the current frame
	touched  []bool      // scratch: tiles written by copies
	tilePix  []byte      // scratch: RGBA of each damaged tile, TileSize*TileSize*4 per tile

	cache *lru
	rate  rateCtl
	stats Stats
}

// New returns an Encoder whose first Encode produces a full lossless frame.
func New(cfg Config) *Encoder {
	if cfg.Workers <= 0 {
		cfg.Workers = runtime.NumCPU()
	}
	if cfg.RefineAfter <= 0 {
		cfg.RefineAfter = 500 * time.Millisecond
	}
	e := &Encoder{cfg: cfg, seed: maphash.MakeSeed(), cache: newLRU(), rate: newRateCtl()}
	return e
}

// Encode compares cur with the image the agent has after all frames
// returned so far, and returns the frame parts that bring the agent up to
// date (Seq left 0: the caller numbers frames; all parts of one call share a
// Seq). It returns nil when nothing changed and no refine is due. cur must
// keep the same bounds between calls; call Reset after a size change.
// The encoder keeps its own copy of what the agent has, so the caller may
// reuse cur's buffer for the next capture.
//
// A size change without Reset is handled like a Reset (full frame, empty
// cache), so the agent's Decoder must be reset in that case too.
func (e *Encoder) Encode(cur *image.RGBA, now time.Time) []screen.Frame {
	start := time.Now()
	w, h := cur.Rect.Dx(), cur.Rect.Dy()
	if w <= 0 || h <= 0 {
		return nil
	}
	full := !e.valid || w != e.w || h != e.h
	if full {
		e.setup(w, h)
	}
	src := e.normalize(cur)
	var ops []screen.Rect
	if full {
		ops = e.fullFrame(src, now)
	} else {
		ops = e.update(src, now)
	}
	if len(ops) == 0 {
		e.stats.EncodeTime += time.Since(start)
		return nil
	}
	frames, size := splitParts(ops)
	if full {
		for i := range frames {
			frames[i].Full = true
		}
	}
	if !full {
		// Full frames are one-offs; they would skew the average frame size.
		e.rate.sent(size)
	}
	e.stats.Frames++
	e.stats.Bytes += size
	e.stats.EncodeTime += time.Since(start)
	return frames
}

// setup sizes the encoder for a w x h screen and forgets the agent state.
func (e *Encoder) setup(w, h int) {
	e.w, e.h = w, h
	e.tilesX = (w + TileSize - 1) / TileSize
	e.tilesY = (h + TileSize - 1) / TileSize
	n := e.tilesX * e.tilesY
	if cap(e.shadow) >= w*h*4 {
		e.shadow = e.shadow[:w*h*4]
	} else {
		e.shadow = make([]byte, w*h*4)
	}
	if cap(e.in) >= w*h*4 {
		e.in = e.in[:w*h*4]
	} else {
		e.in = make([]byte, w*h*4)
	}
	e.lossy = make([]bool, n)
	e.refineAt = make([]time.Time, n)
	e.dirty = make([]bool, n)
	e.touched = make([]bool, n)
	if cap(e.tilePix) >= n*TileSize*TileSize*4 {
		e.tilePix = e.tilePix[:n*TileSize*TileSize*4]
	} else {
		e.tilePix = make([]byte, n*TileSize*TileSize*4)
	}
	e.cache.reset()
	e.valid = true
}

// RefineDue reports when Encode should be called again even without new
// damage, so lossy regions get their lossless refine (zero if none pending).
func (e *Encoder) RefineDue() time.Time {
	var due time.Time
	for t, l := range e.lossy {
		if l && (due.IsZero() || e.refineAt[t].Before(due)) {
			due = e.refineAt[t]
		}
	}
	return due
}

// LossyRects returns the tiles where the agent currently has lossy pixels
// that are still waiting for their refine, in shared-monitor pixels. Outside
// them the agent's image equals the last encoded capture exactly.
func (e *Encoder) LossyRects() []image.Rectangle {
	var rs []image.Rectangle
	for t, l := range e.lossy {
		if l {
			rs = append(rs, e.tileRect(t))
		}
	}
	return rs
}

// Reset forgets everything: the next Encode sends a full lossless frame and
// the tile cache starts empty (the agent's Decoder must Reset too). Use it
// for Refresh, a new Hello or a size change.
func (e *Encoder) Reset() {
	e.valid = false
	e.cache.reset()
}

// SetQuality applies the agent's quality preference.
func (e *Encoder) SetQuality(m screen.QualityMode) { e.rate.setMode(m) }

// Acked feeds the rate controller: a frame of size bytes was acked after rtt.
func (e *Encoder) Acked(bytes int, rtt time.Duration) { e.rate.acked(bytes, rtt) }

// FrameInterval is the capture interval that matches the rate controller's
// current target frame rate (1 / Stats().TargetFPS).
func (e *Encoder) FrameInterval() time.Duration {
	return time.Duration(float64(time.Second) / float64(e.rate.targetFPS()))
}

// Stats describes the encoder's recent work, for logs and the benchmark.
type Stats struct {
	Frames, Bytes                                      int
	Raw, Solid, Palette, JPEG, Copy, CacheRef, Refined int // op counts
	JPEGQuality                                        int // current
	TargetFPS                                          int // current, from the rate controller
	EncodeTime                                         time.Duration
}

// Stats returns cumulative counters since New or the last ResetStats.
func (e *Encoder) Stats() Stats {
	s := e.stats
	s.JPEGQuality = e.rate.jpegQuality()
	s.TargetFPS = e.rate.targetFPS()
	return s
}

// ResetStats zeroes the counters.
func (e *Encoder) ResetStats() { e.stats = Stats{} }

// normalize copies cur into e.in with every alpha byte set to 255. GDI
// leaves alpha undefined, so it must neither count as damage nor reach the
// palette classification, the cache hashes or the agent. Working on one
// normalized copy keeps every later stage simple; the copy is about as
// cheap as one read of the capture.
func (e *Encoder) normalize(cur *image.RGBA) pixSource {
	rowBytes := e.w * 4
	base := cur.PixOffset(cur.Rect.Min.X, cur.Rect.Min.Y)
	bands := (e.h + 63) / 64
	e.parallel(bands, func(b int) {
		for y := b * 64; y < min((b+1)*64, e.h); y++ {
			s := cur.Pix[base+y*cur.Stride : base+y*cur.Stride+rowBytes]
			d := e.in[y*rowBytes : (y+1)*rowBytes]
			i := 0
			for ; i+8 <= rowBytes; i += 8 {
				binary.LittleEndian.PutUint64(d[i:], binary.LittleEndian.Uint64(s[i:])|0xff000000ff000000)
			}
			for ; i < rowBytes; i += 4 {
				binary.LittleEndian.PutUint32(d[i:], binary.LittleEndian.Uint32(s[i:])|0xff000000)
			}
		}
	})
	return pixSource{pix: e.in, stride: rowBytes}
}

// pixSource addresses the pixels of a capture: pixel (x, y) starts at
// pix[y*stride+x*4].
type pixSource struct {
	pix    []byte
	stride int
}

func (s pixSource) row(y, x0, x1 int) []byte {
	off := y*s.stride + x0*4
	return s.pix[off : off+(x1-x0)*4]
}

// shadowRow returns pixels x0..x1 of row y of the shadow.
func (e *Encoder) shadowRow(y, x0, x1 int) []byte {
	off := (y*e.w + x0) * 4
	return e.shadow[off : off+(x1-x0)*4]
}

// tileRect returns the pixel rectangle of tile t.
func (e *Encoder) tileRect(t int) image.Rectangle {
	tx, ty := t%e.tilesX, t/e.tilesX
	x0, y0 := tx*TileSize, ty*TileSize
	return image.Rect(x0, y0, min(x0+TileSize, e.w), min(y0+TileSize, e.h))
}

// parallel runs fn(0..n-1) on up to Workers goroutines.
func (e *Encoder) parallel(n int, fn func(i int)) {
	workers := min(e.cfg.Workers, n)
	if workers <= 1 {
		for i := range n {
			fn(i)
		}
		return
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for {
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				fn(i)
			}
		})
	}
	wg.Wait()
}
