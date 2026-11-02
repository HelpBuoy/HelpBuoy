package encode

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"io/fs"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/mevdschee/help-me-win/proto/encode/internal/refdec"
	"github.com/mevdschee/help-me-win/proto/encode/scenes"
	"github.com/mevdschee/help-me-win/proto/screen"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// realCodecs reports whether proto/screen's tile codecs are implemented
// (they were contract stubs while this package was written).
func realCodecs() bool {
	px := make([]byte, 8*8*4)
	for i := range px {
		px[i] = byte(i * 37)
	}
	return len(screen.EncodeJPEG(px, 32, 8, 8, 80)) > 0
}

// realDecoder reports whether screen.Decoder is implemented.
func realDecoder() bool {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	_, _, err := screen.NewDecoder().Apply(img, screen.Frame{Rects: []screen.Rect{{Kind: screen.OpSolid, W: 4, H: 4, Color: [4]byte{1, 2, 3, 255}}}})
	return err == nil || !strings.Contains(err.Error(), "not implemented")
}

// pipe runs an encoder against the reference decoder and, when it is
// implemented, screen.Decoder, checking the result after every frame.
type pipe struct {
	t        *testing.T
	enc      *Encoder
	ref      *refdec.Decoder
	dec      *screen.Decoder
	refImg   *image.RGBA
	decImg   *image.RGBA
	swap     bool
	seq      uint32
	lastOps  []screen.Rect
	maxDelta int  // largest per-channel error seen in lossy tiles
	wantFull bool // the next frame must be marked Full
}

func newPipe(t *testing.T, cfg Config, w, h int) *pipe {
	p := &pipe{t: t, enc: New(cfg), ref: refdec.New(), swap: cfg.SwapRB, wantFull: true,
		refImg: image.NewRGBA(image.Rect(0, 0, w, h))}
	if realDecoder() {
		p.dec = screen.NewDecoder()
		p.decImg = image.NewRGBA(image.Rect(0, 0, w, h))
	}
	return p
}

// step encodes cur, applies the parts and checks that the helper's image
// equals cur outside the lossy tiles. It returns the parts.
func (p *pipe) step(cur *image.RGBA, now time.Time) []screen.Frame {
	p.t.Helper()
	parts := p.enc.Encode(cur, now)
	p.apply(parts)
	if len(parts) > 0 {
		p.wantFull = false
	}
	p.check(cur)
	return parts
}

func (p *pipe) apply(parts []screen.Frame) {
	p.t.Helper()
	p.seq++
	p.lastOps = nil
	for i, f := range parts {
		if f.More != (i < len(parts)-1) {
			p.t.Fatalf("part %d of %d: More = %v", i, len(parts), f.More)
		}
		if f.Full != p.wantFull {
			p.t.Fatalf("part %d: Full = %v, want %v", i, f.Full, p.wantFull)
		}
		f.Seq = p.seq
		p.lastOps = append(p.lastOps, f.Rects...)
		done, err := p.ref.Apply(p.refImg, f)
		if err != nil {
			p.t.Fatalf("reference decoder: %v", err)
		}
		if done != !f.More {
			p.t.Fatalf("reference decoder: done = %v for More = %v", done, f.More)
		}
		if p.dec != nil {
			_, complete, err := p.dec.Apply(p.decImg, f)
			if err != nil {
				p.t.Fatalf("screen.Decoder: %v", err)
			}
			if complete != !f.More {
				p.t.Fatalf("screen.Decoder: complete = %v for More = %v", complete, f.More)
			}
		}
	}
}

func (p *pipe) reset() {
	p.wantFull = true
	p.enc.Reset()
	p.ref.Reset()
	if p.dec != nil {
		p.dec.Reset()
	}
}

