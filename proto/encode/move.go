package encode

import (
	"bytes"
	"encoding/binary"
	"hash/maphash"
	"image"
	"slices"
	"time"

	"github.com/mevdschee/helpbuoy/proto/screen"
)

const (
	// minRun is the shortest vertical run of damaged tiles in one column
	// band that is searched for a vertical scroll.
	minRun = 2
	// minComponent is the smallest group of connected damaged tiles that is
	// searched for a 2D move.
	minComponent = 4
	// moveSearch is how far, in pixels per axis, the 2D move search looks.
	moveSearch = 256
	// needlePixels is the length of the row segments located by the 2D
	// search (60 bytes, within the vectorized range of bytes.Index).
	needlePixels = 15
	// patchRows is the height of the patch that verifies a needle hit.
	patchRows = 8
)

// moves finds scrolled and moved content among the damaged tiles, applies
// the resulting copies to the shadow (as the techie will) and recomputes
// e.dirty. It returns the OpCopy ops in the order they must be applied.
func (e *Encoder) moves(src pixSource, now time.Time) []screen.Rect {
	var ops []screen.Rect
	// Vertical shifts per column band (scrolling).
	cands := e.verticalCandidates(src)
	dys := make([]int, 0, len(cands))
	for dy := range cands {
		dys = append(dys, dy)
	}
	slices.SortFunc(dys, func(a, b int) int { return len(cands[b]) - len(cands[a]) })
	for _, dy := range dys {
		ops = e.applyCopies(ops, src, cands[dy], 0, dy, now)
	}
	if len(ops) > 0 {
		e.rediff(src)
	}
	// Remaining damage: 2D shifts (horizontal scrolling, window drags).
	n := len(ops)
	for _, comp := range e.components(minComponent) {
		if dx, dy, ok := e.findShift(src, comp); ok {
			ops = e.applyCopies(ops, src, comp, dx, dy, now)
		}
	}
	if len(ops) > n {
		e.rediff(src)
	}
	return ops
}

// rediff recomputes e.dirty for the tiles that were dirty or written by a
// copy; all other tiles are untouched and still equal.
func (e *Encoder) rediff(src pixSource) {
	e.parallel(len(e.dirty), func(t int) {
		if e.dirty[t] || e.touched[t] {
			e.dirty[t] = e.tileDiffers(src, t)
			e.touched[t] = false
		}
	})
}

// isFlat reports whether all pixels of a row segment are equal; such rows
// match anywhere and carry no information about a shift.
func isFlat(seg []byte) bool {
	return len(seg) <= 4 || bytes.Equal(seg[4:], seg[:len(seg)-4])
}

// verticalCandidates looks for vertical scrolls: for every column band with
// a vertical run of damaged tiles, the rows of the run in the capture are
// hashed and looked up among the hashed rows of the same band in the
// shadow. The shift with the most matching rows wins if enough rows agree.
// The result maps a shift dy (content moved down by dy) to the tiles of the
// runs that voted for it.
func (e *Encoder) verticalCandidates(src pixSource) map[int][]int {
	type vote struct {
		dy    int
		tiles []int
	}
	results := make([][]vote, e.tilesX)
	e.parallel(e.tilesX, func(tx int) {
		bx0, bx1 := tx*TileSize, min((tx+1)*TileSize, e.w)
		if bx1-bx0 < 16 {
			return
		}
		type run struct{ ty0, ty1 int }
		var runs []run
		for ty := 0; ty < e.tilesY; {
			if !e.dirty[ty*e.tilesX+tx] {
				ty++
				continue
			}
			ty0 := ty
			for ty < e.tilesY && e.dirty[ty*e.tilesX+tx] {
				ty++
			}
			if ty-ty0 >= minRun {
				runs = append(runs, run{ty0, ty})
			}
		}
		if len(runs) == 0 {
			return
		}
		rows := make(map[uint64]int32, e.h)
		for y := range e.h {
			seg := e.shadowRow(y, bx0, bx1)
			if isFlat(seg) {
				continue
			}
			hv := maphash.Bytes(e.seed, seg)
			if _, dup := rows[hv]; dup {
				rows[hv] = -1
			} else {
				rows[hv] = int32(y)
			}
		}
		for _, r := range runs {
			votes := map[int]int{}
			nonFlat := 0
			for y := r.ty0 * TileSize; y < min(r.ty1*TileSize, e.h); y++ {
				seg := src.row(y, bx0, bx1)
				if isFlat(seg) {
					continue
				}
				nonFlat++
				if ys, ok := rows[maphash.Bytes(e.seed, seg)]; ok && ys >= 0 && int(ys) != y {
					votes[y-int(ys)]++
				}
			}
			best, bestN := 0, 0
			for dy, n := range votes {
				if n > bestN || n == bestN && abs(dy) < abs(best) {
					best, bestN = dy, n
				}
			}
			if bestN >= max(8, nonFlat/4) {
				v := vote{dy: best}
				for ty := r.ty0; ty < r.ty1; ty++ {
					v.tiles = append(v.tiles, ty*e.tilesX+tx)
				}
				results[tx] = append(results[tx], v)
			}
		}
	})
	out := map[int][]int{}
	for _, vs := range results {
		for _, v := range vs {
			out[v.dy] = append(out[v.dy], v.tiles...)
		}
	}
	return out
}

