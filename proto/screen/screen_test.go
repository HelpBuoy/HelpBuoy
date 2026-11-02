package screen

import (
	"bytes"
	"errors"
	"image"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHelloRoundTrip(t *testing.T) {
	for _, h := range []Hello{
		{},
		{Hostname: "ANNA-LAPTOP", UserMachine: "anna@ANNA-LAPTOP", OS: "Windows 11 23H2", Width: 1920, Height: 1080},
		{Hostname: "héllo", Width: MaxDimension, Height: 1},
	} {
		got, err := DecodeHello(EncodeHello(h))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, h) {
			t.Fatalf("got %+v, want %+v", got, h)
		}
	}
	long := strings.Repeat("é", MaxString)
	got, err := DecodeHello(EncodeHello(Hello{Hostname: long, Width: 99999, Height: -5}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hostname) > MaxString || !strings.HasPrefix(long, got.Hostname) || got.Width != MaxDimension || got.Height != 0 {
		t.Fatalf("clamping: %d bytes, %dx%d", len(got.Hostname), got.Width, got.Height)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	px := func(w, h int, v byte) []byte { return bytes.Repeat([]byte{v}, w*h*4) }
	for _, f := range []Frame{
		{Seq: 0},
		{Seq: 7, Rects: []Rect{{X: 0, Y: 0, W: 1, H: 1, RGBA: px(1, 1, 9)}}},
		{Seq: 1<<32 - 1, Rects: []Rect{
			{X: 64, Y: 128, W: 64, H: 64, RGBA: px(64, 64, 1)},
			{X: 1900, Y: 1050, W: 20, H: 30, RGBA: px(20, 30, 2)},
		}},
	} {
		got, err := DecodeFrame(EncodeFrame(f))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, f) {
			t.Fatalf("frame %d mismatch", f.Seq)
		}
	}
	// A rect with the wrong pixel length is padded so the stream stays valid.
	got, err := DecodeFrame(EncodeFrame(Frame{Rects: []Rect{{W: 2, H: 2, RGBA: []byte{1}}}}))
	if err != nil || len(got.Rects[0].RGBA) != 16 || got.Rects[0].RGBA[0] != 1 {
		t.Fatalf("padding: %v %v", got, err)
	}
}

func TestAckRoundTrip(t *testing.T) {
	for _, s := range []uint32{0, 1, 1 << 31, 1<<32 - 1} {
		got, err := DecodeAck(EncodeAck(Ack{Seq: s}))
		if err != nil || got.Seq != s {
			t.Fatalf("ack %d: %v %v", s, got, err)
		}
	}
	if _, err := DecodeAck([]byte{1, 2, 3}); !errors.Is(err, ErrMalformed) {
		t.Fatal("short ack accepted")
	}
}

func TestDecodeFrameRejects(t *testing.T) {
	enc := func(x, y, w, h int, n int) []byte {
		b := []byte{0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, byte(x >> 8), byte(x), byte(y >> 8), byte(y), byte(w >> 8), byte(w), byte(h >> 8), byte(h)}
		return append(b, make([]byte, n)...)
	}
	op := func(kind, flags byte, body ...byte) []byte {
		b := []byte{0, 0, 0, 1, 0, 0, 0, 0, 1, kind, flags, 0, 0, 0, 0, 0, 1, 0, 1}
		return append(b, body...)
	}
	cases := map[string][]byte{
		"empty":            nil,
		"no count":         {0, 0, 0, 1},
		"no flags":         {0, 0, 0, 1, 0},
		"frame flags":      {0, 0, 0, 1, 4, 0, 0, 0, 0},
		"zero width":       enc(0, 0, 0, 1, 4),
		"too wide":         enc(0, 0, MaxDimension+1, 1, 4*(MaxDimension+1)),
		"off the edge":     enc(MaxDimension-1, 0, 2, 1, 8),
		"short pixels":     enc(0, 0, 2, 2, 15),
		"trailing":         enc(0, 0, 1, 1, 5),
		"many rects":       {0, 0, 0, 1, 0, 0, 1, 0, 1},
		"huge count":       {0, 0, 0, 1, 0, 0xff, 0xff, 0xff, 0xff},
		"count/payload":    {0, 0, 0, 1, 0, 0, 0, 0, 2, 0, 0, 0, 0, 0, 0, 0, 1, 0, 1, 1, 2, 3, 4},
		"unknown kind":     op(6, 0, 0, 0, 0, 0),
		"op flags":         op(byte(OpSolid), 2, 0, 0, 0, 0),
		"store on copy":    op(byte(OpCopy), 1, 0, 0, 0, 0, 0, 0),
		"store slot":       op(byte(OpSolid), 1, 0x20, 0, 1, 2, 3, 4),
		"cacheref slot":    op(byte(OpCacheRef), 0, 0x20, 0),
		"copy source":      op(byte(OpCopy), 0, 0x40, 0, 0, 0),
		"data length":      op(byte(OpPalette), 0, 0, 0, 0, 9, 1, 2),
		"huge data length": op(byte(OpJPEG), 0, 0xff, 0xff, 0xff, 0xff),
		"short solid":      op(byte(OpSolid), 0, 1, 2, 3),
	}
	for name, b := range cases {
		if _, err := DecodeFrame(b); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := DecodeFrame(enc(0, 0, 2, 2, 16)); err != nil {
		t.Fatalf("valid frame rejected: %v", err)
	}
	if _, err := DecodeFrame(op(byte(OpCacheRef), 0, 0x1f, 0xff)); err != nil {
		t.Fatalf("valid cacheref rejected: %v", err)
	}
}

func TestApplyBounds(t *testing.T) {
	dst := image.NewRGBA(image.Rect(0, 0, 100, 50))
	ok := Rect{X: 90, Y: 40, W: 10, H: 10, RGBA: make([]byte, 400)}
	bad := []Rect{
		{X: 91, Y: 40, W: 10, H: 10, RGBA: make([]byte, 400)},
		{X: -1, Y: 0, W: 1, H: 1, RGBA: make([]byte, 4)},
		{X: 0, Y: 0, W: 0, H: 1},
		{X: 0, Y: 0, W: 2, H: 2, RGBA: make([]byte, 4)},
		{X: 1 << 62, Y: 0, W: 1 << 62, H: 1},
	}
	for i, r := range bad {
		ok.RGBA[0] = 77
		if err := Apply(dst, Frame{Rects: []Rect{ok, r}}); err == nil {
			t.Fatalf("bad rect %d accepted", i)
		}
		if dst.Pix[dst.PixOffset(90, 40)] != 0 {
			t.Fatal("Apply wrote before failing")
		}
	}
	if err := Apply(dst, Frame{Rects: []Rect{ok}}); err != nil {
		t.Fatal(err)
	}
	if dst.Pix[dst.PixOffset(90, 40)] != 77 {
		t.Fatal("Apply did not write")
	}
	if err := Apply(nil, Frame{}); err == nil {
		t.Fatal("nil image accepted")
	}
}

// TestDiffApply checks that Diff+Apply reproduces every frame exactly, also
// through the encoder, for sizes that are not tile multiples and for images
// whose bounds do not start at the origin.
func TestDiffApply(t *testing.T) {
	for _, r := range []image.Rectangle{
		image.Rect(0, 0, 1920, 1080),
		image.Rect(0, 0, 333, 177),
		image.Rect(10, 20, 10+130, 20+65),
		image.Rect(0, 0, 1, 1),
		image.Rect(0, 0, 63, 200),
	} {
		cur := image.NewRGBA(r)
		var prev *image.RGBA
		view := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
		for i := 0; i < 40; i++ {
			TestPattern(cur, float64(i)*0.033+float64(i%7)*0.5, "anna@ANNA-LAPTOP")
			rects := Diff(prev, cur)
			if i > 0 && r.Dx() == 1920 {
				full := ((1920 + 63) / 64) * ((1080 + 63) / 64)
				if len(rects) > full/4 {
					t.Fatalf("frame %d: %d of %d tiles changed", i, len(rects), full)
				}
			}
			f, err := DecodeFrame(EncodeFrame(Frame{Seq: uint32(i), Rects: rects}))
			if err != nil {
				t.Fatal(err)
			}
			if err := Apply(view, f); err != nil {
				t.Fatal(err)
			}
			for y := 0; y < r.Dy(); y++ {
				a := view.Pix[y*view.Stride : y*view.Stride+r.Dx()*4]
				b := cur.Pix[y*cur.Stride : y*cur.Stride+r.Dx()*4]
				if !bytes.Equal(a, b) {
					t.Fatalf("%v frame %d: row %d differs", r, i, y)
				}
			}
			if prev == nil {
				prev = image.NewRGBA(r)
			}
			copy(prev.Pix, cur.Pix)
		}
	}
}

func TestDiffUnchangedAndFull(t *testing.T) {
	a := image.NewRGBA(image.Rect(0, 0, 200, 100))
	TestPattern(a, 1, "x")
	b := image.NewRGBA(a.Rect)
	copy(b.Pix, a.Pix)
	if got := Diff(a, b); len(got) != 0 {
		t.Fatalf("identical images: %d rects", len(got))
	}
	b.Pix[b.PixOffset(199, 99)] ^= 1
	got := Diff(a, b)
	if len(got) != 1 || got[0].X != 192 || got[0].Y != 64 || got[0].W != 8 || got[0].H != 36 {
		t.Fatalf("one pixel change: %+v", got)
	}
	if n := len(Diff(nil, b)); n != 4*2 {
		t.Fatalf("full image: %d rects", n)
	}
	if n := len(Diff(image.NewRGBA(image.Rect(0, 0, 10, 10)), b)); n != 8 {
		t.Fatalf("size change: %d rects", n)
	}
	if Diff(nil, nil) != nil || Diff(nil, image.NewRGBA(image.Rectangle{})) != nil {
		t.Fatal("empty images produced rects")
	}
}

func TestPatternOddInputs(t *testing.T) {
	for _, r := range []image.Rectangle{
		image.Rect(0, 0, 1, 1), image.Rect(0, 0, 3, 200), image.Rect(0, 0, 400, 2),
		image.Rect(-50, -50, 50, 50), image.Rect(0, 0, 0, 0),
	} {
		img := image.NewRGBA(r)
		for _, tm := range []float64{-1, 0, 0.5, 1e12, 3599999.99} {
			TestPattern(img, tm, strings.Repeat("Ünicode@label ", 100))
		}
	}
	TestPattern(nil, 0, "")
	sub := image.NewRGBA(image.Rect(0, 0, 300, 300)).SubImage(image.Rect(50, 50, 250, 150)).(*image.RGBA)
	TestPattern(sub, 2, "sub")
}

func TestPatternChangesOverTime(t *testing.T) {
	a := image.NewRGBA(image.Rect(0, 0, 640, 480))
	b := image.NewRGBA(a.Rect)
	TestPattern(a, 1.00, "l")
	TestPattern(b, 1.01, "l")
	if bytes.Equal(a.Pix, b.Pix) {
		t.Fatal("pattern did not change in 10 ms")
	}
	TestPattern(b, 1.00, "l")
	if !bytes.Equal(a.Pix, b.Pix) {
		t.Fatal("pattern is not deterministic")
	}
}

func TestWindow(t *testing.T) {
	var w Window
	if !w.CanSend() {
		t.Fatal("empty window full")
	}
	w.Sent(1)
	w.Sent(2)
	if w.CanSend() || w.InFlight() != 2 {
		t.Fatal("window not full after 2")
	}
	if _, ok := w.Acked(5); ok {
		t.Fatal("ack for unsent frame found")
	}
	if w.InFlight() != 0 {
		t.Fatal("cumulative ack did not clear older frames")
	}
	select {
	case <-w.Freed():
	default:
		t.Fatal("no Freed signal")
	}

	w.Sent(1<<32 - 1) // wraparound
	w.Sent(0)
	time.Sleep(time.Millisecond)
	if rtt, ok := w.Acked(1<<32 - 1); !ok || rtt <= 0 {
		t.Fatalf("ack: %v %v", rtt, ok)
	}
	if !w.CanSend() || w.InFlight() != 1 {
		t.Fatal("ack did not free one slot")
	}
	if _, ok := w.Acked(1<<32 - 2); ok || w.InFlight() != 1 {
		t.Fatal("stale ack changed the window")
	}
	if _, ok := w.Acked(0); !ok || w.InFlight() != 0 {
		t.Fatal("ack across wraparound")
	}
	w.Sent(10)
	w.Reset()
	if w.InFlight() != 0 {
		t.Fatal("Reset")
	}
}

func TestWindowConcurrent(t *testing.T) {
	var w Window
	acks := make(chan uint32, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for s := range acks {
			w.Acked(s)
		}
	}()
	for seq := uint32(0); seq < 1000; {
		if !w.CanSend() {
			<-w.Freed()
			continue
		}
		if w.InFlight() >= MaxUnacked {
			t.Fatal("more than MaxUnacked in flight")
		}
		w.Sent(seq)
		acks <- seq
		seq++
	}
	close(acks)
	<-done
}

func FuzzDecodeHello(f *testing.F) {
	f.Add(EncodeHello(Hello{Hostname: "h", UserMachine: "u@m", OS: "w", Width: 10, Height: 20}))
	f.Add([]byte{})
	f.Add([]byte{0xff, 0xff})
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

func FuzzDecodeFrame(f *testing.F) {
	f.Add(EncodeFrame(Frame{Seq: 3, Rects: []Rect{{X: 1, Y: 2, W: 2, H: 1, RGBA: make([]byte, 8)}}}))
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0})
	f.Add([]byte{0, 0, 0, 0, 0, 0xff, 0xff, 0xff, 0xff})
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0x40, 0, 0x40, 0, 0x40, 0, 0x40, 0})
	for _, fr := range sampleFrames() {
		f.Add(EncodeFrame(fr))
	}
	dst := image.NewRGBA(image.Rect(0, 0, 32, 32))
	f.Fuzz(func(t *testing.T, b []byte) {
		fr, err := DecodeFrame(b)
		if err != nil {
			return
		}
		pix := 0
		for _, r := range fr.Rects {
			pix += len(r.RGBA) + len(r.Data)
		}
		if pix > len(b) {
			t.Fatal("pixels larger than payload")
		}
		if !bytes.Equal(EncodeFrame(fr), b) {
			t.Fatal("re-encoding differs")
		}
		Apply(dst, fr)
		NewDecoder().Apply(dst, fr)
	})
}

