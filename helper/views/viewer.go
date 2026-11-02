package views

import (
	"fmt"
	"math"
	"sort"
	"strings"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/mevdschee/help-me-win/helper/keymap"
	"github.com/mevdschee/help-me-win/helper/render"
	"github.com/mevdschee/help-me-win/helper/session"
	"github.com/mevdschee/help-me-win/helper/ui"
	"github.com/mevdschee/help-me-win/proto/input"
	"github.com/mevdschee/help-me-win/proto/screen"
)

// sessionTab is the viewer state of one session.
type sessionTab struct {
	s       *session.Session
	screen  render.Screen
	cursors render.Cursors

	held     map[string]bool // keys sent down and not yet up
	buttons  [5]bool         // mouse buttons sent down
	lastMove input.MouseMove
	moved    bool
	toolbar  bool // fullscreen toolbar visible
}

func newSessionTab(s *session.Session) *sessionTab {
	return &sessionTab{s: s, held: map[string]bool{}}
}

func (st *sessionTab) upload() { st.s.Update(st.screen.Upload) }

func (st *sessionTab) close() {
	st.s.Close()
	st.screen.Unload()
	st.cursors.Unload()
}

// release lifts every key and button the remote side thinks is down: key
// and button ups for the ones this side sent down, then ReleaseAll for
// anything the client may still hold.
func (st *sessionTab) release() {
	if len(st.held) == 0 && st.buttons == [5]bool{} {
		return
	}
	codes := make([]string, 0, len(st.held))
	for code := range st.held {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		st.s.Send(input.Key{Code: code, Down: false})
	}
	for i, b := range rlButtons {
		if st.buttons[i] {
			st.s.Send(input.MouseButton{Button: b.in, Down: false})
		}
	}
	st.s.Send(input.ReleaseAll{})
	clear(st.held)
	st.buttons = [5]bool{}
}

// rlButtons maps raylib mouse buttons to input buttons, by index.
var rlButtons = [5]struct {
	rl rl.MouseButton
	in uint8
}{
	{rl.MouseButtonLeft, input.ButtonLeft},
	{rl.MouseButtonRight, input.ButtonRight},
	{rl.MouseButtonMiddle, input.ButtonMiddle},
	{rl.MouseButtonSide, input.ButtonBack},
	{rl.MouseButtonExtra, input.ButtonForward},
}

// drawSession draws a session tab: the toolbar and the remote screen, and
// forwards input while the session has the keyboard and the mouse is over it.
func (a *App) drawSession(st *sessionTab, area rl.Rectangle) {
	c := a.ui
	var bar, view rl.Rectangle
	if a.fullscreen {
		view = area
		bar = rl.NewRectangle(area.X, area.Y, area.Width, toolbarH)
		st.toolbar = c.Mouse.Y < area.Y+6 || (st.toolbar && c.Hover(bar))
	} else {
		bar = rl.NewRectangle(area.X, area.Y, area.Width, toolbarH)
		view = rl.NewRectangle(area.X, area.Y+toolbarH, area.Width, area.Height-toolbarH)
		st.toolbar = true
	}
	rl.DrawRectangleRec(view, ui.ColViewer)

	dest := st.screen.Fit(view)
	st.screen.Draw(dest)
	if !st.screen.Ready() {
		c.TextCentered(ui.Regular, ui.SizeBody, "Waiting for the screen…", view, ui.ColMuted)
	}

	overBar := st.toolbar && c.Hover(bar)
	over := a.forwardInput(st, dest, overBar)
	c.PushClip(view)
	a.drawCursor(st, dest, over)
	c.PopClip()

	if st.toolbar {
		a.drawToolbar(st, bar)
	}
}

