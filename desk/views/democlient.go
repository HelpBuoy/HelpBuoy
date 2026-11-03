package views

import (
	"hash/fnv"
	"image"
	"math"
	"net"
	"sync"
	"time"

	"github.com/mevdschee/helpbuoy/proto/input"
	"github.com/mevdschee/helpbuoy/proto/screen"
	"github.com/mevdschee/helpbuoy/proto/wire"
)

// demoMonitors are the fake client's displays; they differ in size, so
// switching exercises the resize path, and the third is larger than most
// windows, for trying 1:1 with scrolling.
var demoMonitors = []screen.Monitor{
	{X: 0, Y: 0, W: 1280, H: 720, Primary: true, Name: "Built-in display"},
	{X: 1280, Y: 0, W: 1024, H: 768, Name: "DELL P2419H"},
	{X: 2304, Y: 0, W: 2560, H: 1440, Name: "LG QHD"},
}

// Cursor shape IDs of the demo client.
const (
	demoArrow = 1
	demoBeam  = 2
)

// demoCacheSlots is how many tile cache slots the demo client uses.
const demoCacheSlots = 1024

// demoClient is an in-process stand-in for the Windows client. Without the
// real encoder it still uses every part of the phase 3/4 stream: the test
// pattern with a scrolling text panel (OpCopy plus a new strip) and a small
// animated "video" (JPEG tiles), solid and palette tiles, the tile cache
// (Store and OpCacheRef), full frames split into parts, cursor shapes and
// positions, monitor switching, quality and Refresh. Its cursor follows the
// agent's mouse moves and otherwise wanders on its own.
type demoClient struct {
	name    string
	fw      *wire.Writer
	start   time.Time
	cur     int
	quality screen.QualityMode
	prev    *image.RGBA // what the agent has, apart from JPEG losses
	scroll  int
	seq     uint32

	cache map[uint64]uint16 // tile hash to slot
	slots []uint64          // slot to tile hash
	next  int

	prevScroll int // scroll offset of the panel in prev

	mu      sync.Mutex
	mouse   input.MouseMove
	mouseAt time.Time
	lastPos screen.CursorPos
	sentPos bool
}

// panel and video are the regions of the scrolling text and the animation.
func (d *demoClient) panel() image.Rectangle {
	m := demoMonitors[d.cur]
	return image.Rect(48, m.H-264, 48+448, m.H-56)
}

func (d *demoClient) video() image.Rectangle {
	m := demoMonitors[d.cur]
	return image.Rect(m.W-48-256, m.H-264, m.W-48, m.H-264+192)
}

// runDemoClient serves one demo session until the streams close.
func runDemoClient(scr, in net.Conn, name string) {
	defer scr.Close()
	defer in.Close()
	d := &demoClient{name: name, fw: wire.NewWriter(scr), start: time.Now()}
	go d.readInput(in)

	events := make(chan func(), 16)
	go func() {
		defer close(events)
		fr := wire.NewReader(scr)
		for {
			typ, b, err := fr.ReadFrame()
			if err != nil {
				return
			}
			var ev func()
			switch typ {
			case screen.MsgAck:
				ev = func() {}
			case screen.MsgRefresh:
				ev = func() { d.prev = nil }
			case screen.MsgSelectMonitor:
				if m, err := screen.DecodeSelectMonitor(b); err == nil && m.Index < len(demoMonitors) {
					ev = func() { d.cur = m.Index; d.sendHello() }
				}
			case screen.MsgQuality:
				if q, err := screen.DecodeQuality(b); err == nil {
					ev = func() { d.quality = q.Mode; d.prev = nil }
				}
			}
			if ev != nil {
				events <- ev
			}
			if typ == screen.MsgAck {
				events <- nil // an ack: one frame less in flight
			}
		}
	}()

	if !d.sendHello() || !d.sendShapes() {
		return
	}
	inFlight := 0
	tick := time.NewTicker(time.Second / 60)
	defer tick.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			if ev == nil {
				inFlight--
			} else {
				ev()
			}
			continue
		case <-tick.C:
		}
		if !d.sendCursor() {
			return
		}
		if inFlight >= 2 {
			continue
		}
		n, ok := d.sendFrame()
		if !ok {
			return
		}
		inFlight += n
	}
}

