package screen

import (
	"errors"
	"fmt"
	"image"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"
)

// Limits of a Decoder.
const (
	maxParts     = 64       // most parts of one frame
	maxPartBytes = 64 << 20 // most buffered bytes (op structs plus payloads) of one frame
	decodeBudget = 64 << 20 // most decoded tile pixels held at once
	maxDirty     = 256      // most dirty rectangles returned by Apply
	exactDirty   = 16       // up to this many ops, dirty rects are returned exactly
)

// slot is one tile cache entry: w*h*4 bytes of tightly packed pixels.
type slot struct {
	w, h int
	pix  []byte
}

// Decoder applies frames on the techie. It owns the tile cache and buffers
// multi-part frames. Not safe for concurrent use; it may use several
// goroutines internally to decode the ops of one frame in parallel.
//
// Apply validates every op of a frame against dst and the cache before it
// writes anything and decodes all tile payloads before applying them, so a
// frame that fails leaves dst and the cache unchanged. The only exception is
// a frame whose decoded tiles exceed 64 MiB, which is applied in batches.
type Decoder struct {
	slots []slot // CacheSlots entries, allocated on first Store

	pending      []Rect // ops of the buffered parts
	pendingSeq   uint32
	pendingParts int
	pendingBytes int

	stores  map[uint16][2]int // slot sizes as of the op being validated
	scratch []byte            // decoded tile pixels of the current batch
	offs    []int             // per op of the batch: offset into scratch, or -1
	jobs    []int             // indices of ops to decode
	cells   []bool            // dirty grid
}

// NewDecoder returns a decoder with an empty cache.
func NewDecoder() *Decoder { return &Decoder{} }

// Reset clears the cache and any buffered parts (after Hello or Refresh).
func (d *Decoder) Reset() {
	d.slots = nil
	d.dropParts()
}

func (d *Decoder) dropParts() {
	clear(d.pending) // release the payloads the ops alias
	d.pending = d.pending[:0]
	d.pendingParts, d.pendingBytes = 0, 0
}

func partBytes(rects []Rect) int {
	n := 0
	for i := range rects {
		n += int(unsafe.Sizeof(rects[i])) + len(rects[i].RGBA) + len(rects[i].Data)
	}
	return n
}

// Apply applies one received frame part to dst. For a part with More set it
// only buffers and returns complete=false. For the last part it applies the
// whole frame and returns the dirty rectangles (to upload to the GPU) and
// complete=true. On any error dst may be partly updated; the caller should
// request a Refresh.
//
// Buffered parts keep the slices of their rects (which alias the received
// payloads) until the last part arrives. A frame may have at most 64 parts
// and 64 MiB of ops and payloads; a part with another Seq than the buffered
// ones is an error. After an error the buffered parts are dropped.
//
// The dirty rectangles are in dst's coordinates (offset by dst.Rect.Min),
// lie within dst.Rect and are coarsened to at most a few hundred; the slice
// is valid until the next call.
func (d *Decoder) Apply(dst *image.RGBA, f Frame) (dirty []image.Rectangle, complete bool, err error) {
	if d.pendingParts > 0 && f.Seq != d.pendingSeq {
		d.dropParts()
		return nil, false, fmt.Errorf("screen: frame %d part while frame %d is incomplete", f.Seq, d.pendingSeq)
	}
	d.pendingSeq = f.Seq
	d.pendingParts++
	d.pendingBytes += partBytes(f.Rects)
	if d.pendingParts > maxParts || d.pendingBytes > maxPartBytes {
		d.dropParts()
		return nil, false, fmt.Errorf("screen: frame %d too large (more than %d parts or %d bytes)", f.Seq, maxParts, maxPartBytes)
	}
	if f.More {
		d.pending = append(d.pending, f.Rects...)
		return nil, false, nil
	}
	ops := f.Rects
	if d.pendingParts > 1 {
		d.pending = append(d.pending, f.Rects...)
		ops = d.pending
	}
	defer d.dropParts()
	if dst == nil {
		return nil, false, errors.New("screen: nil image")
	}
	if err := d.validate(dst, ops); err != nil {
		return nil, false, err
	}
	for i := 0; i < len(ops); {
		j, err := d.decodeBatch(ops, i)
		if err != nil {
			return nil, false, err
		}
		d.applyBatch(dst, ops[i:j])
		i = j
	}
	return d.dirtyRects(dst, ops), true, nil
}

