package encode

import (
	"math"
	"time"

	"github.com/mevdschee/help-me-win/proto/screen"
)

// rateCtl is the rate controller (PLAN.md 5.5). It estimates the link from
// acks and steers the JPEG quality and the target frame rate so that the
// bytes produced per second stay below what the link delivers, which keeps
// frames from queueing in SSH and TCP buffers.
//
// The model: a frame of B bytes is acked after rtt = base + queue + B/bw.
// base is the smallest rtt seen recently, or, when frame sizes vary enough,
// the intercept of a linear fit of rtt against frame size (so a stream of
// only large frames, as in video, still reveals its transfer time).
// Anything above base is transfer time plus queueing. Samples of B/(rtt-base) give the bandwidth, and a
// queueing delay well above base says the link is saturated.
type rateCtl struct {
	mode       screen.QualityMode
	bw         float64 // bytes per second
	srtt       time.Duration
	minRTT     time.Duration
	minAge     int     // acks since minRTT was taken
	frameBytes float64 // moving average of the size of sent frames
	quality    float64
	fps        float64

	// Moving averages for the fit of rtt against frame size.
	n                int
	mb, mr, mbb, mbr float64
}

const (
	initialBW  = 50e6 / 8 // 50 Mbit/s until acks say otherwise
	minBW      = 64 << 10
	maxBW      = 10e9 / 8
	minRTTLife = 600 // acks; about 10 s at 60 fps
)

func newRateCtl() rateCtl {
	return rateCtl{bw: initialBW, quality: 75, fps: 60}
}

// limits returns the quality and frame rate ranges of the mode.
// QualitySharp keeps JPEG quality high and gives up frame rate first;
// QualityFast gives up quality first.
func (r *rateCtl) limits() (qmin, qmax, fmin, fmax float64) {
	switch r.mode {
	case screen.QualitySharp:
		return 75, 95, 5, 30
	case screen.QualityFast:
		return 25, 70, 10, 60
	default:
		return 40, 90, 5, 60
	}
}

func (r *rateCtl) setMode(m screen.QualityMode) {
	r.mode = m
	r.clamp()
}

func (r *rateCtl) clamp() {
	qmin, qmax, fmin, fmax := r.limits()
	if r.srtt > 0 {
		// At most two frames are in flight, so more than 2/rtt frames per
		// second can't be acked.
		fmax = max(fmin, min(fmax, 2/r.srtt.Seconds()))
	}
	r.quality = min(max(r.quality, qmin), qmax)
	r.fps = min(max(r.fps, fmin), fmax)
}

// sent records the size of a frame that was produced.
func (r *rateCtl) sent(bytes int) {
	if r.frameBytes == 0 {
		r.frameBytes = float64(bytes)
		return
	}
	r.frameBytes += 0.1 * (float64(bytes) - r.frameBytes)
}

// acked updates the link estimate with one ack and adapts quality and fps.
func (r *rateCtl) acked(bytes int, rtt time.Duration) {
	if rtt <= 0 {
		rtt = time.Microsecond
	}
	if r.srtt == 0 {
		r.srtt = rtt
	} else {
		r.srtt += (rtt - r.srtt) / 8
	}
	r.minAge++
	if r.minRTT == 0 || rtt < r.minRTT || r.minAge > minRTTLife {
		r.minRTT = rtt
		r.minAge = 0
	}
	base := r.minRTT
	if fb, ok := r.fit(bytes, rtt); ok {
		base = min(base, fb)
	}
	queue := rtt - base
	if bytes >= 8<<10 {
		if queue >= time.Millisecond {
			r.bw += 0.2 * (float64(bytes)/queue.Seconds() - r.bw)
		} else if s := float64(bytes) / rtt.Seconds(); s > r.bw {
			// No queueing visible: the frame took at most rtt, so this
			// is only a lower bound.
			r.bw += 0.2 * (s - r.bw)
		}
		r.bw = min(max(r.bw, minBW), maxBW)
	}

	// The frame's own transfer time is not queueing.
	queue -= time.Duration(float64(bytes) / r.bw * float64(time.Second))
	congested := queue > max(15*time.Millisecond, base/2)
	budget := 0.7 * r.bw
	demand := r.frameBytes * r.fps
	switch {
	case congested || demand > budget:
		r.decrease()
	case demand < 0.5*budget:
		r.increase()
	}
	r.clamp()
}

// fit adds a sample to the fit rtt = base + bytes*slope and returns base
// once the sizes vary enough for the fit to mean something.
func (r *rateCtl) fit(bytes int, rtt time.Duration) (time.Duration, bool) {
	const a = 0.05
	b, t := float64(bytes), rtt.Seconds()
	if r.n == 0 {
		r.mb, r.mr, r.mbb, r.mbr = b, t, b*b, b*t
	} else {
		r.mb += a * (b - r.mb)
		r.mr += a * (t - r.mr)
		r.mbb += a * (b*b - r.mbb)
		r.mbr += a * (b*t - r.mbr)
	}
	r.n++
	vb := r.mbb - r.mb*r.mb
	cov := r.mbr - r.mb*r.mr
	if r.n < 8 || vb <= (0.05*r.mb)*(0.05*r.mb) || cov <= 0 {
		return 0, false
	}
	base := r.mr - cov/vb*r.mb
	return time.Duration(max(base, 0) * float64(time.Second)), true
}

func (r *rateCtl) decrease() {
	qmin, _, fmin, _ := r.limits()
	switch r.mode {
	case screen.QualityFast:
		if r.quality > qmin {
			r.quality -= 5
		} else {
			r.fps *= 0.9
		}
	case screen.QualitySharp:
		if r.fps > fmin {
			r.fps *= 0.85
		} else {
			r.quality -= 2
		}
	default:
		// Quality first, down to qmin+10, then both.
		r.quality -= 3
		if r.quality < qmin+10 {
			r.fps *= 0.92
		}
	}
}

func (r *rateCtl) increase() {
	switch r.mode {
	case screen.QualityFast:
		r.fps += 4
		r.quality += 0.5
	case screen.QualitySharp:
		r.quality += 2
		r.fps += 1
	default:
		r.quality += 1
		r.fps += 2
	}
}

func (r *rateCtl) jpegQuality() int { return int(math.Round(r.quality)) }
func (r *rateCtl) targetFPS() int   { return int(math.Round(r.fps)) }

// refineBudget is roughly how many bytes of refine one frame may carry: a
// frame interval's worth of the estimated bandwidth, at least 256 KiB.
func (r *rateCtl) refineBudget() int {
	return max(256<<10, int(r.bw/r.fps))
}
