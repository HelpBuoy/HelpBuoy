package views

import (
	"math"

	rl "github.com/gen2brain/raylib-go/raylib"
)

// viewMode is how a session tab shows the remote screen.
type viewMode int

const (
	// modeFit scales the remote screen to fit the view, keeping its aspect ratio.
	modeFit viewMode = iota
	// modeOne draws the remote screen pixel for pixel: one remote pixel is one
	// physical pixel of the local screen. A screen larger than the view
	// scrolls, smaller ones are centered.
	modeOne
)

// Sizes of the 1:1 scrolling aids, in logical pixels.
const (
	scrollbarW   = 10   // thickness of a scrollbar
	minThumb     = 24   // smallest scrollbar thumb
	edgeZone     = 16   // distance from a view edge where auto-scroll starts
	edgeMaxSpeed = 1600 // auto-scroll speed at the very edge, logical px per second
)

// viewLayout is where the remote screen goes in a view for one frame.
type viewLayout struct {
	// port is the part of the view that shows the remote screen: the view
	// minus the scrollbars.
	port rl.Rectangle
	// dest is the whole remote screen in window coordinates. In 1:1 with
	// scrolling it extends beyond port; drawing is clipped to port.
	dest rl.Rectangle
	// scroll is the offset of port into the remote screen, in remote pixels,
	// clamped to the valid range.
	scroll rl.Vector2
	// hbar and vbar report whether the horizontal and vertical scrollbars
	// are shown; they lie along the bottom and right of the view.
	hbar, vbar bool
	// maxScroll is the largest scroll offset per axis, in remote pixels.
	maxScroll rl.Vector2
}

// layoutView places a remote screen of w x h pixels in view. dpi is the
// window's scale (physical pixels per logical pixel), used in 1:1 mode so one
// remote pixel covers one physical pixel. scroll is the wanted offset in
// remote pixels; bars allows scrollbars (windowed 1:1; fullscreen relies on
// edge auto-scroll only).
func layoutView(view rl.Rectangle, w, h int, mode viewMode, dpi float32, scroll rl.Vector2, bars bool) viewLayout {
	l := viewLayout{port: view}
	if w <= 0 || h <= 0 || view.Width <= 0 || view.Height <= 0 {
		l.dest = rl.NewRectangle(view.X, view.Y, 0, 0)
		return l
	}
	if dpi <= 0 {
		dpi = 1
	}
	if mode == modeFit {
		scale := min(view.Width/float32(w), view.Height/float32(h))
		dw, dh := float32(w)*scale, float32(h)*scale
		l.dest = rl.NewRectangle(view.X+(view.Width-dw)/2, view.Y+(view.Height-dh)/2, dw, dh)
		return l
	}

	lw, lh := float32(w)/dpi, float32(h)/dpi // remote size in logical pixels
	if bars {
		// A bar along one axis takes room from the other, which may then need
		// a bar too.
		l.hbar = lw > view.Width
		l.vbar = lh > view.Height
		if l.hbar && !l.vbar {
			l.vbar = lh > view.Height-scrollbarW
		}
		if l.vbar && !l.hbar {
			l.hbar = lw > view.Width-scrollbarW
		}
		if l.vbar {
			l.port.Width = max(view.Width-scrollbarW, 0)
		}
		if l.hbar {
			l.port.Height = max(view.Height-scrollbarW, 0)
		}
	}
	p := l.port
	l.maxScroll = rl.NewVector2(max(float32(w)-p.Width*dpi, 0), max(float32(h)-p.Height*dpi, 0))
	l.scroll = rl.NewVector2(clampf(scroll.X, 0, l.maxScroll.X), clampf(scroll.Y, 0, l.maxScroll.Y))
	// Round the scroll to whole remote pixels and the origin to the physical
	// pixel grid, so texels map exactly onto screen pixels.
	l.scroll.X, l.scroll.Y = float32(math.Round(float64(l.scroll.X))), float32(math.Round(float64(l.scroll.Y)))
	x := p.X - l.scroll.X/dpi
	if l.maxScroll.X == 0 {
		x = p.X + (p.Width-lw)/2
	}
	y := p.Y - l.scroll.Y/dpi
	if l.maxScroll.Y == 0 {
		y = p.Y + (p.Height-lh)/2
	}
	l.dest = rl.NewRectangle(snapTo(x, dpi), snapTo(y, dpi), lw, lh)
	return l
}

// toRemote maps a window position to normalized remote coordinates
// (0..65535 across the remote screen), as input.MouseMove wants them.
func (l viewLayout) toRemote(p rl.Vector2) (x, y uint16) {
	return normalize(p.X-l.dest.X, l.dest.Width), normalize(p.Y-l.dest.Y, l.dest.Height)
}

// fromRemote maps a remote pixel position to window coordinates.
func (l viewLayout) fromRemote(px, py, w, h int) rl.Vector2 {
	if w <= 0 || h <= 0 {
		return rl.NewVector2(l.dest.X, l.dest.Y)
	}
	return rl.NewVector2(l.dest.X+float32(px)*l.dest.Width/float32(w), l.dest.Y+float32(py)*l.dest.Height/float32(h))
}

