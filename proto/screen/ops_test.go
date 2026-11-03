package screen

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math/rand/v2"
	"reflect"
	"runtime"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// textTile returns w x h pixels that look like anti-aliased text on a light
// background: a handful of colors in short runs.
func textTile(w, h int, seed uint64) []byte {
	rng := rand.New(rand.NewPCG(seed, 1))
	px := make([]byte, w*h*4)
	bg := [4]byte{250, 250, 250, 255}
	shades := [][4]byte{{20, 20, 20, 255}, {90, 90, 90, 255}, {160, 160, 160, 255}, {200, 210, 220, 255}, {30, 80, 200, 255}, {120, 150, 230, 255}}
	for i := 0; i < w*h; i++ {
		copy(px[i*4:], bg[:])
	}
	for y := 2; y+10 < h; y += 14 {
		for x := 1; x+6 < w; x += 7 {
			if rng.IntN(5) == 0 {
				continue // a space
			}
			for k := 0; k < 18; k++ {
				gx, gy := x+rng.IntN(5), y+rng.IntN(10)
				c := shades[rng.IntN(len(shades))]
				copy(px[(gy*w+gx)*4:], c[:])
			}
		}
	}
	return px
}

// photoTile returns w x h pixels with smooth gradients and noise: thousands
// of colors.
func photoTile(w, h int, seed uint64) []byte {
	rng := rand.New(rand.NewPCG(seed, 2))
	px := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			px[i] = byte(x*3 + y + rng.IntN(12))
			px[i+1] = byte(y*2 + 40 + rng.IntN(12))
			px[i+2] = byte(200 - x - y/2 + rng.IntN(12))
			px[i+3] = 255
		}
	}
	return px
}

func mustPalette(px []byte, w, h int) []byte {
	data, ok := EncodePalette(px, w*4, w, h)
	if !ok {
		panic("EncodePalette refused a tile")
	}
	return data
}

// sampleFrames returns frames with every op kind that fit a 64x48 image and
// use cache slots 1 and 2.
func sampleFrames() []Frame {
	return []Frame{
		{Seq: 1, Rects: []Rect{
			{X: 0, Y: 0, W: 64, H: 48, RGBA: photoTile(64, 48, 1)},
			{X: 4, Y: 4, W: 8, H: 8, Kind: OpSolid, Color: [4]byte{1, 2, 3, 255}, Store: true, Slot: 1},
		}},
		{Seq: 2, More: true, Rects: []Rect{
			{X: 16, Y: 0, W: 16, H: 16, Kind: OpPalette, Data: mustPalette(textTile(16, 16, 3), 16, 16), Store: true, Slot: 2},
			{X: 30, Y: 30, W: 16, H: 16, Kind: OpJPEG, Data: EncodeJPEG(photoTile(16, 16, 4), 64, 16, 16, 80)},
		}},
		{Seq: 2, Rects: []Rect{
			{X: 0, Y: 2, W: 40, H: 30, Kind: OpCopy, SrcX: 3, SrcY: 0},
			{X: 40, Y: 0, W: 8, H: 8, Kind: OpCacheRef, Slot: 1},
			{X: 48, Y: 32, W: 16, H: 16, Kind: OpCacheRef, Slot: 2},
			{X: 1, Y: 1, W: 2, H: 2, RGBA: make([]byte, 16), Store: true, Slot: CacheSlots - 1},
		}},
	}
}

func TestFrameRoundTripV2(t *testing.T) {
	frames := sampleFrames()
	frames = append(frames, Frame{Seq: 9, More: true}, Frame{Seq: 10, Rects: []Rect{
		{X: 1, Y: 2, W: 3, H: 4, Kind: OpRaw, RGBA: make([]byte, 48), Store: true, Slot: 7},
		{X: 9, Y: 9, W: 300, H: 200, Kind: OpJPEG, Data: []byte{1, 2, 3}, Store: true, Slot: 0},
		{X: 9, Y: 9, W: 1, H: 1, Kind: OpPalette, Data: []byte{}},
		{X: MaxDimension - 5, Y: 0, W: 5, H: MaxDimension, Kind: OpCopy, SrcX: 0, SrcY: 0},
	}})
	for _, f := range frames {
		b := EncodeFrame(f)
		want := frameHeader
		for _, r := range f.Rects {
			want += r.EncodedSize()
		}
		if len(b) != want {
			t.Fatalf("frame %d: %d bytes, EncodedSize says %d", f.Seq, len(b), want)
		}
		got, err := DecodeFrame(b)
		if err != nil {
			t.Fatalf("frame %d: %v", f.Seq, err)
		}
		for i := range got.Rects {
			// Empty data decodes as an empty, non-nil slice.
			if len(got.Rects[i].Data) == 0 && f.Rects[i].Data != nil {
				got.Rects[i].Data = f.Rects[i].Data
			}
		}
		if !reflect.DeepEqual(got, f) {
			t.Fatalf("frame %d mismatch:\n got %+v\nwant %+v", f.Seq, got, f)
		}
	}
	// Store is dropped where it cannot apply, and CacheRef keeps its slot.
	got, err := DecodeFrame(EncodeFrame(Frame{Rects: []Rect{{W: 1, H: 1, Kind: OpCacheRef, Slot: 5, Store: true}}}))
	if err != nil || got.Rects[0].Store || got.Rects[0].Slot != 5 {
		t.Fatalf("cacheref with store: %+v %v", got, err)
	}
}

