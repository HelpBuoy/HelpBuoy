package encode

import (
	"encoding/binary"
	"image"
	"time"

	"github.com/mevdschee/helpbuoy/proto/screen"
)

// tileBytes is the scratch space per tile in e.tilePix.
const tileBytes = TileSize * TileSize * 4

// maxJPEGTiles bounds the JPEG rectangles merged from neighbouring tiles
// (in tiles per axis): large enough to amortize the JPEG headers, small
// enough to keep all workers busy.
const (
	maxJPEGTilesX = 8
	maxJPEGTilesY = 2
)

// tileJob is one tile to send in the current frame.
type tileJob struct {
	t        int
	r        image.Rectangle // what to send: the whole tile or its changed part
	lossless bool            // full frame or refine: no JPEG
	refine   bool
	fullKey  uint64 // cache key of the whole tile
	key      uint64 // cache key of r
	ref      bool   // send as OpCacheRef of key
	jpeg     bool   // needs JPEG
	lossy    bool   // ended up lossy
	ops      []encOp
}

// encOp is an op plus, for lossless palette and raw ops, its cache key.
type encOp struct {
	rect      screen.Rect
	key       uint64
	cacheable bool
}

// fullFrame sends every tile losslessly into an empty cache.
func (e *Encoder) fullFrame(src pixSource, now time.Time) []screen.Rect {
	jobs := make([]tileJob, len(e.dirty))
	for t := range jobs {
		jobs[t] = tileJob{t: t, lossless: true}
	}
	clear(e.lossy)
	return e.encodeJobs(nil, src, jobs, now, false)
}

// update sends what changed since the last frame plus due refines.
func (e *Encoder) update(src pixSource, now time.Time) []screen.Rect {
	n := e.damage(src)
	var ops []screen.Rect
	if n >= minRun {
		ops = e.moves(src, now)
	}
	var jobs []tileJob
	for t, d := range e.dirty {
		if d {
			jobs = append(jobs, tileJob{t: t})
		}
	}
	// Refine lossy tiles that stayed unchanged, oldest first within the
	// budget; the rest stay due and RefineDue says so.
	budget := e.rate.refineBudget()
	for t, l := range e.lossy {
		if budget <= 0 {
			break
		}
		if l && !e.dirty[t] && !e.refineAt[t].After(now) {
			jobs = append(jobs, tileJob{t: t, lossless: true, refine: true})
			r := e.tileRect(t)
			budget -= r.Dx() * r.Dy() * 2 // assume half of raw
		}
	}
	if len(jobs) == 0 {
		return ops
	}
	return e.encodeJobs(ops, src, jobs, now, true)
}

// refineDelay is how long a lossy tile waits for its refine.
func (e *Encoder) refineDelay() time.Duration {
	if e.rate.mode == screen.QualitySharp {
		return e.cfg.RefineAfter / 2
	}
	return e.cfg.RefineAfter
}

// tileSrc returns the RGBA pixels of tile t extracted into e.tilePix and
// their stride.
func (e *Encoder) tileSrc(t int) ([]byte, int) {
	r := e.tileRect(t)
	return e.tilePix[t*tileBytes : t*tileBytes+r.Dx()*r.Dy()*4], r.Dx() * 4
}

// sub returns the pixels of rectangle r (screen coordinates, within tile t)
// in the extracted tile.
func (e *Encoder) sub(t int, r image.Rectangle) ([]byte, int) {
	pix, stride := e.tileSrc(t)
	tr := e.tileRect(t)
	off := (r.Min.Y-tr.Min.Y)*stride + (r.Min.X-tr.Min.X)*4
	return pix[off:], stride
}