// check compares the decoded images with cur: exact outside the encoder's
// lossy tiles, within a JPEG tolerance inside them.
func (p *pipe) check(cur *image.RGBA) {
	p.t.Helper()
	want := opaque(cur)
	if p.swap {
		want = swapped(want)
	}
	lossy := map[image.Point]bool{}
	for _, r := range p.enc.LossyRects() {
		lossy[r.Min.Div(TileSize)] = true
	}
	imgs := []*image.RGBA{p.refImg}
	if p.dec != nil {
		imgs = append(imgs, p.decImg)
	}
	for k, got := range imgs {
		name := [...]string{"reference decoder", "screen.Decoder"}[k]
		w, h := want.Rect.Dx(), want.Rect.Dy()
		for y := range h {
			a := want.Pix[want.PixOffset(want.Rect.Min.X, want.Rect.Min.Y+y):][:w*4]
			b := got.Pix[y*got.Stride:][:w*4]
			if bytes.Equal(a, b) {
				continue
			}
			for x := range w {
				for c := range 4 {
					d := int(a[x*4+c]) - int(b[x*4+c])
					if d == 0 {
						continue
					}
					if !lossy[image.Pt(x/TileSize, y/TileSize)] {
						p.t.Fatalf("%s: pixel %d,%d is %v, want %v (not lossy)", name, x, y, b[x*4:x*4+4], a[x*4:x*4+4])
					}
					p.maxDelta = max(p.maxDelta, abs(d))
				}
			}
		}
	}
}

// opaque returns a copy of img with A = 255, as the encoder sends it.
func opaque(img *image.RGBA) *image.RGBA {
	out := clone(img)
	for y := range img.Rect.Dy() {
		row := out.Pix[out.PixOffset(out.Rect.Min.X, out.Rect.Min.Y+y):][:img.Rect.Dx()*4]
		for x := 3; x < len(row); x += 4 {
			row[x] = 255
		}
	}
	return out
}

func swapped(img *image.RGBA) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, img.Rect.Dx(), img.Rect.Dy()))
	for y := range img.Rect.Dy() {
		s := img.Pix[img.PixOffset(img.Rect.Min.X, img.Rect.Min.Y+y):]
		d := out.Pix[y*out.Stride:]
		for x := range img.Rect.Dx() {
			o := x * 4
			d[o], d[o+1], d[o+2], d[o+3] = s[o+2], s[o+1], s[o], s[o+3]
		}
	}
	return out
}

// uiImage draws few-colored, text-like content: every row is likely
// unique, every tile has few colors.
func uiImage(rng *rand.Rand, w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	uiFill(rng, img, img.Rect)
	return img
}

func uiFill(rng *rand.Rand, img *image.RGBA, r image.Rectangle) {
	bg := [4]byte{250, 250, 250, 255}
	fg := [][4]byte{{20, 20, 20, 255}, {0, 0, 180, 255}, {160, 20, 20, 255}}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := bg
			if rng.IntN(3) == 0 {
				c = fg[(x/40+y/50)%3]
			}
			copy(img.Pix[img.PixOffset(x, y):], c[:])
		}
	}
}

func noiseImage(rng *rand.Rand, w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = byte(rng.Uint32())
		if i%4 == 3 {
			img.Pix[i] = 255
		}
	}
	return img
}

// smoothImage is a many-colored but JPEG friendly image.
func smoothImage(w, h, phase int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			o := img.PixOffset(x, y)
			img.Pix[o] = uint8(128 + 100*math.Sin(float64(x+phase)/23))
			img.Pix[o+1] = uint8(128 + 100*math.Cos(float64(y-phase)/31))
			img.Pix[o+2] = uint8(128 + 100*math.Sin(float64(x+y+phase)/41))
			img.Pix[o+3] = 255
		}
	}
	return img
}

func clone(img *image.RGBA) *image.RGBA {
	c := *img
	c.Pix = bytes.Clone(img.Pix)
	return &c
}

func countKinds(ops []screen.Rect) map[screen.OpKind]int {
	m := map[screen.OpKind]int{}
	for _, r := range ops {
		m[r.Kind]++
	}
	return m
}