func FuzzDecodeAck(f *testing.F) {
	f.Add([]byte{0, 0, 0, 1})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		a, err := DecodeAck(b)
		if err == nil && !bytes.Equal(EncodeAck(a), b) {
			t.Fatal("re-encoding differs")
		}
	})
}

// TestDecodeAllocations checks that a tiny payload claiming a huge frame does
// not allocate much.
func TestDecodeAllocations(t *testing.T) {
	b := []byte{0, 0, 0, 0, 0, 0, 1, 0, 0}
	b = append(b, make([]byte, 64)...)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < 100; i++ {
		if _, err := DecodeFrame(b); err == nil {
			t.Fatal("bogus frame accepted")
		}
	}
	runtime.ReadMemStats(&after)
	if n := after.TotalAlloc - before.TotalAlloc; n > 100<<10 {
		t.Fatalf("100 decodes allocated %d bytes", n)
	}
}

func BenchmarkPatternDiff1080p(b *testing.B) {
	r := image.Rect(0, 0, 1920, 1080)
	cur, prev := image.NewRGBA(r), image.NewRGBA(r)
	TestPattern(prev, 0, "anna@ANNA-LAPTOP")
	b.SetBytes(int64(len(cur.Pix)))
	b.ReportAllocs()
	tiles := 0
	i := 0
	for b.Loop() {
		i++
		TestPattern(cur, float64(i)/30, "anna@ANNA-LAPTOP")
		tiles += len(Diff(prev, cur))
		prev, cur = cur, prev
	}
	b.ReportMetric(float64(tiles)/float64(i), "tiles/frame")
}

func BenchmarkPatternDiffEncode1080p(b *testing.B) {
	r := image.Rect(0, 0, 1920, 1080)
	cur, prev := image.NewRGBA(r), image.NewRGBA(r)
	TestPattern(prev, 0, "anna@ANNA-LAPTOP")
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		i++
		TestPattern(cur, float64(i)/30, "anna@ANNA-LAPTOP")
		_ = EncodeFrame(Frame{Seq: uint32(i), Rects: Diff(prev, cur)})
		prev, cur = cur, prev
	}
}

func TestFrameFullFlag(t *testing.T) {
	for _, f := range []Frame{{Seq: 7, Full: true}, {Seq: 8, Full: true, More: true}, {Seq: 9}} {
		got, err := DecodeFrame(EncodeFrame(f))
		if err != nil {
			t.Fatal(err)
		}
		if got.Seq != f.Seq || got.More != f.More || got.Full != f.Full {
			t.Fatalf("got %+v, want %+v", got, f)
		}
	}
}