func TestHelloMonitors(t *testing.T) {
	h := Hello{
		Hostname: "PC", UserMachine: "anna@PC", OS: "Windows 11", Width: 2560, Height: 1440,
		Monitors: []Monitor{
			{X: 0, Y: 0, W: 1920, H: 1080, Primary: true, Name: `\\.\DISPLAY1`},
			{X: -2560, Y: -200, W: 2560, H: 1440, Name: "Dell U2719D"},
		},
		Current: 1,
	}
	got, err := DecodeHello(EncodeHello(h))
	if err != nil || !reflect.DeepEqual(got, h) {
		t.Fatalf("got %+v %v", got, err)
	}
	many := Hello{Monitors: make([]Monitor, 20), Current: 19}
	many.Monitors[0].X = 1 << 40
	got, err = DecodeHello(EncodeHello(many))
	if err != nil || len(got.Monitors) != MaxMonitors || got.Current != 0 || got.Monitors[0].X != 1<<31-1 {
		t.Fatalf("clamping: %+v %v", got, err)
	}
	b := EncodeHello(h)
	for name, mut := range map[string]func([]byte) []byte{
		"short":       func(b []byte) []byte { return b[:len(b)-1] },
		"trailing":    func(b []byte) []byte { return append(b, 0) },
		"current":     func(b []byte) []byte { b[len(b)-1] = 2; return b },
		"count":       func(b []byte) []byte { b[bytes.Index(b, []byte{2, 0, 0, 0, 0})] = 17; return b },
		"no monitors": func(b []byte) []byte { return EncodeHello(Hello{Width: 1, Height: 1})[:8] },
	} {
		if _, err := DecodeHello(mut(bytes.Clone(b))); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestMessagesRoundTrip(t *testing.T) {
	cs := CursorShape{ID: 0xdeadbeef, HotX: 3, HotY: 31, W: 32, H: 32, RGBA: photoTile(32, 32, 9)}
	gotCS, err := DecodeCursorShape(EncodeCursorShape(cs))
	if err != nil || !reflect.DeepEqual(gotCS, cs) {
		t.Fatalf("cursor shape: %v", err)
	}
	empty, err := DecodeCursorShape(EncodeCursorShape(CursorShape{ID: 1, HotX: 5}))
	if err != nil || empty.W != 0 || empty.HotX != 0 || len(empty.RGBA) != 0 {
		t.Fatalf("empty cursor: %+v %v", empty, err)
	}
	big, err := DecodeCursorShape(EncodeCursorShape(CursorShape{W: 1000, H: 300, HotX: 999}))
	if err != nil || big.W != MaxCursor || big.H != MaxCursor || big.HotX != MaxCursor-1 {
		t.Fatalf("clamped cursor: %d %d %d %v", big.W, big.H, big.HotX, err)
	}
	for _, p := range []CursorPos{{}, {X: -100, Y: 1 << 20, ID: 7, Visible: true}} {
		got, err := DecodeCursorPos(EncodeCursorPos(p))
		if err != nil || got != p {
			t.Fatalf("cursor pos %+v: %+v %v", p, got, err)
		}
	}
	for i := 0; i < MaxMonitors; i++ {
		if got, err := DecodeSelectMonitor(EncodeSelectMonitor(SelectMonitor{Index: i})); err != nil || got.Index != i {
			t.Fatalf("select %d: %v %v", i, got, err)
		}
	}
	for _, m := range []QualityMode{QualityAuto, QualitySharp, QualityFast} {
		if got, err := DecodeQuality(EncodeQuality(Quality{Mode: m})); err != nil || got.Mode != m {
			t.Fatalf("quality %d: %v %v", m, got, err)
		}
	}

	bad := map[string]error{}
	_, bad["cursor short"] = DecodeCursorShape(EncodeCursorShape(cs)[:100])
	_, bad["cursor trailing"] = DecodeCursorShape(append(EncodeCursorShape(cs), 0))
	_, bad["cursor too big"] = DecodeCursorShape([]byte{0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 0, 1})
	_, bad["cursor hotspot"] = DecodeCursorShape([]byte{0, 0, 0, 0, 0, 1, 0, 0, 0, 1, 0, 1, 1, 2, 3, 4})
	_, bad["pos short"] = DecodeCursorPos(make([]byte, 12))
	_, bad["pos flags"] = DecodeCursorPos(append(make([]byte, 12), 2))
	_, bad["select empty"] = DecodeSelectMonitor(nil)
	_, bad["select range"] = DecodeSelectMonitor([]byte{MaxMonitors})
	_, bad["quality long"] = DecodeQuality([]byte{0, 0})
	_, bad["quality mode"] = DecodeQuality([]byte{3})
	for name, err := range bad {
		if !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestSolidColor(t *testing.T) {
	px := make([]byte, 7*5*4)
	for i := 0; i < len(px); i += 4 {
		copy(px[i:], []byte{9, 8, 7, 255})
	}
	if c, ok := SolidColor(px, 7*4, 7, 5); !ok || c != [4]byte{9, 8, 7, 255} {
		t.Fatalf("solid: %v %v", c, ok)
	}
	// A different pixel outside the region (stride padding) is ignored.
	if _, ok := SolidColor(px, 7*4, 6, 5); !ok {
		t.Fatal("sub-region not solid")
	}
	px[len(px)-1] = 0
	if _, ok := SolidColor(px, 7*4, 7, 5); ok {
		t.Fatal("last pixel differs but reported solid")
	}
	if _, ok := SolidColor(px, 7*4, 6, 5); !ok {
		t.Fatal("stride padding not ignored")
	}
	if _, ok := SolidColor(px, 7*4, 7, 6); ok {
		t.Fatal("short source accepted")
	}
	if _, ok := SolidColor(px, 4, 7, 5); ok {
		t.Fatal("bad stride accepted")
	}
}

func TestPaletteRoundTrip(t *testing.T) {
	for _, sz := range [][2]int{{1, 1}, {3, 5}, {64, 64}, {64, 17}, {200, 3}} {
		w, h := sz[0], sz[1]
		px := textTile(w, h, uint64(w*h))
		// Embed the tile in a wider image to exercise the stride.
		stride := (w + 3) * 4
		img := make([]byte, stride*h)
		for y := 0; y < h; y++ {
			copy(img[y*stride:], px[y*w*4:(y+1)*w*4])
		}
		data, ok := EncodePalette(img, stride, w, h)
		if !ok {
			t.Fatalf("%dx%d refused", w, h)
		}
		out := make([]byte, w*h*4)
		if err := decodePalette(data, w, h, out); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out, px) {
			t.Fatalf("%dx%d: pixels differ", w, h)
		}
		if err := decodePalette(data, w, h+1, make([]byte, w*(h+1)*4)); !errors.Is(err, ErrMalformed) {
			t.Fatalf("wrong size accepted: %v", err)
		}
	}
	// Exactly 256 colors are fine, 257 are not.
	px := make([]byte, 257*4)
	for i := 0; i < 257; i++ {
		binary.LittleEndian.PutUint32(px[i*4:], uint32(i)*0x01030507|0xff000000)
	}
	if _, ok := EncodePalette(px, 256*4, 256, 1); !ok {
		t.Fatal("256 colors refused")
	}
	if _, ok := EncodePalette(px, 257*4, 257, 1); ok {
		t.Fatal("257 colors accepted")
	}
	if _, ok := EncodePalette(px, 4, 2, 1); ok {
		t.Fatal("bad stride accepted")
	}
	data, _ := EncodePalette(px, 256*4, 256, 1)
	out := make([]byte, 256*4)
	if err := decodePalette(data, 256, 1, out); err != nil || !bytes.Equal(out, px[:256*4]) {
		t.Fatalf("256 colors: %v", err)
	}
}

func TestPaletteRejects(t *testing.T) {
	enc, _ := zstd.NewWriter(nil)
	z := func(b []byte) []byte { return enc.EncodeAll(b, nil) }
	out := make([]byte, 4*4)
	cases := map[string][]byte{
		"not zstd":    {1, 2, 3, 4},
		"no colors":   z([]byte{0, 0, 0, 0, 0, 0}),
		"index":       z([]byte{0, 1, 1, 2, 3, 4, 0, 0, 1, 0}),
		"short":       z([]byte{0, 1, 1, 2, 3, 4, 0, 0, 0}),
		"long":        z(append([]byte{0, 1, 1, 2, 3, 4}, make([]byte, 5000)...)),
		"many colors": z([]byte{1, 1}),
		"two frames":  append(z([]byte{0, 1, 1, 2, 3, 4, 0, 0, 0, 0}), z([]byte{0})...),
	}
	for name, data := range cases {
		if err := decodePalette(data, 2, 2, out); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if err := decodePalette(z([]byte{0, 1, 1, 2, 3, 4, 0, 0, 0, 0}), 2, 2, out); err != nil {
		t.Fatal(err)
	}
}

func TestJPEGRoundTrip(t *testing.T) {
	for _, q := range []int{-5, 50, 90, 1000} {
		px := photoTile(64, 48, 5)
		data := EncodeJPEG(px, 64*4, 64, 48, q)
		if data == nil {
			t.Fatal("no jpeg")
		}
		out := make([]byte, len(px))
		if err := decodeJPEG(data, 64, 48, out); err != nil {
			t.Fatal(err)
		}
		var sum, n int
		for i := range px {
			if i%4 == 3 {
				if out[i] != 255 {
					t.Fatal("alpha not 255")
				}
				continue
			}
			d := int(px[i]) - int(out[i])
			sum += d * d
			n++
		}
		mse := float64(sum) / float64(n)
		if q >= 50 && mse > 60 {
			t.Fatalf("quality %d: mse %.1f", q, mse)
		}
		if err := decodeJPEG(data, 64, 47, make([]byte, 64*47*4)); !errors.Is(err, ErrMalformed) {
			t.Fatalf("wrong size accepted: %v", err)
		}
	}
	if err := decodeJPEG([]byte{0xff, 0xd8, 0xff}, 1, 1, make([]byte, 4)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("garbage accepted: %v", err)
	}
	if EncodeJPEG(make([]byte, 10), 4, 2, 2, 50) != nil {
		t.Fatal("short source accepted")
	}
}

// model is a straightforward reference implementation of Decoder.Apply.
type model struct {
	img   *image.RGBA
	cache map[uint16]*image.RGBA
}

func (m *model) sub(x, y, w, h int) image.Rectangle {
	return image.Rect(x, y, x+w, y+h).Add(m.img.Rect.Min)
}

func (m *model) apply(t *testing.T, f Frame) {
	t.Helper()
	for _, r := range f.Rects {
		dr := m.sub(r.X, r.Y, r.W, r.H)
		var src image.Image
		switch r.Kind {
		case OpRaw:
			src = &image.RGBA{Pix: r.RGBA, Stride: r.W * 4, Rect: image.Rect(0, 0, r.W, r.H)}
		case OpSolid:
			src = image.NewUniform(color.RGBA{r.Color[0], r.Color[1], r.Color[2], r.Color[3]})
		case OpPalette:
			// Decoded independently: zstd plus a per-pixel lookup.
			raw, err := zstdDecoder().DecodeAll(r.Data, make([]byte, 0, 1<<20))
			if err != nil {
				t.Fatal(err)
			}
			n := int(binary.BigEndian.Uint16(raw))
			p := image.NewRGBA(image.Rect(0, 0, r.W, r.H))
			for i, v := range raw[2+n*4:] {
				copy(p.Pix[i*4:i*4+4], raw[2+int(v)*4:])
			}
			src = p
		case OpJPEG:
			j, err := jpeg.Decode(bytes.NewReader(r.Data))
			if err != nil {
				t.Fatal(err)
			}
			src = j
		case OpCopy:
			snap := image.NewRGBA(image.Rect(0, 0, r.W, r.H))
			draw.Draw(snap, snap.Rect, m.img, m.sub(r.SrcX, r.SrcY, r.W, r.H).Min, draw.Src)
			src = snap
		case OpCacheRef:
			src = m.cache[r.Slot]
		}
		sp := image.Point{}
		if b := src.Bounds(); !b.Empty() {
			sp = b.Min
		}
		draw.Draw(m.img, dr, src, sp, draw.Src)
		if r.Store {
			c := image.NewRGBA(image.Rect(0, 0, r.W, r.H))
			draw.Draw(c, c.Rect, m.img, dr.Min, draw.Src)
			m.cache[r.Slot] = c
		}
	}
}

// script returns frames for a 200x150 image that use every op kind, the
// cache within and across frames, and overlapping copies in all directions.
func script() []Frame {
	tile := func(x, y, w, h int, kind OpKind, seed uint64) Rect {
		r := Rect{X: x, Y: y, W: w, H: h, Kind: kind}
		switch kind {
		case OpRaw:
			r.RGBA = photoTile(w, h, seed)
		case OpPalette:
			r.Data = mustPalette(textTile(w, h, seed), w, h)
		case OpJPEG:
			r.Data = EncodeJPEG(photoTile(w, h, seed), w*4, w, h, 75)
		}
		return r
	}
	stored := func(r Rect, slot uint16) Rect { r.Store, r.Slot = true, slot; return r }
	// Enough overlapping tiles for parallel decoding, mixed with ops that
	// depend on their order.
	many := Frame{Seq: 4}
	for i := 0; i < 24; i++ {
		kind := OpPalette
		if i%3 == 0 {
			kind = OpJPEG
		}
		r := tile(i*37%150, i*23%100, 50, 50, kind, uint64(10+i))
		if i%5 == 0 {
			r = stored(r, uint16(100+i))
		}
		many.Rects = append(many.Rects, r)
		if i%4 == 3 {
			many.Rects = append(many.Rects, Rect{X: 0, Y: 0, W: 150, H: 100, Kind: OpCopy, SrcX: 3 + i, SrcY: i})
		}
		if i%5 == 1 {
			many.Rects = append(many.Rects, Rect{X: 150, Y: 100, W: 50, H: 50, Kind: OpCacheRef, Slot: uint16(99 + i)})
		}
	}
	return []Frame{
		{Seq: 1, Rects: []Rect{
			tile(0, 0, 200, 150, OpRaw, 1),
			stored(tile(10, 10, 64, 64, OpRaw, 2), 3),
			stored(Rect{X: 100, Y: 5, W: 30, H: 20, Kind: OpSolid, Color: [4]byte{10, 200, 30, 255}}, 4),
			stored(tile(0, 64, 64, 64, OpPalette, 3), 5),
			stored(tile(64, 64, 64, 64, OpJPEG, 4), 6),
			tile(136, 86, 64, 64, OpPalette, 5),
		}},
		{Seq: 2, Rects: []Rect{
			{X: 130, Y: 80, W: 64, H: 64, Kind: OpCacheRef, Slot: 3},
			{X: 0, Y: 10, W: 100, H: 50, Kind: OpCopy, SrcX: 0, SrcY: 0},     // down
			{X: 0, Y: 5, W: 100, H: 50, Kind: OpCopy, SrcX: 0, SrcY: 20},     // up
			{X: 15, Y: 0, W: 80, H: 60, Kind: OpCopy, SrcX: 10, SrcY: 0},     // right
			{X: 100, Y: 70, W: 90, H: 60, Kind: OpCopy, SrcX: 107, SrcY: 70}, // left
			{X: 20, Y: 30, W: 50, H: 40, Kind: OpCopy, SrcX: 25, SrcY: 33},   // up-left
			{X: 0, Y: 0, W: 64, H: 64, Kind: OpCacheRef, Slot: 6},
			{X: 170, Y: 0, W: 30, H: 20, Kind: OpCacheRef, Slot: 4},
			stored(Rect{X: 5, Y: 140, W: 8, H: 8, Kind: OpSolid, Color: [4]byte{1, 2, 3, 255}}, 7),
			{X: 190, Y: 140, W: 8, H: 8, Kind: OpCacheRef, Slot: 7},
			stored(tile(150, 30, 10, 10, OpRaw, 6), 3), // resize slot 3
			{X: 0, Y: 0, W: 10, H: 10, Kind: OpCacheRef, Slot: 3},
			tile(70, 100, 64, 50, OpJPEG, 7),
			{X: 64, Y: 0, W: 64, H: 64, Kind: OpCacheRef, Slot: 5},
		}},
		{Seq: 3, Rects: []Rect{
			{X: 1, Y: 1, W: 199, H: 149, Kind: OpCopy, SrcX: 0, SrcY: 0}, // whole-image scroll
			{X: 0, Y: 0, W: 64, H: 64, Kind: OpCacheRef, Slot: 5},
		}},
		many,
	}
}

func checkDirty(t *testing.T, before, after *image.RGBA, dirty []image.Rectangle) {
	t.Helper()
	for _, d := range dirty {
		if !d.In(after.Rect) {
			t.Fatalf("dirty %v outside %v", d, after.Rect)
		}
	}
	b := after.Rect
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			o := after.PixOffset(x, y)
			if bytes.Equal(before.Pix[o:o+4], after.Pix[o:o+4]) {
				continue
			}
			p := image.Pt(x, y)
			found := false
			for _, d := range dirty {
				if p.In(d) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("pixel %v changed outside the dirty rects %v", p, dirty)
			}
		}
	}
}

func cloneRGBA(m *image.RGBA) *image.RGBA {
	c := *m
	c.Pix = bytes.Clone(m.Pix)
	return &c
}

func TestDecoderScript(t *testing.T) {
	for _, bounds := range []image.Rectangle{image.Rect(0, 0, 200, 150), image.Rect(-30, 17, 170, 167)} {
		dst := image.NewRGBA(bounds)
		ref := &model{img: image.NewRGBA(bounds), cache: map[uint16]*image.RGBA{}}
		d := NewDecoder()
		for _, f := range script() {
			// Through the wire format, as the agent sees it.
			wf, err := DecodeFrame(EncodeFrame(f))
			if err != nil {
				t.Fatal(err)
			}
			before := cloneRGBA(dst)
			dirty, complete, err := d.Apply(dst, wf)
			if err != nil || !complete {
				t.Fatalf("frame %d: %v %v", f.Seq, complete, err)
			}
			ref.apply(t, f)
			if !bytes.Equal(dst.Pix, ref.img.Pix) {
				t.Fatalf("frame %d differs from the model", f.Seq)
			}
			checkDirty(t, before, dst, dirty)
			if len(dirty) == 0 || len(dirty) > maxDirty {
				t.Fatalf("frame %d: %d dirty rects", f.Seq, len(dirty))
			}
		}
	}
}

func TestDecoderMultiPart(t *testing.T) {
	frames := script()
	dst := image.NewRGBA(image.Rect(0, 0, 200, 150))
	ref := &model{img: image.NewRGBA(dst.Rect), cache: map[uint16]*image.RGBA{}}
	d := NewDecoder()
	for _, f := range frames {
		ref.apply(t, f)
		n := len(f.Rects)
		parts := []Frame{
			{Seq: f.Seq, More: true, Rects: f.Rects[:n/3]},
			{Seq: f.Seq, More: true},
			{Seq: f.Seq, More: true, Rects: f.Rects[n/3 : 2*n/3]},
			{Seq: f.Seq, Rects: f.Rects[2*n/3:]},
		}
		for i, p := range parts {
			before := bytes.Clone(dst.Pix)
			_, complete, err := d.Apply(dst, p)
			if err != nil {
				t.Fatal(err)
			}
			if last := i == len(parts)-1; complete != last {
				t.Fatalf("part %d: complete = %v", i, complete)
			}
			if !complete && !bytes.Equal(before, dst.Pix) {
				t.Fatalf("part %d changed dst", i)
			}
		}
		if !bytes.Equal(dst.Pix, ref.img.Pix) {
			t.Fatalf("frame %d differs from the model", f.Seq)
		}
	}

	// A part of another frame while one is buffered is an error and drops it.
	d.Apply(dst, Frame{Seq: 10, More: true, Rects: []Rect{{W: 1, H: 1, Kind: OpSolid}}})
	if _, _, err := d.Apply(dst, Frame{Seq: 11}); err == nil {
		t.Fatal("mixed parts accepted")
	}
	if _, complete, err := d.Apply(dst, Frame{Seq: 12}); err != nil || !complete {
		t.Fatalf("after mixed parts: %v %v", complete, err)
	}

	// Too many parts.
	for i := 0; i < maxParts; i++ {
		if _, _, err := d.Apply(dst, Frame{Seq: 20, More: true}); err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
	}
	if _, _, err := d.Apply(dst, Frame{Seq: 20}); err == nil {
		t.Fatal("too many parts accepted")
	}
	// Too many bytes.
	big := Frame{Seq: 21, More: true, Rects: []Rect{{W: 1, H: 1, Kind: OpPalette, Data: make([]byte, 40<<20)}}}
	d.Apply(dst, big)
	if _, _, err := d.Apply(dst, big); err == nil {
		t.Fatal("too many bytes accepted")
	}
}

func TestDecoderErrorsLeaveDstUnchanged(t *testing.T) {
	setup := func() (*Decoder, *image.RGBA) {
		d := NewDecoder()
		dst := image.NewRGBA(image.Rect(0, 0, 300, 300))
		if _, _, err := d.Apply(dst, script()[0]); err != nil {
			t.Fatal(err)
		}
		return d, dst
	}
	good := []Rect{
		{X: 0, Y: 0, W: 300, H: 300, Kind: OpSolid, Color: [4]byte{1, 1, 1, 255}},
		{X: 5, Y: 5, W: 64, H: 64, Kind: OpCacheRef, Slot: 3},
		{X: 0, Y: 0, W: 10, H: 10, Kind: OpSolid, Store: true, Slot: 3, Color: [4]byte{9, 9, 9, 255}},
	}
	wrongSize := EncodeJPEG(photoTile(8, 8, 1), 32, 8, 8, 50)
	cases := map[string]Rect{
		"outside":         {X: 290, Y: 0, W: 11, H: 1, Kind: OpSolid},
		"negative":        {X: -1, Y: 0, W: 1, H: 1, Kind: OpSolid},
		"empty slot":      {W: 4, H: 4, Kind: OpCacheRef, Slot: 100},
		"slot size":       {W: 64, H: 64, Kind: OpCacheRef, Slot: 3}, // slot 3 was resized to 10x10 above
		"slot range":      {W: 1, H: 1, Kind: OpCacheRef, Slot: CacheSlots},
		"store too large": {W: 257, H: 4, Kind: OpSolid, Store: true, Slot: 1},
		"store range":     {W: 1, H: 1, Kind: OpSolid, Store: true, Slot: CacheSlots},
		"store on copy":   {W: 1, H: 1, Kind: OpCopy, Store: true, Slot: 1},
		"copy source":     {W: 10, H: 10, Kind: OpCopy, SrcX: 295},
		"raw length":      {W: 2, H: 2, RGBA: make([]byte, 15)},
		"bad palette":     {W: 4, H: 4, Kind: OpPalette, Data: []byte{0x28, 0xb5, 0x2f, 0xfd, 0, 0}},
		"no data":         {W: 4, H: 4, Kind: OpJPEG},
		"bad jpeg":        {W: 8, H: 8, Kind: OpJPEG, Data: []byte{0xff, 0xd8, 0xff, 0xdb}},
		"jpeg size":       {W: 8, H: 9, Kind: OpJPEG, Data: wrongSize},
		"kind":            {W: 1, H: 1, Kind: 9},
	}
	for name, bad := range cases {
		d, dst := setup()
		before := bytes.Clone(dst.Pix)
		ops := append(append([]Rect{}, good...), bad)
		// Many decodable tiles too, so the parallel path runs before the error.
		for i := 0; i < 8; i++ {
			ops = append(ops, Rect{X: i * 29, Y: 200, W: 64, H: 64, Kind: OpPalette, Data: mustPalette(textTile(64, 64, 1), 64, 64)})
		}
		if _, complete, err := d.Apply(dst, Frame{Seq: 5, Rects: ops}); err == nil || complete {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !bytes.Equal(before, dst.Pix) {
			t.Errorf("%s: dst changed", name)
		}
		// The cache is unchanged too: slot 3 is still 64x64.
		if _, _, err := d.Apply(dst, Frame{Seq: 6, Rects: []Rect{{W: 64, H: 64, Kind: OpCacheRef, Slot: 3}}}); err != nil {
			t.Errorf("%s: cache changed: %v", name, err)
		}
	}
	d, _ := setup()
	if _, _, err := d.Apply(nil, Frame{}); err == nil {
		t.Fatal("nil image accepted")
	}
	d.Reset()
	dst := image.NewRGBA(image.Rect(0, 0, 300, 300))
	if _, _, err := d.Apply(dst, Frame{Rects: []Rect{{W: 64, H: 64, Kind: OpCacheRef, Slot: 3}}}); err == nil {
		t.Fatal("cache survived Reset")
	}
}

func TestDecoderPhase2(t *testing.T) {
	r := image.Rect(0, 0, 333, 177)
	prev, cur := image.NewRGBA(r), image.NewRGBA(r)
	got := image.NewRGBA(r)
	d := NewDecoder()
	for i := 0; i < 5; i++ {
		TestPattern(cur, float64(i)*1.37, "anna@PC")
		f, err := DecodeFrame(EncodeFrame(Frame{Seq: uint32(i), Rects: Diff(prev, cur)}))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			prev = nil
		}
		if _, _, err := d.Apply(got, f); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Pix, cur.Pix) {
			t.Fatalf("frame %d differs", i)
		}
		prev = cloneRGBA(cur)
	}
}

func TestDirtyMerging(t *testing.T) {
	dst := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	d := NewDecoder()
	var ops []Rect
	for y := 0; y < 1080; y += 64 {
		for x := 0; x < 1920; x += 64 {
			ops = append(ops, Rect{X: x, Y: y, W: min(64, 1920-x), H: min(64, 1080-y), Kind: OpSolid})
		}
	}
	dirty, _, err := d.Apply(dst, Frame{Rects: ops})
	if err != nil {
		t.Fatal(err)
	}
	if len(dirty) != 1 || dirty[0] != dst.Rect {
		t.Fatalf("full screen: %v", dirty)
	}
	// A checkerboard of tiles stays within maxDirty.
	ops = ops[:0]
	for y := 0; y < 1080; y += 64 {
		for x := (y / 64 % 2) * 64; x < 1920; x += 128 {
			ops = append(ops, Rect{X: x, Y: y, W: min(64, 1920-x), H: min(64, 1080-y), Kind: OpSolid})
		}
	}
	dirty, _, _ = d.Apply(dst, Frame{Rects: ops})
	if len(dirty) == 0 || len(dirty) > maxDirty {
		t.Fatalf("checkerboard: %d rects", len(dirty))
	}
	for _, op := range ops {
		r := image.Rect(op.X, op.Y, op.X+op.W, op.Y+op.H)
		covered := false
		for _, d := range dirty {
			if r.In(d) {
				covered = true
			}
		}
		if !covered {
			t.Fatalf("%v not covered", r)
		}
	}
	// Few ops are returned exactly, minus contained ones.
	dirty, _, _ = d.Apply(dst, Frame{Rects: []Rect{
		{X: 10, Y: 10, W: 5, H: 5, Kind: OpSolid},
		{X: 0, Y: 0, W: 100, H: 100, Kind: OpSolid},
		{X: 500, Y: 500, W: 3, H: 3, Kind: OpSolid},
	}})
	if want := []image.Rectangle{image.Rect(0, 0, 100, 100), image.Rect(500, 500, 503, 503)}; !reflect.DeepEqual(dirty, want) {
		t.Fatalf("exact: %v", dirty)
	}
}

func TestPaletteSpeed(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing")
	}
	px := textTile(64, 64, 1)
	res := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			EncodePalette(px, 256, 64, 64)
		}
	})
	// About 13 µs on a desktop CPU. The bound only catches gross regressions
	// (an accidental per-pixel allocation or the slowest zstd level); shared CI
	// runners are several times slower, so it is generous on purpose. Use
	// BenchmarkEncodePalette for real numbers.
	if ns := res.NsPerOp(); ns > 500000 {
		t.Fatalf("EncodePalette of a 64x64 text tile takes %d ns", ns)
	}
}

