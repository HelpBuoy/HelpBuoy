package session

import "image"

// Upload planning. Every rl.UpdateTextureRec is one glTexSubImage2D call
// plus, for rects narrower than the image, a copy into tightly packed rows,
// so many tiny uploads cost more than a few slightly larger ones. PlanUpload
// snaps the dirty rects of one render frame to a grid of UploadCell pixels,
// joins neighbouring cells into rects, and falls back to coarser plans when
// that still leaves too many calls or most of the screen is dirty.
const (
	// UploadCell is the grid size dirty rects are rounded to; it matches the
	// encoder's 64x64 tiles, so tile-aligned damage is not enlarged.
	UploadCell = 64
	// MaxUploads is the most texture updates PlanUpload returns.
	MaxUploads = 48
	// FullUploadPercent: when the planned rects cover at least this share of
	// the screen, one full upload (contiguous rows, no copy) is cheaper.
	FullUploadPercent = 50
)

// run is a rectangle of grid cells: columns x0..x1-1, rows y0..y1-1.
type run struct{ x0, x1, y0, y1 int }

// PlanUpload returns the rects to upload for the dirty rects rs within
// bounds: at most MaxUploads rects that together cover every dirty pixel,
// or just bounds when that is about as cheap.
func PlanUpload(rs []image.Rectangle, bounds image.Rectangle) []image.Rectangle {
	if bounds.Empty() {
		return nil
	}
	cols := (bounds.Dx() + UploadCell - 1) / UploadCell
	rows := (bounds.Dy() + UploadCell - 1) / UploadCell
	grid := make([]bool, cols*rows)
	dirty := false
	for _, r := range rs {
		r = r.Intersect(bounds)
		if r.Empty() {
			continue
		}
		dirty = true
		x0, y0 := (r.Min.X-bounds.Min.X)/UploadCell, (r.Min.Y-bounds.Min.Y)/UploadCell
		x1, y1 := (r.Max.X-bounds.Min.X+UploadCell-1)/UploadCell, (r.Max.Y-bounds.Min.Y+UploadCell-1)/UploadCell
		for y := y0; y < y1; y++ {
			row := grid[y*cols : (y+1)*cols]
			for x := x0; x < x1; x++ {
				row[x] = true
			}
		}
	}
	if !dirty {
		return nil
	}
	cell := func(x0, y0, x1, y1 int) image.Rectangle {
		r := image.Rect(x0*UploadCell, y0*UploadCell, x1*UploadCell, y1*UploadCell).Add(bounds.Min)
		return r.Intersect(bounds)
	}
	full := []image.Rectangle{bounds}

	// Runs of dirty cells per grid row, merged with an identical run in the
	// row above.
	var done, open []run
	marked := 0
	for y := 0; y < rows; y++ {
		var next []run
		row := grid[y*cols : (y+1)*cols]
		for x := 0; x < cols; {
			if !row[x] {
				x++
				continue
			}
			x0 := x
			for x < cols && row[x] {
				x++
			}
			marked += x - x0
			r := run{x0, x, y, y + 1}
			for i, o := range open {
				if o.x0 == r.x0 && o.x1 == r.x1 {
					r.y0 = o.y0
					open[i].y1 = -1 // continued
					break
				}
			}
			next = append(next, r)
		}
		for _, o := range open {
			if o.y1 >= 0 {
				done = append(done, o)
			}
		}
		open = next
	}
	done = append(done, open...)
	if marked*100 >= FullUploadPercent*cols*rows {
		return full
	}
	if len(done) <= MaxUploads {
		out := make([]image.Rectangle, len(done))
		for i, r := range done {
			out[i] = cell(r.x0, r.y0, r.x1, r.y1)
		}
		return out
	}

	// Too many rects: one span per grid row, from its first to its last
	// dirty cell, with equal spans of consecutive rows joined.
	var bands []run
	area := 0
	for y := 0; y < rows; y++ {
		row := grid[y*cols : (y+1)*cols]
		x0, x1 := -1, -1
		for x, d := range row {
			if d {
				if x0 < 0 {
					x0 = x
				}
				x1 = x + 1
			}
		}
		if x0 < 0 {
			continue
		}
		area += x1 - x0
		if n := len(bands); n > 0 && bands[n-1].y1 == y && bands[n-1].x0 == x0 && bands[n-1].x1 == x1 {
			bands[n-1].y1 = y + 1
			continue
		}
		bands = append(bands, run{x0, x1, y, y + 1})
	}
	if area*100 >= FullUploadPercent*cols*rows || len(bands) > MaxUploads {
		if len(bands) > MaxUploads {
			// Join bands into groups of whole rows until few enough.
			return groupBands(bands, cell)
		}
		return full
	}
	out := make([]image.Rectangle, len(bands))
	for i, b := range bands {
		out[i] = cell(b.x0, b.y0, b.x1, b.y1)
	}
	return out
}

// groupBands merges consecutive bands (sorted by row) pairwise until at most
// MaxUploads remain; each merge takes the union of the two.
func groupBands(bands []run, cell func(x0, y0, x1, y1 int) image.Rectangle) []image.Rectangle {
	for len(bands) > MaxUploads {
		n := 0
		for i := 0; i < len(bands); i += 2 {
			b := bands[i]
			if i+1 < len(bands) {
				c := bands[i+1]
				b.x0, b.x1, b.y1 = min(b.x0, c.x0), max(b.x1, c.x1), c.y1
			}
			bands[n] = b
			n++
		}
		bands = bands[:n]
	}
	out := make([]image.Rectangle, len(bands))
	for i, b := range bands {
		out[i] = cell(b.x0, b.y0, b.x1, b.y1)
	}
	return out
}

func union(rs []image.Rectangle) image.Rectangle {
	u := rs[0]
	for _, r := range rs[1:] {
		u = u.Union(r)
	}
	return u
}

// SubImageBytes returns the pixels of r in img as tightly packed RGBA rows,
// using buf as scratch space if r is narrower than the image. It is what a
// texture upload of r needs.
func SubImageBytes(img *image.RGBA, r image.Rectangle, buf []byte) []byte {
	r = r.Intersect(img.Rect)
	if r.Empty() {
		return nil
	}
	row := r.Dx() * 4
	if r.Dx() == img.Rect.Dx() && img.Stride == row {
		off := img.PixOffset(r.Min.X, r.Min.Y)
		return img.Pix[off : off+row*r.Dy()]
	}
	n := row * r.Dy()
	if cap(buf) < n {
		buf = make([]byte, n)
	}
	buf = buf[:n]
	for y := 0; y < r.Dy(); y++ {
		off := img.PixOffset(r.Min.X, r.Min.Y+y)
		copy(buf[y*row:(y+1)*row], img.Pix[off:off+row])
	}
	return buf
}
