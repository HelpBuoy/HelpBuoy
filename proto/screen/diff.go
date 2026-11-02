package screen

import (
	"bytes"
	"image"
)

// TileSize is the edge length of the tiles Diff compares and returns.
const TileSize = 64

// Diff compares prev and cur (same bounds) in 64x64 tiles and returns the
// changed tiles as Rects with copied pixels. A nil prev returns the full image.
// A prev with other bounds than cur is treated like nil. Tiles at the right
// and bottom edges are smaller when the size is not a multiple of TileSize.
// Rects are relative to cur.Bounds().Min and ordered top to bottom, left to
// right; the pixels of all rects share one allocation.
func Diff(prev, cur *image.RGBA) []Rect {
	if cur == nil {
		return nil
	}
	w, h := cur.Rect.Dx(), cur.Rect.Dy()
	if w <= 0 || h <= 0 {
		return nil
	}
	full := prev == nil || prev.Rect.Dx() != w || prev.Rect.Dy() != h
	tilesX := (w + TileSize - 1) / TileSize
	rowBytes := w * 4

	type tile struct{ x, y, w, h int }
	var dirty []tile
	mark := make([]bool, tilesX)
	total := 0
	for ty := 0; ty < h; ty += TileSize {
		th := min(TileSize, h-ty)
		n := 0
		for i := range mark {
			mark[i] = full
		}
		if full {
			n = tilesX
		} else {
			for y := ty; y < ty+th && n < tilesX; y++ {
				pr := prev.Pix[y*prev.Stride : y*prev.Stride+rowBytes]
				cr := cur.Pix[y*cur.Stride : y*cur.Stride+rowBytes]
				// bytes.Equal compares word-wise in assembly, so checking a
				// whole row first makes unchanged rows nearly free.
				if bytes.Equal(pr, cr) {
					continue
				}
				for tx := range mark {
					if mark[tx] {
						continue
					}
					a := tx * TileSize * 4
					b := min(a+TileSize*4, rowBytes)
					if !bytes.Equal(pr[a:b], cr[a:b]) {
						mark[tx] = true
						n++
					}
				}
			}
		}
		if n == 0 {
			continue
		}
		for tx, m := range mark {
			if !m {
				continue
			}
			x := tx * TileSize
			tw := min(TileSize, w-x)
			dirty = append(dirty, tile{x, ty, tw, th})
			total += tw * th * 4
		}
	}
	if len(dirty) == 0 {
		return nil
	}

	buf := make([]byte, total)
	rects := make([]Rect, len(dirty))
	for i, t := range dirty {
		rowLen := t.w * 4
		px := buf[: t.h*rowLen : t.h*rowLen]
		buf = buf[t.h*rowLen:]
		off := t.y*cur.Stride + t.x*4
		for y := 0; y < t.h; y++ {
			copy(px[y*rowLen:(y+1)*rowLen], cur.Pix[off:off+rowLen])
			off += cur.Stride
		}
		rects[i] = Rect{X: t.x, Y: t.y, W: t.w, H: t.h, RGBA: px}
	}
	return rects
}