// TestDecoderAllocations checks that tiny payloads claiming big tiles
// allocate little in DecodeFrame and Decoder.Apply.
func TestDecoderAllocations(t *testing.T) {
	// A zstd frame that claims 64 MiB of content.
	enc, _ := zstd.NewWriter(nil)
	bomb := enc.EncodeAll(make([]byte, 16<<20), nil)
	dst := image.NewRGBA(image.Rect(0, 0, 64, 64))
	d := NewDecoder()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < 20; i++ {
		if _, _, err := d.Apply(dst, Frame{Rects: []Rect{{W: 64, H: 64, Kind: OpPalette, Data: bomb}}}); err == nil {
			t.Fatal("bomb accepted")
		}
	}
	runtime.ReadMemStats(&after)
	if n := after.TotalAlloc - before.TotalAlloc; n > 8<<20 {
		t.Fatalf("20 bombs allocated %d bytes", n)
	}
}

func FuzzDecodeHelloV2(f *testing.F) {
	f.Add(EncodeHello(Hello{Hostname: "h", Monitors: []Monitor{{X: -5, Y: 3, W: 10, H: 20, Primary: true, Name: "a"}, {}}, Current: 1}))
	f.Fuzz(func(t *testing.T, b []byte) {
		h, err := DecodeHello(b)
		if err != nil {
			return
		}
		if !bytes.Equal(EncodeHello(h), b) {
			t.Fatal("re-encoding differs")
		}
	})
}

