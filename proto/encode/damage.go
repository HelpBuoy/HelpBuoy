package encode

import (
	"bytes"
	"encoding/binary"
	"image"
)

// damage marks in e.dirty every tile where src differs from the shadow and
// returns how many there are. Rows are compared whole first, so unchanged
// rows cost one memory comparison each; only differing rows are compared
// per tile, and tiles already known dirty are skipped.
func (e *Encoder) damage(src pixSource) int {
	counts := make([]int, e.tilesY)
	e.parallel(e.tilesY, func(ty int) {
		y0, y1 := ty*TileSize, min((ty+1)*TileSize, e.h)
		row := e.dirty[ty*e.tilesX : (ty+1)*e.tilesX]
		clear(row)
		n := 0
		for y := y0; y < y1 && n < e.tilesX; y++ {
			c, s := src.row(y, 0, e.w), e.shadowRow(y, 0, e.w)
			if bytes.Equal(c, s) {
				continue
			}
			for tx := range row {
				if row[tx] {
					continue
				}
				a, b := tx*TileSize*4, min((tx+1)*TileSize, e.w)*4
				if !bytes.Equal(c[a:b], s[a:b]) {
					row[tx] = true
					n++
				}
			}
		}
		counts[ty] = n
	})
	total := 0
	for _, n := range counts {
		total += n
	}
	return total
}

// tileDiffers reports whether tile t of src differs from the shadow.
func (e *Encoder) tileDiffers(src pixSource, t int) bool {
	r := e.tileRect(t)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		if !bytes.Equal(src.row(y, r.Min.X, r.Max.X), e.shadowRow(y, r.Min.X, r.Max.X)) {
			return true
		}
	}
	return false
}

// diffBounds returns the smallest rectangle within tile t outside which src
// equals the shadow (empty if the tile is unchanged).
func (e *Encoder) diffBounds(src pixSource, t int) image.Rectangle {
	r := e.tileRect(t)
	out := image.Rectangle{}
	found := false
	minX, maxX := r.Max.X, r.Min.X
	for y := r.Min.Y; y < r.Max.Y; y++ {
		c, s := src.row(y, r.Min.X, r.Max.X), e.shadowRow(y, r.Min.X, r.Max.X)
		if bytes.Equal(c, s) {
			continue
		}
		if !found {
			out.Min.Y = y
			found = true
		}
		out.Max.Y = y + 1
		n := len(c) / 4
		first := 0
		for first < n && binary.LittleEndian.Uint32(c[first*4:]) == binary.LittleEndian.Uint32(s[first*4:]) {
			first++
		}
		last := n - 1
		for last > first && binary.LittleEndian.Uint32(c[last*4:]) == binary.LittleEndian.Uint32(s[last*4:]) {
			last--
		}
		minX = min(minX, r.Min.X+first)
		maxX = max(maxX, r.Min.X+last+1)
	}
	if !found {
		return image.Rectangle{}
	}
	out.Min.X, out.Max.X = minX, maxX
	return out
}

// storeShadow copies rectangle r of src into the shadow.
func (e *Encoder) storeShadow(src pixSource, r image.Rectangle) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		copy(e.shadowRow(y, r.Min.X, r.Max.X), src.row(y, r.Min.X, r.Max.X))
	}
}
