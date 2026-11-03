package views

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/mevdschee/helpbuoy/desk/keymap"
	"github.com/mevdschee/helpbuoy/desk/render"
	"github.com/mevdschee/helpbuoy/desk/session"
	"github.com/mevdschee/helpbuoy/desk/ui"
	"github.com/mevdschee/helpbuoy/proto/input"
	"github.com/mevdschee/helpbuoy/proto/screen"
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
	host     hostFilter
	shape    *screen.CursorShape // last cursor shape the client reported

	mode   viewMode   // Fit or 1:1, per tab
	scroll rl.Vector2 // 1:1 scroll offset in remote pixels
	drag   int        // scrollbar being dragged: 0 none, 1 horizontal, 2 vertical
	dragAt float32    // mouse position where the drag started
	dragOf float32    // scroll offset when the drag started

	tbShown float32   // fullscreen toolbar slide-in, 0 hidden to 1 shown
	tbSeen  time.Time // last time the pointer was at the top edge or on the toolbar
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
// anything the client may still hold. It also forgets a held host key.
func (st *sessionTab) release() {
	st.host.reset()
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

// sendKeys sends key events from the host key filter, keeping track of held
// keys; a release is sent only for a key whose press was sent.
func (st *sessionTab) sendKeys(keys []input.Key) {
	for _, k := range keys {
		switch {
		case k.Down:
			st.s.Send(k)
			st.held[k.Code] = true
		case st.held[k.Code]:
			st.s.Send(k)
			delete(st.held, k.Code)
		}
	}
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

// Fullscreen toolbar timing.
const (
	toolbarReveal = 4             // pointer distance from the top edge that shows the toolbar
	toolbarLinger = time.Second   // how long it stays after the pointer leaves
	toolbarSlide  = 8             // slide speed, full heights per second
	toolbarMaxW   = float32(1180) // the floating toolbar is at most this wide
)

// dpi returns the window's scale: physical pixels per logical pixel.
func dpi() float32 {
	s := rl.GetWindowScaleDPI().X
	if s <= 0 {
		return 1
	}
	return s
}

// drawSession draws a session tab: the toolbar and the remote screen, and
// forwards input while the session has the keyboard and the mouse is over it.
// In fullscreen the toolbar floats over the screen and slides in when the
// pointer touches the top edge.
func (a *App) drawSession(st *sessionTab, area rl.Rectangle) {
	c := a.ui
	scale := dpi()
	var bar, view rl.Rectangle
	if a.fullscreen {
		view = area
		bw := min(area.Width, toolbarMaxW)
		full := rl.NewRectangle(area.X+(area.Width-bw)/2, area.Y, bw, toolbarH+1)
		if rl.IsCursorOnScreen() && (c.Mouse.Y <= area.Y+toolbarReveal || (st.tbShown > 0.5 && c.Hover(full))) {
			st.tbSeen = c.Now
		}
		step := rl.GetFrameTime() * toolbarSlide
		if !st.tbSeen.IsZero() && c.Now.Sub(st.tbSeen) < toolbarLinger {
			st.tbShown = min(st.tbShown+step, 1)
		} else {
			st.tbShown = max(st.tbShown-step, 0)
		}
		bar = rl.NewRectangle(full.X, area.Y-(toolbarH+1)*(1-st.tbShown), bw, toolbarH)
	} else {
		bar = rl.NewRectangle(area.X, area.Y, area.Width, toolbarH)
		view = rl.NewRectangle(area.X, area.Y+toolbarH, area.Width, area.Height-toolbarH)
		st.tbShown, st.tbSeen = 1, time.Time{}
	}
	rl.DrawRectangleRec(view, ui.ColViewer)

	w, h := st.screen.Size()
	l := layoutView(view, w, h, st.mode, scale, st.scroll, !a.fullscreen)
	st.scroll = l.scroll
	c.PushClip(l.port)
	st.screen.Draw(l.dest)
	c.PopClip()
	if !st.screen.Ready() {
		c.TextCentered(ui.Regular, ui.SizeBody, "Waiting for the screen…", view, ui.ColMuted)
	}
	onBars := a.drawScrollbars(st, l, scale)

	overBar := st.tbShown > 0 && c.Hover(bar)
	over := a.forwardInput(st, l, overBar || onBars)
	if over && st.mode == modeOne && st.drag == 0 {
		st.scroll = edgeScroll(l, c.Mouse, rl.GetFrameTime(), scale)
	}
	c.PushClip(l.port)
	a.drawCursor(st, l, over)
	c.PopClip()

	if st.tbShown > 0 {
		if a.fullscreen {
			rl.DrawRectangleRec(rl.NewRectangle(bar.X-1, bar.Y, bar.Width+2, bar.Height+1), ui.ColBorder)
		}
		a.drawToolbar(st, bar)
	}
}

// drawScrollbars draws the 1:1 scrollbars and handles dragging them and
// clicks on their tracks. It reports whether the pointer is on a scrollbar
// or dragging one, so the input does not go to the remote side.
func (a *App) drawScrollbars(st *sessionTab, l viewLayout, scale float32) bool {
	c := a.ui
	if !l.hbar && !l.vbar {
		st.drag = 0
		return false
	}
	p := l.port
	vw, vh := p.Width*scale, p.Height*scale // the view in remote pixels
	cw, ch := vw+l.maxScroll.X, vh+l.maxScroll.Y
	if st.drag != 0 && !c.MouseDown {
		st.drag = 0
	}
	on := st.drag != 0
	draw := func(track rl.Rectangle, horizontal bool) {
		rl.DrawRectangleRec(track, ui.ColPanel)
		length, view, content, off, mouse := track.Height, vh, ch, l.scroll.Y, c.Mouse.Y-track.Y
		if horizontal {
			length, view, content, off, mouse = track.Width, vw, cw, l.scroll.X, c.Mouse.X-track.X
		}
		pos, tl := thumb(length, view, content, off)
		tr := rl.NewRectangle(track.X+pos, track.Y+2, tl, track.Height-4)
		if !horizontal {
			tr = rl.NewRectangle(track.X+2, track.Y+pos, track.Width-4, tl)
		}
		id, which := 2, &st.scroll.Y
		if horizontal {
			id, which = 1, &st.scroll.X
		}
		hover := c.Hover(track)
		col := ui.ColBorder
		if hover || st.drag == id {
			col = ui.ColMuted
			on = true
		}
		rl.DrawRectangleRounded(tr, 1, 4, col)
		if hover && c.MousePressed {
			if mouse < pos || mouse > pos+tl {
				*which = thumbJump(length, view, content, mouse)
				off = *which
			}
			st.drag, st.dragAt, st.dragOf = id, mouse, off
		}
		if st.drag == id {
			*which = thumbDrag(length, view, content, st.dragOf, mouse-st.dragAt)
		}
	}
	if l.hbar {
		draw(rl.NewRectangle(p.X, p.Y+p.Height, p.Width, scrollbarW), true)
	}
	if l.vbar {
		draw(rl.NewRectangle(p.X+p.Width, p.Y, scrollbarW, p.Height), false)
	}
	if l.hbar && l.vbar {
		rl.DrawRectangleRec(rl.NewRectangle(p.X+p.Width, p.Y+p.Height, scrollbarW, scrollbarW), ui.ColPanel)
	}
	return on
}

// forwardInput sends this frame's mouse and keyboard input to the session.
//
// Keys are sent as physical keys (W3C codes) on press, repeat and release,
// so the remote layout produces the characters, dead keys and AltGr included.
// They pass the host key filter first (hostFilter): Right Ctrl with F, S, R
// or H runs a viewer shortcut instead. Characters from GetCharPressed are
// sent as Text only in frames where no mapped key went down or repeated:
// those are characters without a physical key (input methods, compose
// sequences, emoji pickers). In a frame with a key press, the characters
// came from that key and are already on their way.
//
// Mouse moves are sent at most once per frame here and coalesced further by
// the session; buttons, wheel and keys go out at once, in order. blocked is
// set while the pointer is on the toolbar or a scrollbar. It reports whether
// the local pointer is over the remote screen and controls it.
func (a *App) forwardInput(st *sessionTab, l viewLayout, blocked bool) bool {
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

	over := focused && st.screen.Ready() && !blocked && rl.IsCursorOnScreen() && c.Hover(l.visible())
	if over {
		x, y := l.toRemote(c.Mouse)
		m := input.MouseMove{X: x, Y: y}
		if !st.moved || m != st.lastMove {
			st.s.Send(m)
			st.lastMove, st.moved = m, true
		}
		for i, b := range rlButtons {
			if rl.IsMouseButtonPressed(b.rl) {
				st.sendKeys(st.host.flush()) // Right Ctrl+click is a Ctrl+click
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
	var acts []hostAction
	press := func(k int32) {
		code, _ := keymap.Code(k)
		if first := rl.IsKeyPressed(k); first || rl.IsKeyPressedRepeat(k) {
			out, act := st.host.key(code, true, !first)
			st.sendKeys(out)
			if act != actNone {
				acts = append(acts, act)
			}
			pressed = true
		}
	}
	release := func(k int32) {
		if rl.IsKeyReleased(k) {
			code, _ := keymap.Code(k)
			out, _ := st.host.key(code, false, false)
			st.sendKeys(out)
		}
	}
	// Events within one frame come without their order. Take the host key's
	// press first and its release last, so a quick Right Ctrl+F (all four
	// events in one frame) still counts as the shortcut.
	press(rl.KeyRightControl)
	for _, k := range keymap.Keys() {
		if k != rl.KeyRightControl {
			press(k)
			release(k)
		}
	}
	release(rl.KeyRightControl)
	if !pressed {
		for _, r := range c.Chars {
			if r >= 32 && r != 127 {
				st.s.Send(input.Text{Text: string(r)})
			}
		}
	}
	for _, act := range acts {
		a.runHostAction(st, act)
	}
	return over
}

// runHostAction runs a viewer shortcut.
func (a *App) runHostAction(st *sessionTab, act hostAction) {
	switch act {
	case actFullscreen:
		a.setFullscreen(!a.fullscreen)
	case actToggleMode:
		st.toggleMode()
	case actResize:
		a.resizeToClient(st)
	}
}

func (st *sessionTab) toggleMode() {
	if st.mode == modeFit {
		st.mode = modeOne
	} else {
		st.mode = modeFit
	}
}

// cursorDraw is what drawCursor draws.
type cursorDraw int

const (
	cursorNone          cursorDraw = iota
	cursorShapeAtMouse             // the remote shape at the local pointer
	cursorArrowAtMouse             // a plain arrow at the local pointer
	cursorShapeAtRemote            // the remote shape where the client reports it
)

// cursorChoice decides how to show the pointer. While the local pointer
// controls the session (over), the system cursor is hidden, so a pointer is
// always drawn there: the remote shape if one is known, even when the client
// reports its cursor as hidden (touch input, no mouse attached, an app hiding
// it), else a plain arrow. Otherwise the remote cursor is drawn at the
// client's position only when it is visible and its shape is known.
func cursorChoice(over, posKnown, visible, hasShape bool) cursorDraw {
	switch {
	case over && hasShape:
		return cursorShapeAtMouse
	case over:
		return cursorArrowAtMouse
	case posKnown && visible && hasShape:
		return cursorShapeAtRemote
	}
	return cursorNone
}

// drawCursor draws the remote cursor. While the local pointer controls the
// session (over), the system cursor is hidden for this frame and a pointer
// is drawn at the local mouse, so pointer movement has no network latency;
// the remote side still decides the shape (arrow, I-beam, hand). Otherwise
// the shape is drawn where the client reports its cursor, with the same
// transform as the screen.
func (a *App) drawCursor(st *sessionTab, l viewLayout, over bool) {
	w, h := st.screen.Size()
	if !st.screen.Ready() || w <= 0 {
		return
	}
	scale := l.dest.Width / float32(w)
	pos, shape, ok := st.s.Cursor()
	if shape != nil {
		st.shape = shape
	}
	known := shape
	if over && known == nil {
		known = st.shape
	}
	switch cursorChoice(over, ok, pos.Visible, known != nil) {
	case cursorShapeAtMouse:
		a.ui.HideCursor()
		st.cursors.Draw(known, a.ui.Mouse, cursorScale(scale))
	case cursorArrowAtMouse:
		a.ui.HideCursor()
		drawPointer(a.ui.Mouse)
	case cursorShapeAtRemote:
		st.cursors.Draw(shape, l.fromRemote(pos.X, pos.Y, w, h), cursorScale(scale))
	}
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

// viewLabels are the toolbar labels of viewMode values, by value.
var viewLabels = []string{"Fit", "1:1"}

var viewTips = []string{
	"Fit: scale the screen to the window (Right Ctrl+S)",
	"1:1: one remote pixel per screen pixel, scroll at the edges (Right Ctrl+S)",
}

// drawToolbar draws the session toolbar: End session, special keys,
// Refresh, monitor and quality choice, statistics, and on the right the view
// controls: Fit or 1:1, Resize to client and fullscreen. When the toolbar
// is narrow, the statistics go first, then Refresh, then quality, then the
// monitor choice, then Resize.
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
	const resizeLabel = "Resize"
	wEnd, wWin, wAlt := c.ButtonWidth("End session"), c.ButtonWidth("Win"), c.ButtonWidth("Alt+Tab")
	wRefresh, wFs := c.ButtonWidth("Refresh"), c.ButtonWidth(fsLabel)
	wResize := float32(0)
	if !a.fullscreen {
		wResize = c.ButtonWidth(resizeLabel)
	}
	wView := c.SegmentedWidth(viewLabels)
	wQual := c.SegmentedWidth(qualityLabels)
	wMon := float32(0)
	if monLabels != nil {
		wMon = c.SegmentedWidth(monLabels)
	}
	wStats := c.Measure(ui.Regular, ui.SizeSmall, stats).X

	avail := r.Width - 20
	used := wEnd + group + wWin + gap + wAlt + group + wView + gap + wFs
	fits := func(w float32) bool {
		if w == 0 || used+w > avail {
			return false
		}
		used += w
		return true
	}
	showResize := wResize > 0 && fits(wResize+gap)
	showMon := wMon > 0 && fits(wMon+group)
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

	// The view controls, right aligned.
	rx := r.X + r.Width - 10 - wFs
	if c.Button(id+"/fullscreen", rl.NewRectangle(rx, y, wFs, bh), fsLabel, ui.Normal, false) {
		a.setFullscreen(!a.fullscreen)
	}
	if c.Hover(rl.NewRectangle(rx, y, wFs, bh)) {
		c.Tooltip("Fullscreen; move the pointer to the top edge for this toolbar (Right Ctrl+F)")
	}
	if showResize {
		rx -= gap + wResize
		if c.Button(id+"/resize", rl.NewRectangle(rx, y, wResize, bh), resizeLabel, ui.Normal, false) {
			a.resizeToClient(st)
		}
		if c.Hover(rl.NewRectangle(rx, y, wResize, bh)) {
			c.Tooltip("Resize the window to show the remote screen at 1:1 (Right Ctrl+R)")
		}
	}
	rx -= gap + wView
	clicked, hovered := c.Segmented(id+"/view", rx, y, bh, viewLabels, int(st.mode))
	if clicked >= 0 {
		st.mode = viewMode(clicked)
	}
	if hovered >= 0 {
		c.Tooltip(viewTips[hovered])
	}
	sx := rx - group
	if showStats {
		sx -= wStats
		c.TextIn(ui.Regular, ui.SizeSmall, stats, rl.NewRectangle(sx, r.Y, wStats, r.Height), ui.ColMuted)
		if c.Hover(rl.NewRectangle(sx, r.Y, wStats, r.Height)) {
			c.Tooltip("Frames per second, round trip time to the client, and screen data received")
		}
	}

	if sx-x <= 40 {
		return
	}
	name := a.sessionName(st)
	if a.fullscreen {
		c.TextIn(ui.Bold, ui.SizeSmall, c.Ellipsize(ui.Bold, ui.SizeSmall, name, sx-x-16), rl.NewRectangle(x+8, r.Y, sx-x, r.Height), ui.ColText)
	} else if um := hello.UserMachine; um != "" && !strings.EqualFold(um, name) {
		c.TextIn(ui.Regular, ui.SizeSmall, c.Ellipsize(ui.Regular, ui.SizeSmall, um, sx-x-16), rl.NewRectangle(x+8, r.Y, sx-x, r.Height), ui.ColMuted)
		if c.Hover(rl.NewRectangle(x, r.Y, sx-x, r.Height)) {
			c.Tooltip("The user and machine the client reports")
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