func FuzzDecodeCursorShape(f *testing.F) {
	f.Add(EncodeCursorShape(CursorShape{ID: 1, HotX: 1, HotY: 2, W: 3, H: 4, RGBA: make([]byte, 48)}))
	f.Add(EncodeCursorShape(CursorShape{}))
	f.Fuzz(func(t *testing.T, b []byte) {
		c, err := DecodeCursorShape(b)
		if err != nil {
			return
		}
		if !bytes.Equal(EncodeCursorShape(c), b) {
			t.Fatal("re-encoding differs")
		}
	})
}

func FuzzDecodeCursorPos(f *testing.F) {
	f.Add(EncodeCursorPos(CursorPos{X: -1, Y: 2, ID: 3, Visible: true}))
	f.Fuzz(func(t *testing.T, b []byte) {
		c, err := DecodeCursorPos(b)
		if err == nil && !bytes.Equal(EncodeCursorPos(c), b) {
			t.Fatal("re-encoding differs")
		}
	})
}

func FuzzDecodeSelectMonitor(f *testing.F) {
	f.Add([]byte{3})
	f.Fuzz(func(t *testing.T, b []byte) {
		s, err := DecodeSelectMonitor(b)
		if err == nil && !bytes.Equal(EncodeSelectMonitor(s), b) {
			t.Fatal("re-encoding differs")
		}
	})
}