// encodeJobs encodes jobs, appends their ops to ops and updates the
// shadow, the lossy flags and the cache. trim allows sending only the
// changed part of a tile.
func (e *Encoder) encodeJobs(ops []screen.Rect, src pixSource, jobs []tileJob, now time.Time, trim bool) []screen.Rect {
	swap := e.cfg.SwapRB
	// Extract, hash and trim.
	e.parallel(len(jobs), func(i int) {
		j := &jobs[i]
		tr := e.tileRect(j.t)
		pix, stride := e.tileSrc(j.t)
		extract(pix, stride, src, tr, swap)
		j.fullKey = hashRect(e.seed, pix, stride, tr.Dx(), tr.Dy())
		j.r, j.key = tr, j.fullKey
		if trim && !j.lossless && !e.lossy[j.t] {
			b := e.diffBounds(src, j.t)
			if !b.Empty() && b.Dx()*b.Dy()*8 <= tr.Dx()*tr.Dy()*7 {
				p, s := e.sub(j.t, b)
				j.r, j.key = b, hashRect(e.seed, p, s, b.Dx(), b.Dy())
			}
		}
	})
	// Cache lookups. Hits are touched now so slots stored later in this
	// frame don't evict them.
	for i := range jobs {
		j := &jobs[i]
		if _, ok := e.cache.lookup(j.fullKey); ok {
			j.ref, j.key, j.r = true, j.fullKey, e.tileRect(j.t)
		} else if j.key != j.fullKey {
			if _, ok := e.cache.lookup(j.key); ok {
				j.ref = true
			}
		}
	}
	// Classify and encode lossless tiles.
	q := e.rate.jpegQuality()
	e.parallel(len(jobs), func(i int) {
		j := &jobs[i]
		if j.ref {
			return
		}
		pix, stride := e.sub(j.t, j.r)
		if j.lossless {
			j.ops = losslessOps(e, pix, stride, j.r, j.key, true)
			return
		}
		if op, ok := solidOp(pix, stride, j.r); ok {
			j.ops = []encOp{{rect: op}}
			return
		}
		if op, ok := paletteOp(pix, stride, j.r); ok {
			j.ops = []encOp{{rect: op, key: j.key, cacheable: true}}
			return
		}
		// Text in several colors with antialiasing often has more than 256
		// colors per tile but not per quadrant: keep it lossless.
		if ops, ok := quadrantOps(e, pix, stride, j.r); ok {
			j.ops = ops
			return
		}
		j.jpeg = true
	})
	jpegs := e.encodeJPEGs(src, jobs, q)
	// Emit in order, assigning cache slots.
	for i := range jobs {
		j := &jobs[i]
		if j.ref {
			if slot, ok := e.cache.lookup(j.key); ok {
				ops = append(ops, screen.Rect{Kind: screen.OpCacheRef, X: j.r.Min.X, Y: j.r.Min.Y, W: j.r.Dx(), H: j.r.Dy(), Slot: slot})
				e.stats.CacheRef++
				continue
			}
			// Evicted within this frame: encode it after all.
			pix, stride := e.sub(j.t, j.r)
			j.ops = losslessOps(e, pix, stride, j.r, j.key, true)
		}
		if j.jpeg {
			continue
		}
		for _, o := range j.ops {
			ops = e.emit(ops, o)
		}
	}
	for _, op := range jpegs {
		ops = append(ops, op)
		e.stats.JPEG++
	}
	// The helper now has these tiles; JPEG ones are lossy until refined.
	delay := e.refineDelay()
	for i := range jobs {
		j := &jobs[i]
		if j.refine {
			e.stats.Refined++
		}
		if j.lossy {
			e.lossy[j.t] = true
			e.refineAt[j.t] = now.Add(delay)
		} else {
			// Trimmed tiles are never lossy, so the whole tile is exact.
			e.lossy[j.t] = false
		}
	}
	e.parallel(len(jobs), func(i int) {
		if !jobs[i].refine {
			e.storeShadow(src, e.tileRect(jobs[i].t))
		}
	})
	return ops
}

// emit appends a lossless op, turning it into a cache ref if its content
// is cached and storing it otherwise.
func (e *Encoder) emit(ops []screen.Rect, o encOp) []screen.Rect {
	r := o.rect
	if o.cacheable {
		if slot, ok := e.cache.lookup(o.key); ok {
			e.stats.CacheRef++
			return append(ops, screen.Rect{Kind: screen.OpCacheRef, X: r.X, Y: r.Y, W: r.W, H: r.H, Slot: slot})
		}
		r.Store, r.Slot = true, e.cache.insert(o.key)
	}
	switch r.Kind {
	case screen.OpRaw:
		e.stats.Raw++
	case screen.OpSolid:
		e.stats.Solid++
	case screen.OpPalette:
		e.stats.Palette++
	}
	return append(ops, r)
}