// forwardInput sends this frame's mouse and keyboard input to the session.
//
// Keys are sent as physical keys (W3C codes) on press, repeat and release,
// so the remote layout produces the characters, dead keys and AltGr included.
// Characters from GetCharPressed are sent as Text only in frames where no
// mapped key went down or repeated: those are characters without a physical
// key (input methods, compose sequences, emoji pickers). In a frame with a
// key press, the characters came from that key and are already on their way.
//
// Mouse moves are sent at most once per frame here and coalesced further by
// the session; buttons, wheel and keys go out at once, in order. It reports
// whether the local pointer is over the remote screen and controls it.
func (a *App) forwardInput(st *sessionTab, dest rl.Rectangle, overBar bool) bool {
	c := a.ui
	focused := rl.IsWindowFocused()
	keyboard := focused && (c.Focus == "" || c.Focus == a.viewID(st))
	if a.keysTo != nil && a.keysTo != st {
		a.keysTo.release()
	}
	if !keyboard {
		st.release()
		a.keysTo = nil
	} else {
		a.keysTo = st
	}

	over := focused && st.screen.Ready() && !overBar && c.Hover(dest)
	if over {
		c.HideCursor()
		x := normalize(c.Mouse.X-dest.X, dest.Width)
		y := normalize(c.Mouse.Y-dest.Y, dest.Height)
		m := input.MouseMove{X: x, Y: y}
		if !st.moved || m != st.lastMove {
			st.s.Send(m)
			st.lastMove, st.moved = m, true
		}
		for i, b := range rlButtons {
			if rl.IsMouseButtonPressed(b.rl) {
				st.s.Send(input.MouseButton{Button: b.in, Down: true})
				st.buttons[i] = true
				c.ClaimFocus(a.viewID(st))
			}
		}
		if wv := c.Wheel; wv.X != 0 || wv.Y != 0 {
			st.s.Send(wheel(wv))
		}
	}
	for i, b := range rlButtons {
		if st.buttons[i] && !rl.IsMouseButtonDown(b.rl) {
			st.s.Send(input.MouseButton{Button: b.in, Down: false})
			st.buttons[i] = false
		}
	}

	if !keyboard {
		return over
	}
	pressed := false
	for _, k := range keymap.Keys() {
		code, _ := keymap.Code(k)
		if rl.IsKeyPressed(k) || rl.IsKeyPressedRepeat(k) {
			st.s.Send(input.Key{Code: code, Down: true})
			st.held[code] = true
			pressed = true
		}
		if rl.IsKeyReleased(k) && st.held[code] {
			st.s.Send(input.Key{Code: code, Down: false})
			delete(st.held, code)
		}
	}
	if !pressed {
		for _, r := range c.Chars {
			if r >= 32 && r != 127 {
				st.s.Send(input.Text{Text: string(r)})
			}
		}
	}
	return over
}

// drawCursor draws the remote cursor. While the local pointer controls the
// session (over), the system cursor is hidden and the remote shape is drawn
// at the local pointer, so pointer movement has no network latency; the
// remote side still decides the shape (arrow, I-beam, hand). Otherwise the
// shape is drawn where the client reports its cursor, scaled with the view.
func (a *App) drawCursor(st *sessionTab, dest rl.Rectangle, over bool) {
	w, _ := st.screen.Size()
	if !st.screen.Ready() || w <= 0 {
		return
	}
	scale := dest.Width / float32(w)
	pos, shape, ok := st.s.Cursor()
	if over {
		switch {
		case ok && !pos.Visible:
			// The remote side hides its cursor (typing, a game): so do we.
		case shape != nil:
			st.cursors.Draw(shape, a.ui.Mouse, cursorScale(scale))
		default:
			drawPointer(a.ui.Mouse) // no shape (yet): a plain arrow
		}
		return
	}
	if !ok || !pos.Visible || shape == nil {
		return
	}
	p := rl.NewVector2(dest.X+float32(pos.X)*scale, dest.Y+float32(pos.Y)*scale)
	st.cursors.Draw(shape, p, cursorScale(scale))
}

// cursorScale is the view scale for cursor shapes, kept large enough that a
// cursor on a strongly reduced view is still easy to see.
func cursorScale(viewScale float32) float32 { return max(viewScale, 0.6) }

// wheel converts a raylib wheel move to an input.Wheel in 1/120 notches.
// raylib (GLFW) reports positive Y for scrolling away from the user, which
// matches input.Wheel, and positive X for scrolling left, which is negated.
func wheel(v rl.Vector2) input.Wheel {
	return input.Wheel{DX: notches(-v.X), DY: notches(v.Y)}
}

func (a *App) viewID(st *sessionTab) string { return fmt.Sprintf("view/%p", st) }