func FuzzDecodeQuality(f *testing.F) {
	f.Add([]byte{1})
	f.Fuzz(func(t *testing.T, b []byte) {
		q, err := DecodeQuality(b)
		if err == nil && !bytes.Equal(EncodeQuality(q), b) {
			t.Fatal("re-encoding differs")
		}
	})
}

// FuzzDecoderApply feeds a frame to a Decoder with a primed cache, then
// checks it never panics, writes only within dst and keeps working.
func FuzzDecoderApply(f *testing.F) {
	for _, fr := range sampleFrames() {
		f.Add(EncodeFrame(fr))
	}
	f.Add(EncodeFrame(Frame{Rects: []Rect{{X: 1, Y: 1, W: 16, H: 16, Kind: OpPalette, Data: mustPalette(textTile(16, 16, 7), 16, 16)}}}))
	prime := sampleFrames()
	f.Fuzz(func(t *testing.T, b []byte) {
		fr, err := DecodeFrame(b)
		if err != nil {
			return
		}
		fr.More = false
		d := NewDecoder()
		backing := image.NewRGBA(image.Rect(0, 0, 66, 50))
		dst := backing.SubImage(image.Rect(1, 1, 65, 49)).(*image.RGBA)
		for _, p := range prime {
			if _, _, err := d.Apply(dst, p); err != nil {
				t.Fatal(err)
			}
		}
		edge := bytes.Clone(backing.Pix)
		dirty, complete, err := d.Apply(dst, fr)
		if err == nil {
			if !complete || len(dirty) > maxDirty {
				t.Fatalf("complete %v, %d dirty", complete, len(dirty))
			}
			for _, r := range dirty {
				if !r.In(dst.Rect) {
					t.Fatalf("dirty %v outside %v", r, dst.Rect)
				}
			}
		}
		// The one-pixel border around dst must be untouched.
		for y := 0; y < 50; y++ {
			for x := 0; x < 66; x++ {
				if image.Pt(x, y).In(dst.Rect) {
					continue
				}
				o := backing.PixOffset(x, y)
				if !bytes.Equal(edge[o:o+4], backing.Pix[o:o+4]) {
					t.Fatalf("wrote outside dst at %d,%d", x, y)
				}
			}
		}
		if _, _, err := d.Apply(dst, prime[0]); err != nil {
			t.Fatal(err)
		}
	})
}