// slotSize returns the size of slot s as of the op being validated.
func (d *Decoder) slotSize(s uint16) (w, h int) {
	if sz, ok := d.stores[s]; ok {
		return sz[0], sz[1]
	}
	if d.slots == nil {
		return 0, 0
	}
	return d.slots[s].w, d.slots[s].h
}

// validate checks every op against dst and the cache, following the
// stores of earlier ops of the frame.
func (d *Decoder) validate(dst *image.RGBA, ops []Rect) error {
	bw, bh := dst.Rect.Dx(), dst.Rect.Dy()
	clear(d.stores)
	for i := range ops {
		r := &ops[i]
		if !inBounds(r.X, r.Y, r.W, r.H, bw, bh) {
			return fmt.Errorf("screen: op %d (%dx%d at %d,%d) outside %dx%d screen", i, r.W, r.H, r.X, r.Y, bw, bh)
		}
		switch r.Kind {
		case OpRaw:
			if len(r.RGBA) != r.W*r.H*4 {
				return fmt.Errorf("screen: op %d has %d pixel bytes, want %d", i, len(r.RGBA), r.W*r.H*4)
			}
		case OpSolid:
		case OpPalette, OpJPEG:
			if len(r.Data) == 0 {
				return fmt.Errorf("screen: op %d has no data", i)
			}
		case OpCopy:
			if !inBounds(r.SrcX, r.SrcY, r.W, r.H, bw, bh) {
				return fmt.Errorf("screen: op %d copies from %dx%d at %d,%d outside %dx%d screen", i, r.W, r.H, r.SrcX, r.SrcY, bw, bh)
			}
		case OpCacheRef:
			if r.Slot >= CacheSlots {
				return fmt.Errorf("screen: op %d refers to slot %d", i, r.Slot)
			}
			if w, h := d.slotSize(r.Slot); w != r.W || h != r.H {
				return fmt.Errorf("screen: op %d draws slot %d of %dx%d as %dx%d", i, r.Slot, w, h, r.W, r.H)
			}
		default:
			return fmt.Errorf("screen: op %d has kind %d", i, r.Kind)
		}
		if r.Store {
			if !storable(r.Kind) {
				return fmt.Errorf("screen: op %d of kind %d cannot be stored", i, r.Kind)
			}
			if r.Slot >= CacheSlots {
				return fmt.Errorf("screen: op %d stores into slot %d", i, r.Slot)
			}
			if r.W > MaxSlotSize || r.H > MaxSlotSize {
				return fmt.Errorf("screen: op %d stores a %dx%d tile", i, r.W, r.H)
			}
			if d.stores == nil {
				d.stores = make(map[uint16][2]int)
			}
			d.stores[r.Slot] = [2]int{r.W, r.H}
		}
	}
	return nil
}