// readInput keeps the latest mouse position from the agent.
func (d *demoClient) readInput(in net.Conn) {
	r := wire.NewReader(in)
	for {
		typ, b, err := r.ReadFrame()
		if err != nil {
			return
		}
		m, err := input.Decode(typ, b)
		if mv, ok := m.(input.MouseMove); ok && err == nil {
			d.mu.Lock()
			d.mouse, d.mouseAt = mv, time.Now()
			d.mu.Unlock()
		}
	}
}

func (d *demoClient) sendHello() bool {
	m := demoMonitors[d.cur]
	d.prev = nil
	return d.fw.WriteFrame(screen.MsgHello, screen.EncodeHello(screen.Hello{
		Hostname: "DEMO", UserMachine: d.name, OS: "demo", Width: m.W, Height: m.H,
		Monitors: demoMonitors, Current: d.cur,
	})) == nil
}

func (d *demoClient) sendShapes() bool {
	for _, c := range []screen.CursorShape{arrowShape(), beamShape()} {
		if d.fw.WriteFrame(screen.MsgCursor, screen.EncodeCursorShape(c)) != nil {
			return false
		}
	}
	return true
}

// sendCursor sends the cursor position when it changed: at the agent's
// mouse while it moved in the last 1.5 s, else on a slow loop.
func (d *demoClient) sendCursor() bool {
	m := demoMonitors[d.cur]
	d.mu.Lock()
	mv, at := d.mouse, d.mouseAt
	d.mu.Unlock()
	var x, y int
	if time.Since(at) < 1500*time.Millisecond {
		x = int(float64(mv.X) / 65535 * float64(m.W-1))
		y = int(float64(mv.Y) / 65535 * float64(m.H-1))
	} else {
		t := time.Since(d.start).Seconds()
		x = m.W/2 + int(float64(m.W)*0.35*math.Sin(t*0.5))
		y = m.H/2 + int(float64(m.H)*0.35*math.Sin(t*0.8))
	}
	p := screen.CursorPos{X: x, Y: y, ID: demoArrow, Visible: true}
	if image.Pt(x, y).In(d.panel()) {
		p.ID = demoBeam
	}
	if d.sentPos && p == d.lastPos {
		return true
	}
	d.lastPos, d.sentPos = p, true
	return d.fw.WriteFrame(screen.MsgCursorPos, screen.EncodeCursorPos(p)) == nil
}

// sendFrame renders the next screen and sends what changed; it returns the
// number of frames sent (0 or 1).
func (d *demoClient) sendFrame() (int, bool) {
	m := demoMonitors[d.cur]
	t := time.Since(d.start).Seconds()
	cur := image.NewRGBA(image.Rect(0, 0, m.W, m.H))
	label := d.name + "  " + map[screen.QualityMode]string{screen.QualityAuto: "auto", screen.QualitySharp: "sharp", screen.QualityFast: "fast"}[d.quality]
	screen.TestPattern(cur, t, label)
	// Scroll for three seconds, then rest for two.
	if math.Mod(t, 5) < 3 {
		d.scroll += 2
	}
	pr := d.panel()
	drawPanel(cur, pr, d.scroll)
	drawVideo(cur, d.video(), t)

	var ops []screen.Rect
	full := d.prev == nil || d.prev.Rect != cur.Rect
	if full {
		d.cache, d.slots, d.next = map[uint64]uint16{}, make([]uint64, demoCacheSlots), 0
		d.prev = image.NewRGBA(cur.Rect)
		for i := range d.prev.Pix {
			d.prev.Pix[i] = 1 // differs from every opaque pixel: all tiles are sent
		}
	} else if step := d.scrolledBy(pr); step > 0 {
		op := screen.Rect{Kind: screen.OpCopy, X: pr.Min.X, Y: pr.Min.Y, W: pr.Dx(), H: pr.Dy() - step, SrcX: pr.Min.X, SrcY: pr.Min.Y + step}
		shiftUp(d.prev, pr, step)
		ops = append(ops, op)
	}
	for _, r := range screen.Diff(d.prev, cur) {
		ops = append(ops, d.tileOp(r))
	}
	d.prev = cur
	d.prevScroll = d.scroll
	if len(ops) == 0 {
		return 0, true
	}
	d.seq++
	parts := [][]screen.Rect{ops}
	if full && len(ops) > 1 {
		parts = [][]screen.Rect{ops[:len(ops)/2], ops[len(ops)/2:]}
	}
	for i, p := range parts {
		f := screen.Frame{Seq: d.seq, More: i < len(parts)-1, Full: full, Rects: p}
		if d.fw.WriteFrame(screen.MsgFrame, screen.EncodeFrame(f)) != nil {
			return 0, false
		}
	}
	return 1, true
}