func BenchmarkEncodePalette(b *testing.B) {
	for _, tc := range []struct {
		name string
		px   []byte
	}{{"text", textTile(64, 64, 1)}, {"photo", photoTile(64, 64, 1)}} {
		b.Run(tc.name, func(b *testing.B) {
			b.SetBytes(64 * 64 * 4)
			b.ReportAllocs()
			size := 0
			for b.Loop() {
				data, _ := EncodePalette(tc.px, 256, 64, 64)
				size = len(data)
			}
			b.ReportMetric(float64(size), "bytes/tile")
		})
	}
}

func BenchmarkEncodeJPEG(b *testing.B) {
	for _, tc := range []struct {
		name string
		px   []byte
	}{{"text", textTile(64, 64, 1)}, {"photo", photoTile(64, 64, 1)}} {
		b.Run(tc.name, func(b *testing.B) {
			b.SetBytes(64 * 64 * 4)
			b.ReportAllocs()
			size := 0
			for b.Loop() {
				size = len(EncodeJPEG(tc.px, 256, 64, 64, 75))
			}
			b.ReportMetric(float64(size), "bytes/tile")
		})
	}
}

// BenchmarkDecoderApply1080p applies a full 1920x1080 frame of mixed tiles:
// palette text, JPEG photo, solid and cache refs.
func BenchmarkDecoderApply1080p(b *testing.B) {
	dst := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	d := NewDecoder()
	text := mustPalette(textTile(64, 64, 1), 64, 64)
	photo := EncodeJPEG(photoTile(64, 64, 1), 256, 64, 64, 75)
	prime := Frame{Rects: []Rect{{W: 64, H: 64, Kind: OpPalette, Data: text, Store: true, Slot: 1}}}
	if _, _, err := d.Apply(dst, prime); err != nil {
		b.Fatal(err)
	}
	var ops []Rect
	i := 0
	for y := 0; y < 1080; y += 64 {
		for x := 0; x < 1920; x += 64 {
			w, h := min(64, 1920-x), min(64, 1080-y)
			r := Rect{X: x, Y: y, W: w, H: h}
			switch {
			case w != 64 || h != 64 || i%8 == 0:
				r.Kind, r.Color = OpSolid, [4]byte{40, 40, 40, 255}
			case i%8 == 1:
				r.Kind, r.Slot = OpCacheRef, 1
			case i%8 < 5:
				r.Kind, r.Data = OpPalette, text
			default:
				r.Kind, r.Data = OpJPEG, photo
			}
			ops = append(ops, r)
			i++
		}
	}
	f := Frame{Seq: 1, Rects: ops}
	b.SetBytes(int64(len(dst.Pix)))
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := d.Apply(dst, f); err != nil {
			b.Fatal(err)
		}
	}
}
