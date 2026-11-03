// Command hmw-bench runs the client's screen encoder against the helper's
// decoder (screen.Decoder) end to end, on synthetic scenarios or on a
// recording made with the client's --record option, and compares the
// results with the targets of PLAN.md 5.1.
//
//	hmw-bench                       all synthetic scenarios
//	hmw-bench -scenario typing,scroll
//	hmw-bench -recording DIR [-class typing|scroll|video|idle|other]
//
// Every decoded frame is checked against the capture: exact outside the
// tiles the encoder reports as lossy, and exact everywhere after the
// pending refines have been sent at the end.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"math"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/mevdschee/helpbuoy/proto/encode"
	"github.com/mevdschee/helpbuoy/proto/encode/scenes"
	"github.com/mevdschee/helpbuoy/proto/screen"
)

func main() {
	var (
		names    = flag.String("scenario", "all", "comma-separated synthetic scenarios, or all")
		recDir   = flag.String("recording", "", "replay this recording directory instead")
		class    = flag.String("class", "other", "target class of the recording: typing, scroll, video, idle or other")
		workers  = flag.Int("workers", 0, "encoder workers (default: all CPUs)")
		linkMbit = flag.Float64("link", 100, "simulated link for the rate controller, Mbit/s")
		rtt      = flag.Duration("rtt", 10*time.Millisecond, "simulated base round-trip time")
		quality  = flag.String("quality", "auto", "quality mode: auto, sharp or fast")
		frames   = flag.Int("frames", 0, "limit frames per scenario")
	)
	flag.Parse()
	cfg := runConfig{workers: *workers, link: *linkMbit * 1e6 / 8, rtt: *rtt, frames: *frames}
	switch *quality {
	case "auto":
	case "sharp":
		cfg.mode = screen.QualitySharp
	case "fast":
		cfg.mode = screen.QualityFast
	default:
		fatalf("unknown quality %q", *quality)
	}

	var sources []source
	if *recDir != "" {
		rec, err := encode.OpenRecording(*recDir)
		if err != nil {
			fatalf("%v", err)
		}
		c := slices.Index([]string{"typing", "scroll", "video", "idle", "other"}, *class)
		if c < 0 {
			fatalf("unknown class %q", *class)
		}
		sources = append(sources, &recSource{rec: rec, class: scenes.Class(c), name: "recording"})
	} else {
		for _, sc := range scenes.All() {
			if *names == "all" || slices.Contains(strings.Split(*names, ","), sc.Name) {
				sources = append(sources, &sceneSource{sc: sc})
			}
		}
		if len(sources) == 0 {
			fatalf("no scenario matches %q", *names)
		}
	}

	var results []result
	for _, s := range sources {
		r, err := run(s, cfg)
		if err != nil {
			fatalf("%s: %v", s.info().name, err)
		}
		results = append(results, r)
	}
	printResults(results)
	if !printTargets(results) {
		os.Exit(1)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "hmw-bench: "+format+"\n", args...)
	os.Exit(2)
}

// source is a sequence of captures.
type source interface {
	info() sourceInfo
	frame(i int, dst *image.RGBA) (*image.RGBA, time.Duration, error)
}

type sourceInfo struct {
	name   string
	class  scenes.Class
	frames int
	w, h   int
	bgra   bool
	fps    float64 // capture rate
}

type sceneSource struct {
	sc scenes.Scenario
	pl scenes.Player
}

func (s *sceneSource) info() sourceInfo {
	return sourceInfo{name: s.sc.Name, class: s.sc.Class, frames: s.sc.Frames, w: s.sc.Width, h: s.sc.Height, fps: float64(s.sc.FPS)}
}

func (s *sceneSource) frame(i int, dst *image.RGBA) (*image.RGBA, time.Duration, error) {
	if i == 0 {
		s.pl = s.sc.New()
	}
	if dst == nil {
		dst = image.NewRGBA(image.Rect(0, 0, s.sc.Width, s.sc.Height))
	}
	s.pl.Frame(i, dst)
	return dst, time.Duration(i) * time.Second / time.Duration(s.sc.FPS), nil
}