// scrolledBy returns how far the panel scrolled since the last frame, or 0
// if it did not or a copy would not help.
func (d *demoClient) scrolledBy(pr image.Rectangle) int {
	step := d.scroll - d.prevScroll
	if step <= 0 || step >= pr.Dy() {
		return 0
	}
	return step
}

// tileOp codes one changed tile: solid, palette or (unless Sharp) JPEG, and
// lossless tiles through the cache.
func (d *demoClient) tileOp(r screen.Rect) screen.Rect {
	stride := r.W * 4
	if c, ok := screen.SolidColor(r.RGBA, stride, r.W, r.H); ok {
		return screen.Rect{Kind: screen.OpSolid, X: r.X, Y: r.Y, W: r.W, H: r.H, Color: c}
	}
	h := fnv.New64a()
	h.Write([]byte{byte(r.W), byte(r.H)})
	h.Write(r.RGBA)
	sum := h.Sum64()
	if slot, ok := d.cache[sum]; ok && d.slots[slot] == sum {
		return screen.Rect{Kind: screen.OpCacheRef, X: r.X, Y: r.Y, W: r.W, H: r.H, Slot: slot}
	}
	op := screen.Rect{X: r.X, Y: r.Y, W: r.W, H: r.H}
	if data, ok := screen.EncodePalette(r.RGBA, stride, r.W, r.H); ok {
		op.Kind, op.Data = screen.OpPalette, data
	} else if d.quality == screen.QualitySharp {
		op.Kind, op.RGBA = screen.OpRaw, r.RGBA
	} else {
		q := 75
		if d.quality == screen.QualityFast {
			q = 45
		}
		op.Kind, op.Data = screen.OpJPEG, screen.EncodeJPEG(r.RGBA, stride, r.W, r.H, q)
		return op // lossy tiles are not cached
	}
	slot := uint16(d.next)
	d.next = (d.next + 1) % demoCacheSlots
	if old := d.slots[slot]; old != 0 && d.cache[old] == slot {
		delete(d.cache, old)
	}
	d.cache[sum], d.slots[slot] = slot, sum
	op.Store, op.Slot = true, slot
	return op
}

// shiftUp moves the pixels of r in img up by step rows, like OpCopy.
func shiftUp(img *image.RGBA, r image.Rectangle, step int) {
	row := r.Dx() * 4
	for y := r.Min.Y; y < r.Max.Y-step; y++ {
		dst := img.PixOffset(r.Min.X, y)
		src := img.PixOffset(r.Min.X, y+step)
		copy(img.Pix[dst:dst+row], img.Pix[src:src+row])
	}
}

// drawPanel draws a text-like document scrolled by scroll pixels into r.
func drawPanel(dst *image.RGBA, r image.Rectangle, scroll int) {
	const lineH = 18
	paper := [4]byte{250, 250, 248, 255}
	ink := [4]byte{55, 60, 70, 255}
	link := [4]byte{37, 99, 235, 255}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		docY := scroll + y - r.Min.Y
		line, in := docY/lineH, docY%lineH
		off := dst.PixOffset(r.Min.X, y)
		for x := 0; x < r.Dx(); x++ {
			copy(dst.Pix[off+x*4:], paper[:])
		}
		if in < 5 || in > 12 {
			continue
		}
		rng := uint32(line)*2654435761 + 12345
		next := func() int { rng = rng*1664525 + 1013904223; return int(rng >> 16) }
		end := r.Dx() - 16 - next()%160
		if line%6 == 5 {
			end = 0 // an empty line between paragraphs
		}
		for x := 14; x < end; {
			w := 10 + next()%56
			col := ink
			if next()%11 == 0 {
				col = link
			}
			for i := x; i < min(x+w, end); i++ {
				copy(dst.Pix[off+i*4:], col[:])
			}
			x += w + 7
		}
	}
}