// components returns the 4-connected groups of damaged tiles with at least
// minSize tiles, each sorted in row-major order.
func (e *Encoder) components(minSize int) [][]int {
	seen := make([]bool, len(e.dirty))
	var out [][]int
	var stack []int
	for t, d := range e.dirty {
		if !d || seen[t] {
			continue
		}
		var comp []int
		stack = append(stack[:0], t)
		seen[t] = true
		for len(stack) > 0 {
			c := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			comp = append(comp, c)
			tx, ty := c%e.tilesX, c/e.tilesX
			for _, nb := range [4][2]int{{tx - 1, ty}, {tx + 1, ty}, {tx, ty - 1}, {tx, ty + 1}} {
				if nb[0] < 0 || nb[1] < 0 || nb[0] >= e.tilesX || nb[1] >= e.tilesY {
					continue
				}
				n := nb[1]*e.tilesX + nb[0]
				if e.dirty[n] && !seen[n] {
					seen[n] = true
					stack = append(stack, n)
				}
			}
		}
		if len(comp) >= minSize {
			slices.Sort(comp)
			out = append(out, comp)
		}
	}
	return out
}

// transitions counts adjacent pixel pairs that differ in a row segment.
func transitions(seg []byte) int {
	n := 0
	for i := 4; i+4 <= len(seg); i += 4 {
		if binary.LittleEndian.Uint32(seg[i:]) != binary.LittleEndian.Uint32(seg[i-4:]) {
			n++
		}
	}
	return n
}

// findShift looks for one 2D shift of a group of damaged tiles: short,
// textured row segments of the capture are located in the shadow within
// moveSearch pixels, and the shift found by most samples wins.
func (e *Encoder) findShift(src pixSource, comp []int) (dx, dy int, ok bool) {
	type point struct{ x, y int }
	var samples []point
	step := max(1, len(comp)/12)
	for i := 0; i < len(comp) && len(samples) < 8; i += step {
		r := e.tileRect(comp[i])
	search:
		for _, oy := range [...]int{32, 16, 48, 8, 56, 24, 40} {
			y := r.Min.Y + oy
			if y >= r.Max.Y {
				continue
			}
			for _, ox := range [...]int{0, 24, 48} {
				x := r.Min.X + ox
				if x+needlePixels > r.Max.X {
					continue
				}
				if transitions(src.row(y, x, x+needlePixels)) >= 3 {
					samples = append(samples, point{x, y})
					break search
				}
			}
		}
	}
	if len(samples) < 2 {
		return 0, 0, false
	}
	type shift struct{ dx, dy int }
	found := make([][]shift, len(samples))
	e.parallel(len(samples), func(i int) {
		p := samples[i]
		needle := src.row(p.y, p.x, p.x+needlePixels)
		hx0, hx1 := max(0, p.x-moveSearch), min(e.w, p.x+needlePixels+moveSearch)
		var hits []shift
		for sy := max(0, p.y-moveSearch); sy <= min(e.h-1, p.y+moveSearch); sy++ {
			hay := e.shadowRow(sy, hx0, hx1)
			for off := 0; ; {
				j := bytes.Index(hay[off:], needle)
				if j < 0 {
					break
				}
				pos := off + j
				off = pos + 1
				if pos%4 != 0 {
					continue
				}
				s := shift{p.x - (hx0 + pos/4), p.y - sy}
				if !e.patchMatches(src, p.x, p.y, s.dx, s.dy) {
					continue
				}
				if s.dx == 0 && s.dy == 0 || len(hits) == 4 {
					// Static content or too ambiguous to vote.
					found[i] = nil
					return
				}
				hits = append(hits, s)
			}
		}
		found[i] = hits
	})
	votes := map[shift]int{}
	valid := 0
	for _, hits := range found {
		if len(hits) > 0 {
			valid++
		}
		for _, s := range hits {
			votes[s]++
		}
	}
	var best shift
	bestN := 0
	for s, n := range votes {
		if n > bestN || n == bestN && abs(s.dx)+abs(s.dy) < abs(best.dx)+abs(best.dy) {
			best, bestN = s, n
		}
	}
	if bestN < 2 || bestN*3 < valid {
		return 0, 0, false
	}
	return best.dx, best.dy, true
}