type recSource struct {
	rec   *encode.Recording
	class scenes.Class
	name  string
}

func (s *recSource) info() sourceInfo {
	n := len(s.rec.Frames)
	fps := 30.0
	if n > 1 && s.rec.Frames[n-1].T > 0 {
		fps = float64(n-1) / s.rec.Frames[n-1].T.Seconds()
	}
	return sourceInfo{name: s.name, class: s.class, frames: n, w: s.rec.Width, h: s.rec.Height, bgra: s.rec.BGRA, fps: fps}
}

func (s *recSource) frame(i int, dst *image.RGBA) (*image.RGBA, time.Duration, error) {
	img, err := s.rec.Load(i, dst)
	return img, s.rec.Frames[i].T, err
}

type runConfig struct {
	workers int
	link    float64 // bytes per second
	rtt     time.Duration
	frames  int
	mode    screen.QualityMode
}

type result struct {
	sourceInfo
	enc, dec     []time.Duration // per frame
	bytes        []int           // per frame
	stats        encode.Stats
	area         map[screen.OpKind]int // pixels drawn per op kind
	lossyFrames  int                   // frames with lossy tiles pending
	minPSNR      float64               // worst PSNR of lossy tiles
	refineFrames int
	refineBytes  int
	exactAtEnd   bool
}