func TestDamageExact(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 1))
	const w, h = 300, 200 // not multiples of the tile size
	img := uiImage(rng, w, h)
	e := New(Config{Workers: 3})
	if e.Encode(img, t0) == nil {
		t.Fatal("first frame is empty")
	}
	src := pixSource{pix: img.Pix, stride: img.Stride}
	for iter := range 200 {
		prev := clone(img)
		want := map[int]bool{}
		for range rng.IntN(6) {
			x, y := rng.IntN(w), rng.IntN(h)
			img.Pix[img.PixOffset(x, y)+rng.IntN(3)] ^= byte(1 + rng.IntN(255))
			want[(y/TileSize)*e.tilesX+x/TileSize] = true
		}
		n := e.damage(src)
		if n != len(want) {
			t.Fatalf("iteration %d: %d dirty tiles, want %d", iter, n, len(want))
		}
		for tile, d := range e.dirty {
			if d != want[tile] {
				t.Fatalf("iteration %d: tile %d dirty = %v", iter, tile, d)
			}
			if d {
				b := e.diffBounds(src, tile)
				for y := range h {
					for x := range w {
						in := image.Pt(x, y).In(b)
						if !in && image.Pt(x, y).In(e.tileRect(tile)) &&
							!bytes.Equal(img.Pix[img.PixOffset(x, y):][:4], prev.Pix[prev.PixOffset(x, y):][:4]) {
							t.Fatalf("iteration %d: changed pixel %d,%d outside bounds %v", iter, x, y, b)
						}
					}
				}
			}
		}
		e.storeShadow(src, image.Rect(0, 0, w, h))
	}
}

func TestUnchangedIsNil(t *testing.T) {
	img := uiImage(rand.New(rand.NewPCG(2, 2)), 200, 100)
	p := newPipe(t, Config{}, 200, 100)
	if p.step(img, t0) == nil {
		t.Fatal("first frame is empty")
	}
	if parts := p.enc.Encode(img, t0.Add(time.Second)); parts != nil {
		t.Fatalf("unchanged screen gave %d parts", len(parts))
	}
	if !p.enc.RefineDue().IsZero() {
		t.Fatal("refine due on a lossless screen")
	}
}

// opArea sums the area of the ops other than copies.
func opArea(ops []screen.Rect) (area int, bounds image.Rectangle) {
	for _, r := range ops {
		if r.Kind == screen.OpCopy {
			continue
		}
		area += r.W * r.H
		bounds = bounds.Union(image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H))
	}
	return
}

func TestVerticalScroll(t *testing.T) {
	for _, dy := range []int{-37, -64, -1, 5, 100} {
		t.Run(fmt.Sprint(dy), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(3, uint64(dy+1000)))
			const w, h = 640, 480
			doc := uiImage(rng, w, h+200)
			view := func(off int) *image.RGBA {
				return doc.SubImage(image.Rect(0, off, w, off+h)).(*image.RGBA)
			}
			p := newPipe(t, Config{}, w, h)
			p.step(view(100), t0)
			// Content moves by dy: the view's offset moves by -dy.
			p.step(view(100-dy), t0.Add(time.Second/60))
			copies := 0
			for _, r := range p.lastOps {
				if r.Kind == screen.OpCopy {
					copies++
					if r.Y-r.SrcY != dy || r.X != r.SrcX {
						t.Errorf("copy %dx%d from %d,%d to %d,%d, want shift 0,%d", r.W, r.H, r.SrcX, r.SrcY, r.X, r.Y, dy)
					}
				}
			}
			if copies == 0 {
				t.Fatalf("no copy for a scroll by %d: %v", dy, countKinds(p.lastOps))
			}
			area, b := opArea(p.lastOps)
			strip := image.Rect(0, h+dy, w, h)
			if dy > 0 {
				strip = image.Rect(0, 0, w, dy)
			}
			if area > strip.Dx()*strip.Dy() || !b.In(strip) {
				t.Fatalf("encoded %d pixels within %v, want only the exposed strip %v", area, b, strip)
			}
		})
	}
}