// encodeJPEGs encodes the jobs marked jpeg, merging whole tiles that touch
// into rectangles of up to maxJPEGTilesX x maxJPEGTilesY tiles, and
// returns the JPEG ops. Jobs whose JPEG fails get lossless ops instead and
// lose their jpeg mark, so the caller emits them in order with the rest.
func (e *Encoder) encodeJPEGs(src pixSource, jobs []tileJob, q int) []screen.Rect {
	byTile := map[int]int{}
	var groups []jpegGroup
	for i := range jobs {
		j := &jobs[i]
		if !j.jpeg {
			continue
		}
		if j.r == e.tileRect(j.t) {
			byTile[j.t] = i
		} else {
			groups = append(groups, jpegGroup{r: j.r, jobs: []int{i}})
		}
	}
	groups = append(groups, e.mergeTiles(byTile)...)
	out := make([]screen.Rect, len(groups))
	e.parallel(len(groups), func(gi int) {
		g := groups[gi]
		w, h := g.r.Dx(), g.r.Dy()
		buf := make([]byte, w*h*4)
		extract(buf, w*4, src, g.r, e.cfg.SwapRB)
		data := screen.EncodeJPEG(buf, w*4, w, h, q)
		if len(data) == 0 || len(data) >= len(buf) {
			// No JPEG (or no gain): lossless instead.
			for _, i := range g.jobs {
				j := &jobs[i]
				pix, stride := e.sub(j.t, j.r)
				j.ops = losslessOps(e, pix, stride, j.r, j.key, true)
				j.jpeg = false
			}
			return
		}
		for _, i := range g.jobs {
			jobs[i].lossy = true
		}
		out[gi] = screen.Rect{Kind: screen.OpJPEG, X: g.r.Min.X, Y: g.r.Min.Y, W: w, H: h, Data: data}
	})
	var ops []screen.Rect
	for _, op := range out {
		if op.Kind == screen.OpJPEG {
			ops = append(ops, op)
		}
	}
	return ops
}

// jpegGroup is one JPEG rectangle and the jobs it covers.
type jpegGroup struct {
	r    image.Rectangle
	jobs []int
}

// mergeTiles groups whole tiles into rectangles: horizontal runs per tile
// row, joined with the identical run of the next row.
func (e *Encoder) mergeTiles(byTile map[int]int) []jpegGroup {
	var groups []jpegGroup
	done := map[int]bool{}
	for ty := range e.tilesY {
		for tx := 0; tx < e.tilesX; tx++ {
			t := ty*e.tilesX + tx
			if _, ok := byTile[t]; !ok || done[t] {
				continue
			}
			w := 1
			for w < maxJPEGTilesX && tx+w < e.tilesX && !done[t+w] {
				if _, ok := byTile[t+w]; !ok {
					break
				}
				w++
			}
			h := 1
			for h < maxJPEGTilesY && ty+h < e.tilesY {
				full := true
				for k := range w {
					n := (ty+h)*e.tilesX + tx + k
					if _, ok := byTile[n]; !ok || done[n] {
						full = false
						break
					}
				}
				if !full {
					break
				}
				h++
			}
			var g jpegGroup
			for dy := range h {
				for dx := range w {
					n := (ty+dy)*e.tilesX + tx + dx
					done[n] = true
					g.jobs = append(g.jobs, byTile[n])
				}
			}
			g.r = e.tileRect(t).Union(e.tileRect((ty+h-1)*e.tilesX + tx + w - 1))
			groups = append(groups, g)
		}
	}
	return groups
}

// solidOp returns an OpSolid if the rectangle has one color.
func solidOp(pix []byte, stride int, r image.Rectangle) (screen.Rect, bool) {
	c, ok := screen.SolidColor(pix, stride, r.Dx(), r.Dy())
	if !ok {
		return screen.Rect{}, false
	}
	return screen.Rect{Kind: screen.OpSolid, X: r.Min.X, Y: r.Min.Y, W: r.Dx(), H: r.Dy(), Color: c}, true
}