// visible is the part of the remote screen that is on screen.
func (l viewLayout) visible() rl.Rectangle { return intersectRect(l.dest, l.port) }

// edgeSpeed is the auto-scroll speed along one axis for a pointer at pos in
// a view spanning lo..hi: -1 at the low edge to 0 at edgeZone from it, and
// likewise 0 to +1 toward the high edge. Outside the view it is 0.
func edgeSpeed(pos, lo, hi float32) float32 {
	if pos < lo || pos > hi || hi-lo < 2*edgeZone {
		return 0
	}
	if d := pos - lo; d < edgeZone {
		return -(edgeZone - d) / edgeZone
	}
	if d := hi - pos; d < edgeZone {
		return (edgeZone - d) / edgeZone
	}
	return 0
}

// edgeScroll returns the new scroll offset (remote pixels) after dt seconds
// of edge auto-scroll with the pointer at mouse over port.
func edgeScroll(l viewLayout, mouse rl.Vector2, dt, dpi float32) rl.Vector2 {
	s := l.scroll
	if dpi <= 0 {
		dpi = 1
	}
	if l.maxScroll.X > 0 {
		s.X += edgeSpeed(mouse.X, l.port.X, l.port.X+l.port.Width) * edgeMaxSpeed * dt * dpi
	}
	if l.maxScroll.Y > 0 {
		s.Y += edgeSpeed(mouse.Y, l.port.Y, l.port.Y+l.port.Height) * edgeMaxSpeed * dt * dpi
	}
	return rl.NewVector2(clampf(s.X, 0, l.maxScroll.X), clampf(s.Y, 0, l.maxScroll.Y))
}

// thumb returns the position and length of a scrollbar thumb along a track
// of length track, for a view of length view into content of length
// content scrolled by off (view, content and off in the same unit).
func thumb(track, view, content, off float32) (pos, length float32) {
	if content <= view || track <= 0 {
		return 0, track
	}
	length = min(max(track*view/content, minThumb), track)
	pos = (track - length) * clampf(off, 0, content-view) / (content - view)
	return pos, length
}

// thumbDrag returns the scroll offset after dragging a thumb by delta (track
// units) from where it was at offset start.
func thumbDrag(track, view, content, start, delta float32) float32 {
	if content <= view {
		return 0
	}
	_, length := thumb(track, view, content, start)
	free := track - length
	if free <= 0 {
		return start
	}
	return clampf(start+delta*(content-view)/free, 0, content-view)
}

// thumbJump returns the scroll offset that centers the thumb at at (track
// units), for a click on the track outside the thumb.
func thumbJump(track, view, content, at float32) float32 {
	if content <= view {
		return 0
	}
	_, length := thumb(track, view, content, 0)
	free := track - length
	if free <= 0 {
		return 0
	}
	return clampf((at-length/2)*(content-view)/free, 0, content-view)
}

// resizeTarget is the window size Resize to client asks for.
type resizeTarget struct {
	w, h int  // window size in window units (what rl.SetWindowSize takes)
	fits bool // false if clamped to the monitor: the screen needs scrolling
}

// resizeToClient computes the window size that shows a remote screen of
// rw x rh pixels at 1:1. chromeW and chromeH are the logical sizes around the
// view (client list, tab strip, toolbar); dpi converts remote pixels to
// logical pixels and units converts logical pixels to window units (they
// differ on platforms where the window size is in physical pixels). The
// result is clamped to the monitor (in window units) minus room for the
// window decorations, and kept at least minW x minH.
func resizeToClient(rw, rh int, dpi, units, chromeW, chromeH float32, monW, monH, minW, minH int) resizeTarget {
	if dpi <= 0 {
		dpi = 1
	}
	if units <= 0 {
		units = 1
	}
	w := int(math.Ceil(float64((float32(rw)/dpi + chromeW) * units)))
	h := int(math.Ceil(float64((float32(rh)/dpi + chromeH) * units)))
	t := resizeTarget{w: w, h: h, fits: true}
	maxW, maxH := monW-decorW, monH-decorH
	if monW > 0 && t.w > maxW {
		t.w, t.fits = maxW, false
	}
	if monH > 0 && t.h > maxH {
		t.h, t.fits = maxH, false
	}
	t.w, t.h = max(t.w, minW), max(t.h, minH)
	return t
}

// Room left on the monitor for the window frame, title bar and task bar when
// a window is resized to its client, in window units.
const (
	decorW = 16
	decorH = 80
)

func clampf(v, lo, hi float32) float32 { return min(max(v, lo), hi) }

// snapTo rounds a logical coordinate to the physical pixel grid.
func snapTo(v, dpi float32) float32 {
	return float32(math.Round(float64(v*dpi))) / dpi
}

func intersectRect(a, b rl.Rectangle) rl.Rectangle {
	x0, y0 := max(a.X, b.X), max(a.Y, b.Y)
	x1, y1 := min(a.X+a.Width, b.X+b.Width), min(a.Y+a.Height, b.Y+b.Height)
	return rl.NewRectangle(x0, y0, max(x1-x0, 0), max(y1-y0, 0))
}