func TestHorizontalScrollAndDrag(t *testing.T) {
	rng := rand.New(rand.NewPCG(4, 4))
	const w, h = 800, 600
	for _, d := range []image.Point{{-50, 0}, {31, 0}, {13, 7}, {-120, 45}} {
		t.Run(fmt.Sprint(d), func(t *testing.T) {
			bg := uiImage(rng, w, h)
			win := uiImage(rng, 300, 200)
			frame := func(pos image.Point) *image.RGBA {
				img := clone(bg)
				for y := range 200 {
					copy(img.Pix[img.PixOffset(pos.X, pos.Y+y):][:300*4], win.Pix[y*win.Stride:][:300*4])
				}
				return img
			}
			p := newPipe(t, Config{}, w, h)
			start := image.Pt(250, 200)
			p.step(frame(start), t0)
			p.step(frame(start.Add(d)), t0.Add(time.Second/60))
			var moved int
			for _, r := range p.lastOps {
				if r.Kind == screen.OpCopy {
					if r.X-r.SrcX != d.X || r.Y-r.SrcY != d.Y {
						t.Errorf("copy shift %d,%d, want %v", r.X-r.SrcX, r.Y-r.SrcY, d)
					}
					moved += r.W * r.H
				}
			}
			if moved < 300*200*9/10 {
				t.Fatalf("copies cover %d pixels of the 60000 moved: %v", moved, countKinds(p.lastOps))
			}
		})
	}
}

func TestCacheHitsAfterSwitchingBack(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 5))
	const w, h = 512, 384
	a, b := uiImage(rng, w, h), uiImage(rng, w, h)
	p := newPipe(t, Config{}, w, h)
	p.step(a, t0)
	p.step(b, t0.Add(time.Second))
	p.step(a, t0.Add(2*time.Second))
	k := countKinds(p.lastOps)
	if k[screen.OpCacheRef] != (w/TileSize)*(h/TileSize) || len(p.lastOps) != k[screen.OpCacheRef] {
		t.Fatalf("switching back gave %v, want only cache refs", k)
	}
	if p.enc.Stats().CacheRef != k[screen.OpCacheRef] {
		t.Fatalf("Stats.CacheRef = %d", p.enc.Stats().CacheRef)
	}
}

func TestLRU(t *testing.T) {
	c := newLRU()
	for k := range uint64(screen.CacheSlots) {
		if s := c.insert(k + 1); int(s) != int(k) {
			t.Fatalf("key %d got slot %d", k+1, s)
		}
	}
	// Touch key 1 so key 2 is the least recently used.
	if s, ok := c.lookup(1); !ok || s != 0 {
		t.Fatalf("lookup(1) = %d, %v", s, ok)
	}
	if s := c.insert(1 << 40); s != 1 {
		t.Fatalf("evicted slot %d, want 1 (key 2)", s)
	}
	if c.has(2) || !c.has(1) || !c.has(3) {
		t.Fatal("wrong key evicted")
	}
	if s := c.insert(1 << 41); s != 2 {
		t.Fatalf("evicted slot %d, want 2 (key 3)", s)
	}
	c.reset()
	if c.has(1) || c.insert(9) != 0 {
		t.Fatal("reset did not empty the cache")
	}
}

// TestCacheConsistency runs more distinct tiles than there are slots, with
// returns to earlier screens, and checks every cache ref against what the
// encoder's ops stored in that slot.
func TestCacheConsistency(t *testing.T) {
	rng := rand.New(rand.NewPCG(6, 6))
	const w, h = 1024, 512 // 128 tiles
	p := newPipe(t, Config{}, w, h)
	var screens []*image.RGBA
	slots := map[uint16]uint64{} // slot -> hash of the stored content
	content := func(img *image.RGBA, r screen.Rect) uint64 {
		s := swappedIf(img, false)
		return hashRect(p.enc.seed, s.Pix[s.PixOffset(r.X, r.Y):], s.Stride, r.W, r.H)
	}
	for i := range 120 {
		var img *image.RGBA
		if i > 10 && rng.IntN(3) == 0 {
			img = screens[rng.IntN(len(screens))]
		} else {
			img = uiImage(rng, w, h)
			screens = append(screens, img)
		}
		p.step(img, t0.Add(time.Duration(i)*time.Second))
		for _, r := range p.lastOps {
			if r.Kind == screen.OpCacheRef {
				if slots[r.Slot] != content(img, r) {
					t.Fatalf("frame %d: ref to slot %d with other content", i, r.Slot)
				}
			}
			if r.Store {
				slots[r.Slot] = content(img, r)
			}
		}
	}
	if p.ref.Stored <= screen.CacheSlots || p.ref.Refs == 0 {
		t.Fatalf("stored %d, refs %d: cache not exercised", p.ref.Stored, p.ref.Refs)
	}
}