// decodeBatch decodes the payloads of the ops from start on, as many as fit
// in decodeBudget (at least one), into d.scratch and returns the end of the
// batch.
func (d *Decoder) decodeBatch(ops []Rect, start int) (end int, err error) {
	d.offs = d.offs[:0]
	d.jobs = d.jobs[:0]
	total := 0
	end = start
	for ; end < len(ops); end++ {
		r := &ops[end]
		if r.Kind != OpPalette && r.Kind != OpJPEG {
			d.offs = append(d.offs, -1)
			continue
		}
		n := r.W * r.H * 4
		if total > 0 && total+n > decodeBudget {
			break
		}
		d.offs = append(d.offs, total)
		d.jobs = append(d.jobs, end-start)
		total += n
	}
	if len(d.jobs) == 0 {
		return end, nil
	}
	if cap(d.scratch) < total {
		d.scratch = make([]byte, total)
	}
	scratch := d.scratch[:total]
	batch := ops[start:end]
	decode := func(k int) error {
		r := &batch[k]
		out := scratch[d.offs[k] : d.offs[k]+r.W*r.H*4]
		if r.Kind == OpPalette {
			return decodePalette(r.Data, r.W, r.H, out)
		}
		return decodeJPEG(r.Data, r.W, r.H, out)
	}

	workers := min(runtime.GOMAXPROCS(0), len(d.jobs))
	if workers <= 1 || total < 4*TileSize*TileSize*4 {
		for _, k := range d.jobs {
			if err := decode(k); err != nil {
				return 0, fmt.Errorf("screen: op %d: %w", start+k, err)
			}
		}
		return end, nil
	}
	var (
		next     atomic.Int64
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		firstOp  = len(ops)
	)
	for range workers {
		wg.Go(func() {
			for {
				j := int(next.Add(1)) - 1
				if j >= len(d.jobs) {
					return
				}
				k := d.jobs[j]
				if err := decode(k); err != nil {
					mu.Lock()
					if start+k < firstOp {
						firstOp, firstErr = start+k, err
					}
					mu.Unlock()
					next.Store(int64(len(d.jobs))) // stop the other workers early
					return
				}
			}
		})
	}
	wg.Wait()
	if firstErr != nil {
		return 0, fmt.Errorf("screen: op %d: %w", firstOp, firstErr)
	}
	return end, nil
}

// applyBatch applies validated ops whose payloads decodeBatch decoded.
func (d *Decoder) applyBatch(dst *image.RGBA, batch []Rect) {
	for k := range batch {
		r := &batch[k]
		switch r.Kind {
		case OpRaw:
			blit(dst, r.X, r.Y, r.W, r.H, r.RGBA)
		case OpSolid:
			fillRect(dst, r.X, r.Y, r.W, r.H, r.Color)
		case OpPalette, OpJPEG:
			blit(dst, r.X, r.Y, r.W, r.H, d.scratch[d.offs[k]:d.offs[k]+r.W*r.H*4])
		case OpCopy:
			copyRect(dst, r.SrcX, r.SrcY, r.X, r.Y, r.W, r.H)
		case OpCacheRef:
			blit(dst, r.X, r.Y, r.W, r.H, d.slots[r.Slot].pix)
		}
		if r.Store {
			d.store(dst, r)
		}
	}
}

// store saves the pixels r just drew into its slot.
func (d *Decoder) store(dst *image.RGBA, r *Rect) {
	if d.slots == nil {
		d.slots = make([]slot, CacheSlots)
	}
	s := &d.slots[r.Slot]
	n := r.W * r.H * 4
	if cap(s.pix) < n {
		s.pix = make([]byte, n)
	}
	s.w, s.h, s.pix = r.W, r.H, s.pix[:n]
	rowLen := r.W * 4
	off := r.Y*dst.Stride + r.X*4
	for y := 0; y < r.H; y++ {
		copy(s.pix[y*rowLen:(y+1)*rowLen], dst.Pix[off:off+rowLen])
		off += dst.Stride
	}
}

func fillRect(dst *image.RGBA, x, y, w, h int, c [4]byte) {
	rowLen := w * 4
	off := y*dst.Stride + x*4
	first := dst.Pix[off : off+rowLen]
	for i := 0; i < rowLen; i += 4 {
		copy(first[i:i+4], c[:])
	}
	for row := 1; row < h; row++ {
		off += dst.Stride
		copy(dst.Pix[off:off+rowLen], first)
	}
}