// normalize maps a position within length to 0..65535.
func normalize(v, length float32) uint16 {
	if length <= 1 {
		return 0
	}
	f := v / (length - 1)
	f = min(max(f, 0), 1)
	return uint16(math.Round(float64(f) * 65535))
}

// notches converts raylib wheel steps to 1/120 notches.
func notches(v float32) int16 {
	n := math.Round(float64(v) * 120)
	return int16(min(max(n, math.MinInt16), math.MaxInt16))
}

// drawPointer draws a local arrow pointer at p, used while the system
// cursor is hidden over the remote screen.
func drawPointer(p rl.Vector2) {
	shape := [][2]float32{{0, 0}, {0, 16}, {4, 12}, {7, 19}, {9.5, 18}, {6.5, 11}, {12, 11}}
	pts := make([]rl.Vector2, len(shape))
	for i, s := range shape {
		pts[i] = rl.NewVector2(p.X+s[0], p.Y+s[1])
	}
	// Fill as two triangles plus the tail, then outline.
	white := rl.White
	rl.DrawTriangle(pts[0], pts[1], pts[6], white)
	rl.DrawTriangle(pts[2], pts[3], pts[5], white)
	rl.DrawTriangle(pts[3], pts[4], pts[5], white)
	for i := range pts {
		rl.DrawLineEx(pts[i], pts[(i+1)%len(pts)], 1.2, rl.Black)
	}
}

// qualityLabels are the toolbar labels of screen.QualityMode values, by value.
var qualityLabels = []string{"Auto", "Sharp", "Fast"}

var qualityTips = []string{
	"Auto: the client balances sharpness and frame rate",
	"Sharp: lossless text and images, at a lower frame rate",
	"Fast: higher frame rate, softer images",
}