// sinTable is one period of sin in 1024 steps, for drawVideo.
var sinTable = func() (t [1024]float32) {
	for i := range t {
		t[i] = float32(math.Sin(float64(i) * 2 * math.Pi / 1024))
	}
	return
}()

func fsin(v float32) float32 { return sinTable[int(v*1024/(2*math.Pi))&1023] }

// drawVideo draws a moving plasma into r: many colors, so it is coded as JPEG.
func drawVideo(dst *image.RGBA, r image.Rectangle, t float64) {
	ft := float32(math.Mod(t, 2*math.Pi*100))
	for y := r.Min.Y; y < r.Max.Y; y++ {
		fy := float32(y - r.Min.Y)
		off := dst.PixOffset(r.Min.X, y)
		for x := 0; x < r.Dx(); x++ {
			fx := float32(x)
			v := fsin(fx*0.045+ft*1.7) + fsin(fy*0.06-ft*1.3) + fsin((fx+fy)*0.025+ft)
			p := dst.Pix[off+x*4 : off+x*4+4 : off+x*4+4]
			p[0] = byte(128 + 120*fsin(v*1.6))
			p[1] = byte(128 + 120*fsin(v*1.6+2.1))
			p[2] = byte(128 + 120*fsin(v*1.6+4.2))
			p[3] = 255
		}
	}
}

// arrowShape is the demo's arrow cursor: white with a black outline.
func arrowShape() screen.CursorShape {
	poly := [][2]float64{{0, 0}, {0, 16}, {4, 12}, {7, 19}, {9.5, 18}, {6.5, 11}, {12, 11}}
	const w, h = 15, 22
	inside := func(x, y int) bool {
		px, py := float64(x)-1+0.5, float64(y)-1+0.5
		in := false
		for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
			a, b := poly[i], poly[j]
			if (a[1] > py) != (b[1] > py) && px < (b[0]-a[0])*(py-a[1])/(b[1]-a[1])+a[0] {
				in = !in
			}
		}
		return in
	}
	return outlined(demoArrow, w, h, 1, 1, inside, [4]byte{255, 255, 255, 255}, [4]byte{0, 0, 0, 255})
}

// beamShape is the demo's text (I-beam) cursor: black with a white outline.
func beamShape() screen.CursorShape {
	const w, h = 9, 20
	inside := func(x, y int) bool {
		switch {
		case y == 1 || y == h-2:
			return x >= 1 && x <= 7
		case y > 1 && y < h-2:
			return x == 4
		}
		return false
	}
	return outlined(demoBeam, w, h, 4, 10, inside, [4]byte{0, 0, 0, 255}, [4]byte{255, 255, 255, 255})
}

// outlined renders a w x h shape: fill where inside, line on the pixels
// next to it.
func outlined(id uint32, w, h, hotX, hotY int, inside func(x, y int) bool, fill, line [4]byte) screen.CursorShape {
	px := make([]byte, w*h*4)
	for y := range h {
		for x := range w {
			var c [4]byte
			if inside(x, y) {
				c = fill
			} else {
				for _, d := range [][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}, {-1, -1}, {1, 1}, {-1, 1}, {1, -1}} {
					if nx, ny := x+d[0], y+d[1]; nx >= 0 && ny >= 0 && nx < w && ny < h && inside(nx, ny) {
						c = line
						break
					}
				}
			}
			copy(px[(y*w+x)*4:], c[:])
		}
	}
	return screen.CursorShape{ID: id, HotX: hotX, HotY: hotY, W: w, H: h, RGBA: px}
}