// copyRect copies the w x h rect at sx,sy to dx,dy within dst like memmove:
// overlapping rects see the source as it was before the copy.
func copyRect(dst *image.RGBA, sx, sy, dx, dy, w, h int) {
	rowLen := w * 4
	so := sy*dst.Stride + sx*4
	do := dy*dst.Stride + dx*4
	if dy > sy {
		// Moving down: copy bottom rows first.
		for row := h - 1; row >= 0; row-- {
			s, t := so+row*dst.Stride, do+row*dst.Stride
			copy(dst.Pix[t:t+rowLen], dst.Pix[s:s+rowLen])
		}
		return
	}
	// Moving up or within the same rows: top rows first; copy itself handles
	// overlap within a row.
	for row := 0; row < h; row++ {
		s, t := so+row*dst.Stride, do+row*dst.Stride
		copy(dst.Pix[t:t+rowLen], dst.Pix[s:s+rowLen])
	}
}

// dirtyRects returns the area written by ops, coarsened to a grid of cells
// when there are many ops, in dst's coordinates.
func (d *Decoder) dirtyRects(dst *image.RGBA, ops []Rect) []image.Rectangle {
	org := dst.Rect.Min
	out := make([]image.Rectangle, 0, min16(len(ops)))
	if len(ops) == 0 {
		return out
	}
	if len(ops) <= exactDirty {
	next:
		for i := range ops {
			r := image.Rect(ops[i].X, ops[i].Y, ops[i].X+ops[i].W, ops[i].Y+ops[i].H).Add(org)
			for j := range out {
				if r.In(out[j]) {
					continue next
				}
			}
			// Drop earlier rects the new one covers.
			k := 0
			for _, o := range out {
				if !o.In(r) {
					out[k] = o
					k++
				}
			}
			out = append(out[:k], r)
		}
		return out
	}

	bw, bh := dst.Rect.Dx(), dst.Rect.Dy()
	cs := TileSize
	for (bw+cs-1)/cs > 32 || (bh+cs-1)/cs > 32 {
		cs *= 2
	}
	gw, gh := (bw+cs-1)/cs, (bh+cs-1)/cs
	if cap(d.cells) < gw*gh {
		d.cells = make([]bool, gw*gh)
	}
	cells := d.cells[:gw*gh]
	clear(cells)
	for i := range ops {
		r := &ops[i]
		for cy := r.Y / cs; cy <= (r.Y+r.H-1)/cs; cy++ {
			row := cells[cy*gw : (cy+1)*gw]
			for cx := r.X / cs; cx <= (r.X+r.W-1)/cs; cx++ {
				row[cx] = true
			}
		}
	}
	cell := func(cx0, cy0, cx1, cy1 int) image.Rectangle {
		return image.Rect(cx0*cs, cy0*cs, cx1*cs, cy1*cs).Intersect(image.Rect(0, 0, bw, bh)).Add(org)
	}
	// Horizontal runs per cell row, merged with an identical run directly
	// above (open lists the runs that ended on the previous row).
	open := map[[2]int]int{} // [x0, x1] -> index in out of a rect ending on the previous row
	for cy := 0; cy < gh; cy++ {
		row := cells[cy*gw : (cy+1)*gw]
		nextOpen := map[[2]int]int{}
		for cx := 0; cx < gw; {
			if !row[cx] {
				cx++
				continue
			}
			x0 := cx
			for cx < gw && row[cx] {
				cx++
			}
			key := [2]int{x0, cx}
			if i, ok := open[key]; ok {
				out[i].Max.Y = cell(x0, cy, cx, cy+1).Max.Y
				nextOpen[key] = i
			} else {
				nextOpen[key] = len(out)
				out = append(out, cell(x0, cy, cx, cy+1))
			}
		}
		open = nextOpen
	}
	if len(out) <= maxDirty {
		return out
	}
	// Too fragmented: one rect per cell row spanning all its dirty cells.
	out = out[:0]
	for cy := 0; cy < gh; cy++ {
		row := cells[cy*gw : (cy+1)*gw]
		x0, x1 := -1, -1
		for cx, m := range row {
			if m {
				if x0 < 0 {
					x0 = cx
				}
				x1 = cx + 1
			}
		}
		if x0 >= 0 {
			out = append(out, cell(x0, cy, x1, cy+1))
		}
	}
	return out
}

func min16(n int) int {
	if n < exactDirty {
		return n
	}
	return exactDirty
}