// patchMatches reports whether the needlePixels x patchRows patch at (x, y)
// in the capture (clipped to the screen) equals the shadow shifted by
// (dx, dy); the first row is known to match. A single row segment of text
// or UI often occurs many times; a patch rarely does.
func (e *Encoder) patchMatches(src pixSource, x, y, dx, dy int) bool {
	for yy := y + 1; yy < min(y+patchRows, e.h, e.h+dy); yy++ {
		if yy-dy < 0 || !e.rowMatch(src, yy, x, x+needlePixels, dx, dy) {
			return false
		}
	}
	return true
}

// rowMatch reports whether pixels x0..x1 of row y of the capture equal the
// shadow shifted by (dx, dy). The caller checks the bounds.
func (e *Encoder) rowMatch(src pixSource, y, x0, x1, dx, dy int) bool {
	return bytes.Equal(src.row(y, x0, x1), e.shadowRow(y-dy, x0-dx, x1-dx))
}

// colMatch is rowMatch for the pixels y0..y1 of column x.
func (e *Encoder) colMatch(src pixSource, x, y0, y1, dx, dy int) bool {
	for y := y0; y < y1; y++ {
		if !bytes.Equal(src.row(y, x, x+1), e.shadowRow(y-dy, x-dx, x-dx+1)) {
			return false
		}
	}
	return true
}

// inside reports whether r and its source r - (dx, dy) lie on the screen.
func (e *Encoder) inside(r image.Rectangle, dx, dy int) bool {
	scr := image.Rect(0, 0, e.w, e.h)
	return r.In(scr) && r.Sub(image.Pt(dx, dy)).In(scr)
}

// rectMatches reports whether all of r in the capture equals the shadow
// shifted by (dx, dy).
func (e *Encoder) rectMatches(src pixSource, r image.Rectangle, dx, dy int) bool {
	if r.Empty() || !e.inside(r, dx, dy) {
		return false
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		if !e.rowMatch(src, y, r.Min.X, r.Max.X, dx, dy) {
			return false
		}
	}
	return true
}

// applyCopies turns the tiles that fully match the shadow shifted by
// (dx, dy) into as few rectangles as possible, grows each by up to a tile
// minus one pixel on every side while it keeps matching, and applies them
// one by one. Each rectangle is verified against the shadow as it is at
// that point, so every emitted copy leaves exactly the captured pixels.
func (e *Encoder) applyCopies(ops []screen.Rect, src pixSource, tiles []int, dx, dy int, now time.Time) []screen.Rect {
	match := make([]bool, len(tiles))
	e.parallel(len(tiles), func(i int) {
		match[i] = e.rectMatches(src, e.tileRect(tiles[i]), dx, dy)
	})
	set := map[int]bool{}
	for i, t := range tiles {
		if match[i] {
			set[t] = true
		}
	}
	if len(set) == 0 {
		return ops
	}
	// Horizontal runs per tile row, merged with the identical run of the
	// row above.
	type span struct{ tx0, tx1 int }
	var rects []image.Rectangle
	open := map[span]int{} // run -> index in rects, for runs ending at the previous row
	for ty := range e.tilesY {
		next := map[span]int{}
		for tx := 0; tx < e.tilesX; {
			if !set[ty*e.tilesX+tx] {
				tx++
				continue
			}
			tx0 := tx
			for tx < e.tilesX && set[ty*e.tilesX+tx] {
				tx++
			}
			s := span{tx0, tx}
			r := image.Rect(tx0*TileSize, ty*TileSize, min(tx*TileSize, e.w), min((ty+1)*TileSize, e.h))
			if i, ok := open[s]; ok {
				rects[i].Max.Y = r.Max.Y
				next[s] = i
			} else {
				next[s] = len(rects)
				rects = append(rects, r)
			}
		}
		open = next
	}
	for i, r := range rects {
		rects[i] = e.grow(src, r, dx, dy)
	}
	// Order so that a copy doesn't overwrite the source of a later one
	// where that can be avoided, like memmove.
	slices.SortFunc(rects, func(a, b image.Rectangle) int {
		switch {
		case dy > 0 && a.Min.Y != b.Min.Y:
			return b.Min.Y - a.Min.Y
		case dy < 0 && a.Min.Y != b.Min.Y:
			return a.Min.Y - b.Min.Y
		case dx > 0:
			return b.Min.X - a.Min.X
		default:
			return a.Min.X - b.Min.X
		}
	})
	for _, r := range rects {
		if !e.rectMatches(src, r, dx, dy) {
			continue
		}
		ops = append(ops, e.applyCopy(r, dx, dy, now))
	}
	return ops
}