// drawToolbar draws the session toolbar: End session, special keys,
// Refresh, monitor and quality choice, statistics and fullscreen. When the
// toolbar is narrow, the statistics go first, then Refresh, then quality.
func (a *App) drawToolbar(st *sessionTab, r rl.Rectangle) {
	c := a.ui
	rl.DrawRectangleRec(r, ui.ColPanel)
	rl.DrawLineV(rl.NewVector2(r.X, r.Y+r.Height), rl.NewVector2(r.X+r.Width, r.Y+r.Height), ui.ColBorder)
	id := a.viewID(st)
	y, bh := r.Y+(r.Height-30)/2, float32(30)
	const gap, group = 8, 16

	hello, _ := st.s.Hello()
	var monLabels []string
	if len(hello.Monitors) > 1 {
		for i := range hello.Monitors {
			monLabels = append(monLabels, fmt.Sprint(i+1))
		}
	}
	stats := a.statsText(st)

	fsLabel := "Fullscreen"
	if a.fullscreen {
		fsLabel = "Exit fullscreen"
	}
	wEnd, wWin, wAlt := c.ButtonWidth("End session"), c.ButtonWidth("Win"), c.ButtonWidth("Alt+Tab")
	wRefresh, wFs := c.ButtonWidth("Refresh"), c.ButtonWidth(fsLabel)
	wQual := c.SegmentedWidth(qualityLabels)
	wMon := float32(0)
	if monLabels != nil {
		wMon = c.SegmentedWidth(monLabels)
	}
	wStats := c.Measure(ui.Regular, ui.SizeSmall, stats).X

	avail := r.Width - 20
	used := wEnd + group + wWin + gap + wAlt + group + wFs
	fits := func(w float32) bool {
		if w == 0 || used+w > avail {
			return false
		}
		used += w
		return true
	}
	showMon := fits(wMon + group)
	showQual := fits(wQual + group)
	showRefresh := fits(wRefresh + gap)
	showStats := fits(wStats + group)

	x := r.X + 10
	btn := func(name, label string) bool {
		w := c.ButtonWidth(label)
		hit := c.Button(id+"/"+name, rl.NewRectangle(x, y, w, bh), label, ui.Normal, false)
		x += w + gap
		return hit
	}
	if btn("end", "End session") {
		st.s.Close() // the tab closes when the session is done
	}
	x += group - gap
	if btn("win", "Win") {
		st.s.Send(input.Key{Code: "MetaLeft", Down: true})
		st.s.Send(input.Key{Code: "MetaLeft", Down: false})
	}
	if btn("alttab", "Alt+Tab") {
		for _, k := range []input.Key{{Code: "AltLeft", Down: true}, {Code: "Tab", Down: true}, {Code: "Tab"}, {Code: "AltLeft"}} {
			st.s.Send(k)
		}
	}
	if showRefresh {
		if btn("refresh", "Refresh") {
			st.s.Refresh()
		}
		if c.Hover(rl.NewRectangle(x-wRefresh-gap, y, wRefresh, bh)) {
			c.Tooltip("Get a full, sharp copy of the screen")
		}
	}
	if showMon {
		x += group - gap
		clicked, hovered := c.Segmented(id+"/monitor", x, y, bh, monLabels, hello.Current)
		if clicked >= 0 && clicked != hello.Current {
			st.s.SelectMonitor(clicked)
		}
		if hovered >= 0 {
			c.Tooltip(monitorTip(hello.Monitors[hovered]))
		}
		x += wMon + gap
	}
	if showQual {
		x += group - gap
		q := int(st.s.Quality())
		clicked, hovered := c.Segmented(id+"/quality", x, y, bh, qualityLabels, q)
		if clicked >= 0 && clicked != q {
			st.s.SetQuality(screen.QualityMode(clicked))
		}
		if hovered >= 0 {
			c.Tooltip(qualityTips[hovered])
		}
		x += wQual + gap
	}

	fsX := r.X + r.Width - wFs - 10
	if c.Button(id+"/fullscreen", rl.NewRectangle(fsX, y, wFs, bh), fsLabel, ui.Normal, false) {
		a.setFullscreen(!a.fullscreen)
	}
	sx := fsX - group
	if showStats {
		sx -= wStats
		c.TextIn(ui.Regular, ui.SizeSmall, stats, rl.NewRectangle(sx, r.Y, wStats, r.Height), ui.ColMuted)
		if c.Hover(rl.NewRectangle(sx, r.Y, wStats, r.Height)) {
			c.Tooltip("Frames per second, round trip time to the client, and screen data received")
		}
	}

	if a.fullscreen {
		name := st.s.Info.ClientName
		if sx-x > 40 {
			c.TextIn(ui.Bold, ui.SizeSmall, c.Ellipsize(ui.Bold, ui.SizeSmall, name, sx-x-16), rl.NewRectangle(x+8, r.Y, sx-x, r.Height), ui.ColText)
		}
	} else if other := st.s.NameMismatch(); other != "" && sx-x > 40 {
		msg := "Reports itself as " + other
		c.TextIn(ui.Regular, ui.SizeSmall, c.Ellipsize(ui.Regular, ui.SizeSmall, msg, sx-x-16), rl.NewRectangle(x+8, r.Y, sx-x, r.Height), ui.ColWarn)
		if c.Hover(rl.NewRectangle(x, r.Y, sx-x, r.Height)) {
			c.Tooltip("Registered as " + st.s.Info.ClientName + ": the key may have been copied to another PC or account, or the PC was renamed.")
		}
	}
}

// statsText is the toolbar readout: frame rate, round trip time and the
// screen data rate.
func (a *App) statsText(st *sessionTab) string {
	fps, mbps := st.s.Stats(a.ui.Now)
	parts := []string{fmt.Sprintf("%.0f fps", fps)}
	if rtt := st.s.RTT(); rtt > 0 {
		if ms := rtt.Milliseconds(); ms > 0 {
			parts = append(parts, fmt.Sprintf("%d ms", ms))
		} else {
			parts = append(parts, "<1 ms")
		}
	}
	if mbps < 10 {
		parts = append(parts, fmt.Sprintf("%.1f Mbit/s", mbps))
	} else {
		parts = append(parts, fmt.Sprintf("%.0f Mbit/s", mbps))
	}
	return strings.Join(parts, "  ·  ")
}

// monitorTip describes a monitor for the selector's tooltip.
func monitorTip(m screen.Monitor) string {
	s := fmt.Sprintf("%d x %d", m.W, m.H)
	if m.Name != "" {
		s = m.Name + ", " + s
	}
	if m.Primary {
		s += ", primary"
	}
	return s
}