func run(s source, cfg runConfig) (result, error) {
	inf := s.info()
	if cfg.frames > 0 {
		inf.frames = min(inf.frames, cfg.frames)
	}
	res := result{sourceInfo: inf, area: map[screen.OpKind]int{}, minPSNR: math.Inf(1)}
	e := encode.New(encode.Config{SwapRB: inf.bgra, Workers: cfg.workers})
	e.SetQuality(cfg.mode)
	dec := screen.NewDecoder()
	helper := image.NewRGBA(image.Rect(0, 0, inf.w, inf.h))
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var cur *image.RGBA
	var seq uint32
	var now time.Time

	step := func(now time.Time) (int, time.Duration, time.Duration, error) {
		start := time.Now()
		parts := e.Encode(cur, now)
		encTime := time.Since(start)
		seq++
		size := 0
		start = time.Now()
		for _, f := range parts {
			f.Seq = seq
			for _, r := range f.Rects {
				size += r.EncodedSize()
				res.area[r.Kind] += r.W * r.H
			}
			if _, _, err := dec.Apply(helper, f); err != nil {
				return 0, 0, 0, fmt.Errorf("frame %d: decode: %w", seq, err)
			}
		}
		decTime := time.Since(start)
		if size > 0 {
			size += 9 * len(parts)
			// Simulated link: transfer time on top of the base rtt.
			e.Acked(size, cfg.rtt+time.Duration(float64(size)/cfg.link*float64(time.Second)))
		}
		return size, encTime, decTime, nil
	}

	for i := range inf.frames {
		var t time.Duration
		var err error
		if cur, t, err = s.frame(i, cur); err != nil {
			return res, err
		}
		now = t0.Add(t)
		size, et, dt, err := step(now)
		if err != nil {
			return res, err
		}
		res.enc = append(res.enc, et)
		res.dec = append(res.dec, dt)
		res.bytes = append(res.bytes, size)
		lossy := e.LossyRects()
		if len(lossy) > 0 {
			res.lossyFrames++
		}
		psnr, err := compare(helper, cur, inf.bgra, lossy)
		if err != nil {
			return res, fmt.Errorf("frame %d: %w", i, err)
		}
		res.minPSNR = min(res.minPSNR, psnr)
	}
	res.stats = e.Stats()
	// Let the pending refines go out, then the image must be exact.
	for k := 0; !e.RefineDue().IsZero() && k < 1000; k++ {
		now = maxTime(now.Add(time.Millisecond), e.RefineDue())
		size, _, _, err := step(now)
		if err != nil {
			return res, err
		}
		res.refineFrames++
		res.refineBytes += size
	}
	_, err := compare(helper, cur, inf.bgra, nil)
	res.exactAtEnd = err == nil && len(e.LossyRects()) == 0
	return res, nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// compare checks the helper's image against the capture: equal (ignoring
// the capture's alpha) outside the lossy rectangles. It returns the PSNR
// over the lossy rectangles (+Inf if there are none or they are exact).
func compare(helper, cur *image.RGBA, bgra bool, lossy []image.Rectangle) (float64, error) {
	w, h := cur.Rect.Dx(), cur.Rect.Dy()
	inLossy := func(x, y int) bool {
		for _, r := range lossy {
			if image.Pt(x, y).In(r) {
				return true
			}
		}
		return false
	}
	var se float64
	var n int
	want := make([]byte, w*4)
	for y := range h {
		src := cur.Pix[cur.PixOffset(cur.Rect.Min.X, cur.Rect.Min.Y+y):][:w*4]
		for i := 0; i < len(src); i += 4 {
			if bgra {
				want[i], want[i+1], want[i+2] = src[i+2], src[i+1], src[i]
			} else {
				copy(want[i:i+3], src[i:i+3])
			}
			want[i+3] = 255
		}
		got := helper.Pix[y*helper.Stride:][:w*4]
		if bytes.Equal(want, got) {
			continue
		}
		for x := range w {
			o := x * 4
			if bytes.Equal(want[o:o+4], got[o:o+4]) {
				continue
			}
			if !inLossy(x, y) {
				return 0, fmt.Errorf("pixel %d,%d is %v, want %v outside lossy tiles", x, y, got[o:o+4], want[o:o+4])
			}
			for c := range 3 {
				d := float64(want[o+c]) - float64(got[o+c])
				se += d * d
			}
			n++
		}
	}
	if n == 0 {
		return math.Inf(1), nil
	}
	area := 0
	for _, r := range lossy {
		area += r.Dx() * r.Dy()
	}
	mse := se / float64(3*area)
	return 10 * math.Log10(255*255/mse), nil
}

func mean(ds []time.Duration) time.Duration {
	var s time.Duration
	for _, d := range ds {
		s += d
	}
	return s / time.Duration(max(1, len(ds)))
}

func p95(ds []time.Duration) time.Duration {
	s := slices.Clone(ds)
	slices.Sort(s)
	return s[min(len(s)-1, len(s)*95/100)]
}

func ms(d time.Duration) string { return fmt.Sprintf("%.2f", d.Seconds()*1000) }

// achievedFPS is the frame rate encoder plus decoder sustain back to back.
func (r result) achievedFPS() float64 {
	var total time.Duration
	for i := range r.enc {
		total += r.enc[i] + r.dec[i]
	}
	return float64(len(r.enc)) / total.Seconds()
}

func (r result) meanBytes() float64 {
	s := 0
	for _, b := range r.bytes {
		s += b
	}
	return float64(s) / float64(max(1, len(r.bytes)))
}

// steadyBytes is the mean frame size without the first (full) frame.
func (r result) steadyBytes() float64 {
	if len(r.bytes) < 2 {
		return r.meanBytes()
	}
	s := 0
	for _, b := range r.bytes[1:] {
		s += b
	}
	return float64(s) / float64(len(r.bytes)-1)
}

func (r result) mbits() float64 { return r.steadyBytes() * r.fps * 8 / 1e6 }

// copyCacheShare is the share of the updated area (after the first frame)
// drawn by copies and cache refs.
func (r result) copyCacheShare() float64 {
	total := 0
	for _, a := range r.area {
		total += a
	}
	total -= r.w * r.h // the first, full frame
	if total <= 0 {
		return 0
	}
	return float64(r.area[screen.OpCopy]+r.area[screen.OpCacheRef]) / float64(total)
}

func printResults(rs []result) {
	fmt.Printf("%-10s %6s %5s %8s %8s %8s %8s %8s %10s %9s %6s  %-38s %s\n",
		"scenario", "frames", "rate", "fps", "enc ms", "enc p95", "dec ms", "dec p95", "KB/frame", "Mbit/s", "q/fps", "ops solid/pal/jpeg/raw/copy/ref/refined", "check")
	for _, r := range rs {
		s := r.stats
		check := "exact"
		if r.lossyFrames > 0 {
			check = fmt.Sprintf("lossy %d frames, min PSNR %.1f dB", r.lossyFrames, r.minPSNR)
		}
		if r.exactAtEnd {
			check += fmt.Sprintf(", exact after %d refine frames (%d KB)", r.refineFrames, r.refineBytes>>10)
		} else {
			check += ", NOT exact after refine"
		}
		fmt.Printf("%-10s %6d %5.0f %8.1f %8s %8s %8s %8s %10.1f %9.2f %6s  %-38s %s\n",
			r.name, len(r.enc), r.fps, r.achievedFPS(), ms(mean(r.enc)), ms(p95(r.enc)), ms(mean(r.dec)), ms(p95(r.dec)),
			r.steadyBytes()/1024, r.mbits(), fmt.Sprintf("%d/%d", s.JPEGQuality, s.TargetFPS),
			fmt.Sprintf("%d/%d/%d/%d/%d/%d/%d", s.Solid, s.Palette, s.JPEG, s.Raw, s.Copy, s.CacheRef, s.Refined), check)
	}
	fmt.Println("\nfps: frames per second of encoder + decoder back to back; KB/frame and Mbit/s exclude the first (full) frame;")
	fmt.Println("Mbit/s at the scenario's capture rate; q/fps: the rate controller's JPEG quality and target fps at the end.")
}

// printTargets compares with PLAN.md 5.1 and reports whether all passed.
func printTargets(rs []result) bool {
	fmt.Println("\nPLAN.md 5.1 targets:")
	ok := true
	line := func(name, target string, pass bool, got string) {
		v := "PASS"
		if !pass {
			v, ok = "FAIL", false
		}
		fmt.Printf("  %-4s %-10s %-48s %s\n", v, name, target, got)
	}
	for _, r := range rs {
		lat := p95(sum(r.enc, r.dec))
		switch r.class {
		case scenes.ClassTyping:
			line(r.name, "60 fps at 1080p", r.achievedFPS() >= 60, fmt.Sprintf("%.0f fps", r.achievedFPS()))
			line(r.name, "<= 1 Mbit/s typical", r.mbits() <= 1, fmt.Sprintf("%.2f Mbit/s", r.mbits()))
			line(r.name, "text pixel-exact (no lossy frame)", r.lossyFrames == 0, fmt.Sprintf("%d lossy frames", r.lossyFrames))
			line(r.name, "input to visible <= 30 ms + RTT (enc+dec p95)", lat <= 30*time.Millisecond,
				fmt.Sprintf("%s ms, excluding capture and transfer", ms(lat)))
		case scenes.ClassScroll:
			line(r.name, ">= 30 fps at 1080p", r.achievedFPS() >= 30, fmt.Sprintf("%.0f fps", r.achievedFPS()))
			line(r.name, "mostly copy-rect and cache hits (> 50% area)", r.copyCacheShare() > 0.5,
				fmt.Sprintf("%.0f%% of updated area", 100*r.copyCacheShare()))
		case scenes.ClassVideo:
			line(r.name, ">= 20 fps full-screen video", r.achievedFPS() >= 20, fmt.Sprintf("%.0f fps", r.achievedFPS()))
			line(r.name, "lossy (JPEG) under load", r.stats.JPEG > 0, fmt.Sprintf("%d JPEG ops, quality now %d", r.stats.JPEG, r.stats.JPEGQuality))
		case scenes.ClassIdle:
			cpu := mean(r.enc[1:]).Seconds() * r.fps * 100
			line(r.name, "< 2% of a core while idle (encoder only)", cpu < 2,
				fmt.Sprintf("%.2f%% at %.0f Hz, %s ms per check", cpu, r.fps, ms(mean(r.enc[1:]))))
		}
		line(r.name, "pixel-exact after refine", r.exactAtEnd, "")
	}
	return ok
}

func sum(a, b []time.Duration) []time.Duration {
	out := make([]time.Duration, len(a))
	for i := range a {
		out[i] = a[i] + b[i]
	}
	return out
}