// grow extends r by up to TileSize-1 pixels per side while the added rows
// and columns still match the shifted shadow.
func (e *Encoder) grow(src pixSource, r image.Rectangle, dx, dy int) image.Rectangle {
	for i := 0; i < TileSize-1 && r.Min.Y > 0 && r.Min.Y-1-dy >= 0 && r.Min.Y-1-dy < e.h &&
		e.rowMatch(src, r.Min.Y-1, r.Min.X, r.Max.X, dx, dy); i++ {
		r.Min.Y--
	}
	for i := 0; i < TileSize-1 && r.Max.Y < e.h && r.Max.Y-dy >= 0 && r.Max.Y-dy < e.h &&
		e.rowMatch(src, r.Max.Y, r.Min.X, r.Max.X, dx, dy); i++ {
		r.Max.Y++
	}
	for i := 0; i < TileSize-1 && r.Min.X > 0 && r.Min.X-1-dx >= 0 && r.Min.X-1-dx < e.w &&
		e.colMatch(src, r.Min.X-1, r.Min.Y, r.Max.Y, dx, dy); i++ {
		r.Min.X--
	}
	for i := 0; i < TileSize-1 && r.Max.X < e.w && r.Max.X-dx >= 0 && r.Max.X-dx < e.w &&
		e.colMatch(src, r.Max.X, r.Min.Y, r.Max.Y, dx, dy); i++ {
		r.Max.X++
	}
	return r
}

// applyCopy copies the shadow rectangle r - (dx, dy) to r with memmove
// semantics, as the techie does for OpCopy, carries the lossy flags of the
// source tiles to the destination tiles, and returns the op.
func (e *Encoder) applyCopy(r image.Rectangle, dx, dy int, now time.Time) screen.Rect {
	d := image.Pt(dx, dy)
	s := r.Sub(d)
	type flag struct {
		t     int
		lossy bool
	}
	var flags []flag
	tx0, tx1 := r.Min.X/TileSize, (r.Max.X-1)/TileSize
	ty0, ty1 := r.Min.Y/TileSize, (r.Max.Y-1)/TileSize
	for ty := ty0; ty <= ty1; ty++ {
		for tx := tx0; tx <= tx1; tx++ {
			t := ty*e.tilesX + tx
			tr := e.tileRect(t)
			part := tr.Intersect(r)
			if e.anyLossy(part.Sub(d)) {
				flags = append(flags, flag{t, true})
			} else if part == tr {
				flags = append(flags, flag{t, false})
			}
			e.touched[t] = true
		}
	}
	for _, f := range flags {
		e.lossy[f.t] = f.lossy
		if f.lossy {
			e.refineAt[f.t] = now.Add(e.refineDelay())
		}
	}
	if dy > 0 {
		for y := r.Max.Y - 1; y >= r.Min.Y; y-- {
			copy(e.shadowRow(y, r.Min.X, r.Max.X), e.shadowRow(y-dy, s.Min.X, s.Max.X))
		}
	} else {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			copy(e.shadowRow(y, r.Min.X, r.Max.X), e.shadowRow(y-dy, s.Min.X, s.Max.X))
		}
	}
	e.stats.Copy++
	return screen.Rect{Kind: screen.OpCopy, X: r.Min.X, Y: r.Min.Y, W: r.Dx(), H: r.Dy(), SrcX: s.Min.X, SrcY: s.Min.Y}
}

// anyLossy reports whether a tile overlapping r is lossy.
func (e *Encoder) anyLossy(r image.Rectangle) bool {
	if r.Empty() {
		return false
	}
	for ty := r.Min.Y / TileSize; ty <= (r.Max.Y-1)/TileSize; ty++ {
		for tx := r.Min.X / TileSize; tx <= (r.Max.X-1)/TileSize; tx++ {
			if e.lossy[ty*e.tilesX+tx] {
				return true
			}
		}
	}
	return false
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