func swappedIf(img *image.RGBA, swap bool) *image.RGBA {
	if swap {
		return swapped(img)
	}
	return img
}

func TestPartsSplit(t *testing.T) {
	if testing.Short() {
		t.Skip("large frames")
	}
	rng := rand.New(rand.NewPCG(7, 7))
	const w, h = 2560, 1440 // 14.7 MB of noise: needs raw tiles
	p := newPipe(t, Config{}, w, h)
	parts := p.step(noiseImage(rng, w, h), t0)
	if len(parts) < 2 {
		t.Fatalf("%d parts for a noisy 2560x1440 full frame", len(parts))
	}
	for i, f := range parts {
		size := frameHeader
		for _, r := range f.Rects {
			size += OpSize(r)
		}
		if size > screen.MaxPart {
			t.Fatalf("part %d has %d bytes", i, size)
		}
	}
}

func TestReset(t *testing.T) {
	rng := rand.New(rand.NewPCG(8, 8))
	const w, h = 400, 300
	p := newPipe(t, Config{}, w, h)
	a := uiImage(rng, w, h)
	p.step(a, t0)
	b := clone(a)
	uiFill(rng, b, image.Rect(10, 10, 200, 100))
	p.step(b, t0.Add(time.Second))
	p.reset()
	// The helper's image is garbage now (e.g. after a resize).
	for i := range p.refImg.Pix {
		p.refImg.Pix[i] = 7
	}
	if p.decImg != nil {
		copy(p.decImg.Pix, p.refImg.Pix)
	}
	p.step(b, t0.Add(2*time.Second))
	var cover image.Rectangle
	area := 0
	stored := map[uint16]bool{}
	for _, r := range p.lastOps {
		if r.Store {
			stored[r.Slot] = true
		}
		if r.Kind == screen.OpCacheRef && !stored[r.Slot] {
			t.Fatalf("full frame refers to slot %d from before the reset", r.Slot)
		}
		if r.Kind == screen.OpCopy || r.Kind == screen.OpJPEG {
			t.Fatalf("full frame has op kind %d", r.Kind)
		}
		cover = cover.Union(image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H))
		area += r.W * r.H
	}
	if cover != image.Rect(0, 0, w, h) || area != w*h {
		t.Fatalf("full frame covers %v with %d pixels", cover, area)
	}
	if len(p.enc.LossyRects()) != 0 {
		t.Fatal("full frame is lossy")
	}
}

func TestSizeChangeWithoutReset(t *testing.T) {
	rng := rand.New(rand.NewPCG(9, 9))
	p := newPipe(t, Config{}, 300, 200)
	p.step(uiImage(rng, 300, 200), t0)
	img := uiImage(rng, 200, 100)
	parts := p.enc.Encode(img, t0.Add(time.Second))
	area := 0
	for _, f := range parts {
		for _, r := range f.Rects {
			area += r.W * r.H
		}
	}
	if area != 200*100 {
		t.Fatalf("frame after a size change covers %d pixels", area)
	}
}

// TestUndefinedAlpha feeds captures whose alpha bytes are random, as GDI
// produces them: alpha changes alone are no damage, classification and
// cache work on the colors only, and the helper gets A = 255.
func TestUndefinedAlpha(t *testing.T) {
	rng := rand.New(rand.NewPCG(13, 13))
	const w, h = 320, 256
	scramble := func(img *image.RGBA) *image.RGBA {
		out := clone(img)
		for i := 3; i < len(out.Pix); i += 4 {
			out.Pix[i] = byte(rng.Uint32())
		}
		return out
	}
	for _, swap := range []bool{false, true} {
		p := newPipe(t, Config{SwapRB: swap}, w, h)
		a, b := uiImage(rng, w, h), uiImage(rng, w, h)
		p.step(scramble(a), t0)
		if k := countKinds(p.lastOps); k[screen.OpRaw] > 0 || k[screen.OpJPEG] > 0 {
			t.Fatalf("random alpha defeats the palette: %v", k)
		}
		if parts := p.step(scramble(a), t0.Add(time.Second)); parts != nil {
			t.Fatalf("alpha change alone gave %v", countKinds(p.lastOps))
		}
		p.step(scramble(b), t0.Add(2*time.Second))
		p.step(scramble(a), t0.Add(3*time.Second))
		if k := countKinds(p.lastOps); k[screen.OpCacheRef] != len(p.lastOps) {
			t.Fatalf("switching back with other alpha: %v", k)
		}
	}
}