// paletteOp returns an OpPalette if the rectangle has at most 256 colors
// and the result is smaller than raw pixels.
func paletteOp(pix []byte, stride int, r image.Rectangle) (screen.Rect, bool) {
	data, ok := screen.EncodePalette(pix, stride, r.Dx(), r.Dy())
	if !ok || len(data)+4 >= r.Dx()*r.Dy()*4 {
		return screen.Rect{}, false
	}
	return screen.Rect{Kind: screen.OpPalette, X: r.Min.X, Y: r.Min.Y, W: r.Dx(), H: r.Dy(), Data: data}, true
}

// losslessOps encodes a rectangle exactly: solid, palette, or, if split is
// set and the rectangle is larger than 32x32, palette/raw per quadrant
// (gradients and photos often have more than 256 colors per tile but not
// per quadrant), else raw.
func losslessOps(e *Encoder, pix []byte, stride int, r image.Rectangle, key uint64, split bool) []encOp {
	if op, ok := solidOp(pix, stride, r); ok {
		return []encOp{{rect: op}}
	}
	if op, ok := paletteOp(pix, stride, r); ok {
		return []encOp{{rect: op, key: key, cacheable: true}}
	}
	const half = TileSize / 2
	if split && (r.Dx() > half || r.Dy() > half) {
		var out []encOp
		for y := r.Min.Y; y < r.Max.Y; y += half {
			for x := r.Min.X; x < r.Max.X; x += half {
				q := image.Rect(x, y, min(x+half, r.Max.X), min(y+half, r.Max.Y))
				p := pix[(y-r.Min.Y)*stride+(x-r.Min.X)*4:]
				out = append(out, losslessOps(e, p, stride, q, hashRect(e.seed, p, stride, q.Dx(), q.Dy()), false)...)
			}
		}
		return out
	}
	w, h := r.Dx(), r.Dy()
	raw := make([]byte, w*h*4)
	for y := range h {
		copy(raw[y*w*4:(y+1)*w*4], pix[y*stride:y*stride+w*4])
	}
	return []encOp{{rect: screen.Rect{Kind: screen.OpRaw, X: r.Min.X, Y: r.Min.Y, W: w, H: h, RGBA: raw}, key: key, cacheable: true}}
}

// quadrantOps encodes a rectangle larger than 32x32 as solid or palette
// ops per 32x32 quadrant, and fails as soon as one quadrant needs more
// than 256 colors (then the content is photo-like and JPEG is better).
func quadrantOps(e *Encoder, pix []byte, stride int, r image.Rectangle) ([]encOp, bool) {
	const half = TileSize / 2
	if r.Dx() <= half && r.Dy() <= half {
		return nil, false
	}
	var out []encOp
	for y := r.Min.Y; y < r.Max.Y; y += half {
		for x := r.Min.X; x < r.Max.X; x += half {
			q := image.Rect(x, y, min(x+half, r.Max.X), min(y+half, r.Max.Y))
			p := pix[(y-r.Min.Y)*stride+(x-r.Min.X)*4:]
			if op, ok := solidOp(p, stride, q); ok {
				out = append(out, encOp{rect: op})
			} else if op, ok := paletteOp(p, stride, q); ok {
				out = append(out, encOp{rect: op, key: hashRect(e.seed, p, stride, q.Dx(), q.Dy()), cacheable: true})
			} else {
				return nil, false
			}
		}
	}
	return out, true
}

// extract copies rectangle r of src to dst (with the given stride) as
// RGBA, swapping R and B if swap is set.
func extract(dst []byte, stride int, src pixSource, r image.Rectangle, swap bool) {
	n := r.Dx() * 4
	for y := r.Min.Y; y < r.Max.Y; y++ {
		d := dst[(y-r.Min.Y)*stride : (y-r.Min.Y)*stride+n]
		s := src.row(y, r.Min.X, r.Max.X)
		if !swap {
			copy(d, s)
			continue
		}
		for i := 0; i < n; i += 4 {
			v := binary.LittleEndian.Uint32(s[i:])
			v = v&0xff00ff00 | v>>16&0xff | v&0xff<<16
			binary.LittleEndian.PutUint32(d[i:], v)
		}
	}
}