func TestSwapRB(t *testing.T) {
	rng := rand.New(rand.NewPCG(10, 10))
	const w, h = 256, 192
	p := newPipe(t, Config{SwapRB: true}, w, h)
	a := uiImage(rng, w, h)
	p.step(a, t0)
	b := clone(a)
	uiFill(rng, b, image.Rect(70, 70, 150, 90))
	p.step(b, t0.Add(time.Second))
	p.step(smoothImage(w, h, 0), t0.Add(2*time.Second))
}

func TestLossyAndRefine(t *testing.T) {
	if !realCodecs() {
		t.Skip("proto/screen tile codecs are still stubs (EncodeJPEG returns nil)")
	}
	const w, h = 640, 360
	p := newPipe(t, Config{RefineAfter: 500 * time.Millisecond}, w, h)
	p.step(uiImage(rand.New(rand.NewPCG(11, 11)), w, h), t0)
	now := t0
	for i := range 10 {
		now = t0.Add(time.Duration(i+1) * time.Second / 30)
		p.step(smoothImage(w, h, i*3), now)
	}
	if p.enc.Stats().JPEG == 0 || len(p.enc.LossyRects()) == 0 {
		t.Fatalf("smooth video gave no JPEG: %+v", p.enc.Stats())
	}
	if p.maxDelta > 64 {
		t.Fatalf("lossy error up to %d", p.maxDelta)
	}
	due := p.enc.RefineDue()
	if want := now.Add(500 * time.Millisecond); !due.Equal(want) {
		t.Fatalf("RefineDue = %v, want %v", due, want)
	}
	last := smoothImage(w, h, 27)
	if parts := p.step(last, due.Add(-time.Millisecond)); parts != nil {
		t.Fatalf("refine before it is due: %v", countKinds(p.lastOps))
	}
	for k := 0; !p.enc.RefineDue().IsZero(); k++ {
		if k > 100 {
			t.Fatal("refine never finishes")
		}
		p.step(last, p.enc.RefineDue())
		if countKinds(p.lastOps)[screen.OpJPEG] > 0 {
			t.Fatal("refine sent JPEG")
		}
	}
	if len(p.enc.LossyRects()) != 0 {
		t.Fatal("lossy tiles left after refine")
	}
	p.check(last) // now exact everywhere
	if p.enc.Stats().Refined == 0 {
		t.Fatal("Stats.Refined is 0")
	}
}

// TestScenes runs a few frames of every synthetic scenario through the
// pipeline, then refines and checks for an exact image.
func TestScenes(t *testing.T) {
	for _, sc := range scenes.All() {
		t.Run(sc.Name, func(t *testing.T) {
			n := 12
			if testing.Short() {
				n = 4
			}
			pl := sc.New()
			img := image.NewRGBA(image.Rect(0, 0, sc.Width, sc.Height))
			p := newPipe(t, Config{}, sc.Width, sc.Height)
			now := t0
			for i := range n {
				pl.Frame(i, img)
				now = t0.Add(time.Duration(i) * time.Second / time.Duration(sc.FPS))
				p.step(img, now)
			}
			for k := 0; !p.enc.RefineDue().IsZero(); k++ {
				if k > 200 {
					t.Fatal("refine never finishes")
				}
				now = maxTime(now.Add(time.Millisecond), p.enc.RefineDue())
				p.step(img, now)
			}
			if len(p.enc.LossyRects()) != 0 {
				t.Fatal("lossy tiles left")
			}
		})
	}
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func TestRateController(t *testing.T) {
	e := New(Config{})
	q0 := e.Stats().JPEGQuality
	// A slow, congested link: 2 Mbit/s with growing delay.
	for i := range 200 {
		e.rate.sent(200 << 10)
		e.Acked(200<<10, 20*time.Millisecond+time.Duration(i)*time.Millisecond)
	}
	s := e.Stats()
	if s.JPEGQuality >= q0 || s.JPEGQuality < 40 || s.TargetFPS > 30 || s.TargetFPS < 5 {
		t.Fatalf("congested: quality %d (from %d), fps %d", s.JPEGQuality, q0, s.TargetFPS)
	}
	// A fast LAN with small frames: back up.
	for range 400 {
		e.rate.sent(20 << 10)
		e.Acked(20<<10, 2*time.Millisecond)
	}
	s = e.Stats()
	if s.JPEGQuality != 90 || s.TargetFPS != 60 {
		t.Fatalf("fast link: quality %d, fps %d", s.JPEGQuality, s.TargetFPS)
	}
	e.SetQuality(screen.QualityFast)
	if s := e.Stats(); s.JPEGQuality > 70 {
		t.Fatalf("fast mode: quality %d", s.JPEGQuality)
	}
	e.SetQuality(screen.QualitySharp)
	if s := e.Stats(); s.JPEGQuality < 75 || s.TargetFPS > 30 {
		t.Fatalf("sharp mode: quality %d, fps %d", s.JPEGQuality, s.TargetFPS)
	}
	if e.FrameInterval() < time.Second/30 {
		t.Fatalf("sharp mode: interval %v", e.FrameInterval())
	}
}

func TestRecordingRoundTrip(t *testing.T) {
	dir := t.TempDir() + "/rec"
	rng := rand.New(rand.NewPCG(12, 12))
	r, err := NewRecorder(dir, 120, 80, true)
	if err != nil {
		t.Fatal(err)
	}
	var imgs []*image.RGBA
	for i := range 3 {
		big := uiImage(rng, 140, 90)
		img := big.SubImage(image.Rect(10, 5, 130, 85)).(*image.RGBA) // stride != width
		imgs = append(imgs, img)
		if err := r.Add(img, t0.Add(time.Duration(i)*40*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Add(image.NewRGBA(image.Rect(0, 0, 10, 10)), t0); err == nil {
		t.Fatal("frame of the wrong size accepted")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRecorder(dir, 120, 80, true); err == nil {
		t.Fatal("NewRecorder accepted a non-empty directory")
	}
	rec, err := OpenRecording(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Width != 120 || rec.Height != 80 || !rec.BGRA || len(rec.Frames) != 3 || rec.Frames[2].T != 80*time.Millisecond {
		t.Fatalf("recording %+v", rec)
	}
	var dst *image.RGBA
	for i, want := range imgs {
		if dst, err = rec.Load(i, dst); err != nil {
			t.Fatal(err)
		}
		for y := range 80 {
			if !bytes.Equal(dst.Pix[y*dst.Stride:][:120*4], want.Pix[want.PixOffset(10, 5+y):][:120*4]) {
				t.Fatalf("frame %d row %d differs", i, y)
			}
		}
	}
	if _, err := rec.Load(3, nil); err == nil {
		t.Fatal("Load past the end")
	}
	if _, err := OpenRecording(t.TempDir()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("OpenRecording of an empty directory: %v", err)
	}
}

// BenchmarkEncode measures Encode per frame of the synthetic scenarios
// (rendering is not timed).
func BenchmarkEncode(b *testing.B) {
	for _, sc := range scenes.All() {
		b.Run(sc.Name, func(b *testing.B) {
			pl := sc.New()
			img := image.NewRGBA(image.Rect(0, 0, sc.Width, sc.Height))
			e := New(Config{})
			pl.Frame(0, img)
			e.Encode(img, t0)
			frame := 1
			bytes := 0
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				if frame == sc.Frames {
					pl, frame = sc.New(), 0
				}
				pl.Frame(frame, img)
				b.StartTimer()
				for _, f := range e.Encode(img, t0.Add(time.Duration(i+1)*time.Second/time.Duration(sc.FPS))) {
					for _, r := range f.Rects {
						bytes += OpSize(r)
					}
				}
				frame++
			}
			b.ReportMetric(float64(bytes)/float64(b.N), "B/frame")
		})
	}
}
