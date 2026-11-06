// Package ui is the computer's window on Linux (PLAN.md 6.2 and 6.3). It
// speaks the X11 protocol directly with jezek/xgb and draws its own
// controls with the Go fonts, so it needs no toolkit and no C libraries.
// Like the Windows window, it only renders the app.View it is given and
// forwards button presses to the app.Controller.
package ui

import (
	"context"
	"image"
	"time"

	"github.com/jezek/xgb/xproto"

	"github.com/helpbuoy/helpbuoy/client-linux/xkeys"
	"github.com/helpbuoy/helpbuoy/client-windows/app"
	"github.com/helpbuoy/helpbuoy/client-windows/session"
	"github.com/helpbuoy/helpbuoy/proto/i18n"
)

// Title is the window title.
const Title = "HelpBuoy"

// Client area size in 96-DPI units, as on Windows.
const clientW, clientH = 460, 330

// contentW is the width of the content between the 24-unit side margins.
const contentW = clientW - 48

// statusY is the position of the waiting screen's status line; it moves
// down by titleH when the server's title is shown above it.
const statusY, titleH = 160, 26

// Window is the computer's only window.
type Window struct {
	x     *xwin
	ctrl  *app.Controller
	icons [][]byte
	scale float64
	fonts *fonts
	back  *image.RGBA

	calls chan func()
	quit  chan struct{}

	// ScreenAddress
	lblHeading, lblAddrHint, lblAddrError *widget
	edtAddr, btnConnect                   *widget
	// ScreenKey
	lblKeyIntro, lblKeyFp, lblServerFp, lblKeyNotice *widget
	edtKey, btnCopy, lnkChange                       *widget
	// ScreenMain
	lblComment, lblWaiting, lblInfoTitle, lblStatus *widget
	btnDisconnect                                   *widget
	// ScreenKeyError
	lblKeyErr, btnRemoveKey *widget
	// ScreenWarning
	lblWarnHeading, lblWarnText, btnContinue *widget
	// Unsupported
	lblUnsupHeading, lblUnsup, btnClose *widget

	// Unsupported, when set before Run, is shown instead of the app: the
	// reason this desktop can't be shared. The controller is not started.
	Unsupported string
	closing     bool // the Close button was pressed

	widgets []*widget
	dialogs []*dialog // the last one is shown

	focus    *widget
	hover    *widget
	pressed  *widget
	hasFocus bool // the window has the keyboard focus
	mapped   bool

	serverAsked string
	// lastRelease is the last key release; a press of the same key at the
	// same time is auto-repeat.
	lastRelease xproto.KeyReleaseEvent
	copiedAt    time.Time

	view     app.View
	rendered bool
}

// dialog is a modal question drawn over the whole window.
type dialog struct {
	icon    iconKind
	badge   *badge // the verified server badge, drawn as a band at the top
	heading string
	body    string
	buttons []*widget
	cancel  int // index of the button Esc and closing choose
	done    func(int)
}

// badge is the verified server badge (PLAN.md 12.6). It has its own band,
// color and icon, where no text from the server is ever drawn.
type badge struct {
	heading string // "Verified: Example Inc, Netherlands"
	server  string // "Server: helpme.example.com"
}

type iconKind int

const (
	iconNone iconKind = iota
	iconWarning
	iconError
)

// New returns the window; it opens with Run. icons are PNG files for the
// window manager.
func New(icons [][]byte) *Window {
	w := &Window{icons: icons, calls: make(chan func()), quit: make(chan struct{})}
	label := func(x, y, wd, h int, f fontKind, a align) *widget {
		l := newWidget(kindLabel, x, y, wd, h, f)
		l.align = a
		return l
	}
	button := func(text string, x, y, wd, h int, f fontKind, click func()) *widget {
		b := newWidget(kindButton, x, y, wd, h, f)
		b.text, b.onClick = text, click
		return b
	}

	w.lblHeading = label(24, 22, 412, 30, fontHeading, alignLeft)
	w.lblHeading.text = i18n.Trans("Connect to a techie")
	w.lblAddrHint = label(24, 62, 412, 36, fontNormal, alignLeft)
	w.lblAddrHint.text = i18n.Trans("Type the server address a techie gave you, for example helpme.example.com.")
	w.edtAddr = newWidget(kindField, 24, 106, 300, 28, fontNormal)
	w.edtAddr.field.limit = 300
	w.btnConnect = button(i18n.Trans("Connect"), 334, 106, 102, 28, fontNormal, w.connect)
	w.btnConnect.isDefault = true
	w.lblAddrError = label(24, 146, 412, 80, fontNormal, alignLeft)
	w.lblAddrError.color = colError

	w.lblKeyIntro = label(24, 18, 412, 36, fontNormal, alignLeft)
	w.lblKeyIntro.text = i18n.Trans("Paste this key into a chat with a techie (for example WhatsApp).")
	w.edtKey = newWidget(kindKeyBox, 24, 56, 412, 62, fontNormal)
	w.btnCopy = button(i18n.Trans("Copy key"), 24, 128, 412, 46, fontBig, w.copyKey)
	w.lblKeyFp = label(24, 182, 412, 34, fontNormal, alignLeft)
	w.lblServerFp = label(24, 217, 412, 34, fontNormal, alignLeft)
	w.lblKeyNotice = label(24, 256, 412, 36, fontNormal, alignLeft)
	w.lblKeyNotice.color = colGray
	w.lnkChange = newWidget(kindLink, 24, 302, 260, 20, fontNormal)
	w.lnkChange.text = i18n.Trans("Use a different server address")
	w.lnkChange.onClick = func() { w.ctrl.Disconnect() }

	w.lblComment = label(24, 44, 412, 34, fontHeading, alignCenter)
	w.lblWaiting = label(24, 104, 412, 40, fontBig, alignCenter)
	w.lblInfoTitle = label(24, 152, 412, 28, fontHeading, alignCenter)
	w.lblStatus = label(24, statusY, 412, 64, fontNormal, alignCenter)
	w.btnDisconnect = button(i18n.Trans("Disconnect"), 150, 258, 160, 32, fontNormal, func() {
		if w.view.InSession {
			w.ctrl.EndSession()
		} else {
			w.ctrl.Disconnect()
		}
	})

	w.lblKeyErr = label(24, 24, 412, 190, fontNormal, alignLeft)
	w.lblKeyErr.color = colError
	w.btnRemoveKey = button(i18n.Trans("Remove key..."), 24, 226, 160, 32, fontNormal, w.confirmRemoveKey)

	w.lblWarnHeading = label(24, 22, 412, 56, fontHeading, alignLeft)
	w.lblWarnHeading.text = i18n.Trans("Only continue if someone you know personally gave you this address.")
	w.lblWarnText = label(24, 86, 412, 130, fontNormal, alignLeft)
	w.btnContinue = button(i18n.Trans("Continue"), 24, 236, 160, 32, fontNormal, func() { w.ctrl.ContinueWarning() })
	w.btnContinue.isDefault = true

	w.lblUnsupHeading = label(24, 22, 412, 30, fontHeading, alignLeft)
	w.lblUnsupHeading.text = i18n.Trans("This desktop can't be shared")
	w.lblUnsup = label(24, 62, 412, 180, fontNormal, alignLeft)
	w.btnClose = button(i18n.Trans("Close"), 336, 278, 100, 30, fontNormal, func() { w.closing = true })
	w.btnClose.isDefault = true

	w.widgets = []*widget{
		w.lblHeading, w.lblAddrHint, w.edtAddr, w.btnConnect, w.lblAddrError,
		w.lblKeyIntro, w.edtKey, w.btnCopy, w.lblKeyFp, w.lblServerFp, w.lblKeyNotice, w.lnkChange,
		w.lblComment, w.lblWaiting, w.lblInfoTitle, w.lblStatus, w.btnDisconnect,
		w.lblKeyErr, w.btnRemoveKey,
		w.lblWarnHeading, w.lblWarnText, w.btnContinue,
		w.lblUnsupHeading, w.lblUnsup, w.btnClose,
	}
	return w
}

// Run opens the window, starts the controller and handles events until the
// window is closed. It returns an error when there is no usable display.
func (w *Window) Run(ctrl *app.Controller) error {
	w.ctrl = ctrl
	// The window size depends on the scale, which needs the connection.
	x, err := openX(Title, func(dpi float64) (int, int) {
		w.scale = max(1, dpi/96)
		return w.px(clientW), w.px(clientH)
	}, w.icons)
	if err != nil {
		return err
	}
	w.x = x
	defer x.close()
	if w.fonts, err = newFonts(w.scale); err != nil {
		return err
	}
	w.fitButtons()
	w.back = image.NewRGBA(image.Rect(0, 0, w.px(clientW), w.px(clientH)))
	xproto.MapWindow(x.c, x.win)

	events := make(chan xgbEvent, 64)
	go func() {
		defer close(events)
		for {
			ev, err := x.c.WaitForEvent()
			if ev == nil && err == nil {
				return
			}
			events <- xgbEvent{ev, err}
		}
	}()

	w.apply(app.View{Screen: -1})
	if w.Unsupported != "" {
		w.lblUnsup.text = w.Unsupported
		for _, wd := range []*widget{w.lblUnsupHeading, w.lblUnsup, w.btnClose} {
			wd.visible = true
		}
		w.focus = w.btnClose
	} else {
		ctrl.Start()
	}
	defer close(w.quit)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case e, ok := <-events:
			if !ok {
				ctrl.Close()
				return nil
			}
			if e.err != nil {
				continue // errors of requests nobody checks
			}
			if w.handle(e.ev) {
				ctrl.Close()
				w.closeDialogs()
				return nil
			}
		case f := <-w.calls:
			f()
		case <-tick.C:
			if !w.copiedAt.IsZero() && time.Since(w.copiedAt) > 2*time.Second {
				w.copiedAt = time.Time{}
				w.btnCopy.text = i18n.Trans("Copy key")
			}
		}
		if w.closing {
			ctrl.Close()
			return nil
		}
		w.redraw()
	}
}

type xgbEvent struct {
	ev  interface{}
	err error
}

// do runs f on the UI goroutine and waits for it; after the window closed
// it does nothing.
func (w *Window) do(f func()) {
	done := make(chan struct{})
	select {
	case w.calls <- func() { f(); close(done) }:
		<-done
	case <-w.quit:
	}
}

// post runs f on the UI goroutine without waiting.
func (w *Window) post(f func()) {
	go w.do(f)
}

// px converts 96-DPI units to pixels.
func (w *Window) px(v int) int { return int(float64(v)*w.scale + 0.5) }

func (w *Window) rect(wd *widget) image.Rectangle {
	return image.Rect(w.px(wd.x), w.px(wd.y), w.px(wd.x+wd.w), w.px(wd.y+wd.h))
}

// Render implements app.UI.
func (w *Window) Render(v app.View) {
	w.do(func() { w.apply(v) })
}

// checkWait is how long the approval dialog waits for the verified server
// check before it opens with "Checking who runs this server..." (PLAN.md
// 12.5).
const checkWait = 3 * time.Second

// ConfirmTechie implements app.UI with a dialog in the window that asks
// whether the techie may take control. A verified server gets a green band
// with the company at the top, where no text from the server is shown
// (PLAN.md 12.6). A changed techie key gets an error icon, a warning
// headline and Cancel as the default button. While the verified server check
// still runs the dialog says so, and it changes once the answer is in.
func (w *Window) ConfirmTechie(ctx context.Context, h session.Techie, oldFP string, b *app.Badges) bool {
	select {
	case <-b.Done():
	case <-time.After(checkWait):
	case <-ctx.Done():
		return false
	case <-w.quit:
		return false
	}
	d := &dialog{}
	fill := func() { techieDialog(d, h, oldFP, b.Badge()) }
	fill()
	allow := 0
	if oldFP != "" {
		d.setButtons(0, 0, i18n.Trans("Cancel"), i18n.Trans("Allow anyway"))
		allow = 1
	} else {
		d.setButtons(0, 1, i18n.Trans("Allow"), i18n.Trans("Cancel"))
	}
	answer := make(chan int, 1)
	d.done = func(i int) { answer <- i }
	w.do(func() {
		if ctx.Err() == nil {
			w.openDialog(d)
			w.bringToFront()
		} else {
			answer <- d.cancel
		}
	})
	checked := b.Done()
	for {
		select {
		case i := <-answer:
			return i == allow && ctx.Err() == nil
		case <-checked:
			checked = nil
			w.do(fill)
		case <-ctx.Done():
			// The question became moot (the techie gave up, the server
			// connection was lost): close it as if Cancel was pressed.
			w.do(func() { w.closeDialog(d, d.cancel) })
			return false
		case <-w.quit:
			return false
		}
	}
}

// techieDialog sets the texts of the approval dialog for techie h with
// badge b; the buttons stay.
func techieDialog(d *dialog, h session.Techie, oldFP string, b app.Badge) {
	quoted := `"` + h.Name + `"`
	esc := i18n.Trans("You can take back control at any time by pressing Esc.")
	fingerprint := i18n.Trans("Key fingerprint:\n{0}", h.Fingerprint)
	d.badge = nil
	if b.Verified {
		d.badge = &badge{heading: b.Heading, server: b.Server}
	}
	switch {
	case oldFP != "":
		d.icon = iconError
		d.heading = i18n.Trans("Warning: the key of {0} has changed", quoted)
		d.body = i18n.Trans("Key last time:\n{0}\n"+
			"Key now:\n{1}\n\n"+
			"This may be someone pretending to be a techie. Do not continue "+
			"unless the techie confirmed by phone that they have a new key.", oldFP, h.Fingerprint)
		if !b.Verified {
			d.body += "\n\n" + b.Text
		}
	case b.Verified:
		d.icon = iconNone
		d.heading = i18n.Trans("Allow {0} to take control of this computer?", quoted)
		d.body = b.Text + "\n\n" + fingerprint + "\n\n" + esc
	default:
		d.icon = iconWarning
		d.heading = i18n.Trans("Allow {0} to take control of this computer?", quoted)
		d.body = b.Text + "\n\n" + fingerprint + "\n\n" + esc
	}
}

// setButtons creates the dialog's buttons; def is the default one.
func (d *dialog) setButtons(def, cancel int, labels ...string) {
	d.cancel = cancel
	for i, l := range labels {
		b := newWidget(kindButton, 0, 0, 120, 30, fontNormal)
		b.text, b.visible = l, true
		b.isDefault = i == def
		d.buttons = append(d.buttons, b)
	}
}

func (w *Window) openDialog(d *dialog) {
	for i, b := range d.buttons {
		b.onClick = func() { w.closeDialog(d, i) }
	}
	w.dialogs = append(w.dialogs, d)
	for _, b := range d.buttons {
		if b.isDefault {
			w.focus = b
		}
	}
	w.layoutDialog()
}

// closeDialog removes d, if it is still open, and reports the answer.
func (w *Window) closeDialog(d *dialog, answer int) {
	for i, o := range w.dialogs {
		if o == d {
			w.dialogs = append(w.dialogs[:i], w.dialogs[i+1:]...)
			w.focus, w.pressed, w.hover = nil, nil, nil
			w.layoutDialog()
			if len(w.dialogs) == 0 {
				w.focusDefault()
			}
			if d.done != nil {
				d.done(answer)
			}
			return
		}
	}
}

func (w *Window) closeDialogs() {
	for len(w.dialogs) > 0 {
		d := w.dialogs[len(w.dialogs)-1]
		w.closeDialog(d, d.cancel)
	}
}

// layoutDialog puts the buttons of the shown dialog at the bottom right.
func (w *Window) layoutDialog() {
	if len(w.dialogs) == 0 {
		return
	}
	d := w.dialogs[len(w.dialogs)-1]
	x := clientW - 24
	for i := len(d.buttons) - 1; i >= 0; i-- {
		b := d.buttons[i]
		b.w = max(100, w.fonts.width(fontNormal, b.text)*96/w.px(96)+32)
		x -= b.w
		b.x, b.y = x, clientH-20-b.h
		x -= 10
	}
	if w.focus == nil {
		w.focus = d.buttons[0]
	}
}

// active returns the widgets that take input now: the shown dialog's
// buttons, or the visible widgets of the screen.
func (w *Window) active() []*widget {
	if len(w.dialogs) > 0 {
		return w.dialogs[len(w.dialogs)-1].buttons
	}
	var out []*widget
	for _, wd := range w.widgets {
		if wd.visible {
			out = append(out, wd)
		}
	}
	return out
}

func (w *Window) connect() {
	if w.view.Screen == app.ScreenAddress && !w.view.Connecting {
		w.ctrl.Connect(string(w.edtAddr.field.text))
	}
}

// copyKey puts the key line on the clipboard and shows "Copied" for a
// moment.
func (w *Window) copyKey() {
	if w.view.KeyLine == "" || !w.x.copy(w.view.KeyLine) {
		return
	}
	w.btnCopy.text = i18n.Trans("Copied")
	w.copiedAt = time.Now()
}

func (w *Window) confirmRemoveKey() {
	d := &dialog{
		icon:    iconWarning,
		heading: i18n.Trans("Remove the key of this PC?"),
		body: i18n.Trans("This deletes the key and the settings of this app. A new key is made, and your " +
			"techie will have to register this PC again before you can ask for help."),
	}
	d.setButtons(1, 1, i18n.Trans("Remove key"), i18n.Trans("Cancel"))
	d.done = func(i int) {
		if i != 0 {
			return
		}
		if err := w.ctrl.RemoveKey(); err != nil {
			w.message(iconError, i18n.Trans("The key could not be removed"), err.Error())
		}
	}
	w.openDialog(d)
}

// message shows a dialog with only OK.
func (w *Window) message(icon iconKind, heading, body string) {
	d := &dialog{icon: icon, heading: heading, body: body}
	d.setButtons(0, 0, i18n.Trans("OK"))
	w.openDialog(d)
}

// maybeWarnServerKey shows the server key change warning once per new key.
func (w *Window) maybeWarnServerKey() {
	hk := w.view.ServerKeyChange
	if hk == nil || w.serverAsked == hk.NewFingerprint {
		return
	}
	w.serverAsked = hk.NewFingerprint
	change := *hk
	d := &dialog{
		icon:    iconError,
		heading: i18n.Trans("Warning: the identity of the server has changed"),
		body: i18n.Trans("Key last time:\n{0}\n"+
			"Key now:\n{1}\n\n"+
			"This may be someone pretending to be your server. HelpBuoy stopped connecting. "+
			"Only continue if a techie confirmed by phone that the server has a new key "+
			"and the new fingerprint above matches what they read to you.",
			change.OldFingerprint, change.NewFingerprint),
	}
	d.setButtons(0, 0, i18n.Trans("Cancel"), i18n.Trans("Trust the new key"))
	d.done = func(i int) {
		if i == 1 {
			w.ctrl.TrustServerKey(change.NewFingerprint)
		}
	}
	w.openDialog(d)
	w.bringToFront()
}

// bringToFront shows the window and asks for the focus.
func (w *Window) bringToFront() {
	w.x.activate()
	if !w.hasFocus {
		w.x.demandAttention(true)
	}
}

// apply shows v. It runs on the UI goroutine.
func (w *Window) apply(v app.View) {
	prev := w.view
	entered := !w.rendered || prev.Screen != v.Screen

	show := func(on bool, ws ...*widget) {
		for _, wd := range ws {
			wd.visible = on
		}
	}

	addr := v.Screen == app.ScreenAddress
	show(addr, w.lblHeading, w.lblAddrHint, w.edtAddr, w.btnConnect, w.lblAddrError)
	if addr {
		if entered && v.Address != "" {
			w.edtAddr.field.set(v.Address)
		}
		w.edtAddr.enabled = !v.Connecting
		w.btnConnect.enabled = !v.Connecting
		if v.Connecting {
			w.btnConnect.text = i18n.Trans("Connecting...")
			w.lblAddrError.text = ""
		} else {
			w.btnConnect.text = i18n.Trans("Connect")
			w.lblAddrError.text = v.Error
			if entered || prev.Connecting {
				w.focus = w.edtAddr
				w.edtAddr.field.selectAll()
			}
		}
	}

	warn := v.Screen == app.ScreenWarning
	show(warn, w.lblWarnHeading, w.lblWarnText, w.btnContinue)
	if warn {
		w.lblWarnText.text = i18n.Trans("If someone called you unexpectedly, hang up: banks, Microsoft and the "+
			"police never ask you to install this.") + "\n\n" + i18n.Trans("Server: {0}", v.Address)
		w.btnContinue.enabled = v.WarnWait == 0
		if v.WarnWait > 0 {
			w.btnContinue.text = i18n.Trans("Continue ({0})", v.WarnWait)
		} else {
			w.btnContinue.text = i18n.Trans("Continue")
		}
		if entered || (v.WarnWait == 0 && prev.WarnWait > 0) {
			w.focus = w.btnContinue
		}
	}

	key := v.Screen == app.ScreenKey
	show(key, w.lblKeyIntro, w.edtKey, w.btnCopy, w.lblKeyFp, w.lblServerFp, w.lblKeyNotice)
	show(key || warn, w.lnkChange)
	if key {
		w.edtKey.text = v.KeyLine
		w.lblKeyFp.text = i18n.Trans("Key fingerprint:\n{0}", v.KeyFingerprint)
		w.lblServerFp.text = i18n.Trans("Server fingerprint:\n{0}", v.ServerFingerprint)
		notice := v.Notice
		if notice == "" {
			notice = i18n.Trans("Waiting for a techie to add this key. This window changes by itself when that is done.")
		}
		w.lblKeyNotice.text = notice
		if entered {
			w.focus = w.btnCopy
		}
	}

	main := v.Screen == app.ScreenMain
	title := main && v.Ready && v.Error == "" && v.Notice == "" && v.Title != ""
	show(main, w.lblWaiting, w.lblStatus, w.btnDisconnect, w.lblComment)
	show(title, w.lblInfoTitle)
	w.lblStatus.y = statusY
	if title {
		w.lblStatus.y += titleH
	}
	if main {
		w.lblComment.text = v.KeyComment
		w.lblWaiting.text = ""
		if v.Ready {
			w.lblWaiting.text = i18n.Trans("Waiting for a techie")
		}
		status := v.Error
		if status == "" {
			status = v.Notice
		}
		if status == "" && v.Ready {
			w.lblInfoTitle.text = v.Title
			status = v.Instruction
		}
		w.lblStatus.text = status
		w.lblStatus.color = colGray
		if v.Error != "" {
			w.lblStatus.color = colError
		}
		// During a session (window minimized) the button ends just the
		// session; afterwards it disconnects again.
		if v.InSession {
			w.btnDisconnect.text = i18n.Trans("End session")
		} else {
			w.btnDisconnect.text = i18n.Trans("Disconnect")
		}
		if entered {
			w.focus = w.btnDisconnect
		}
	}

	keyErr := v.Screen == app.ScreenKeyError
	show(keyErr, w.lblKeyErr, w.btnRemoveKey)
	if keyErr {
		w.lblKeyErr.text = v.Error + "\n\n" + i18n.Trans("Folder: {0}", v.KeyDir) + "\n\n" +
			i18n.Trans("The key is never replaced on its own. Remove key deletes it and makes a new one; "+
				"a techie will have to register this PC again.")
		if entered {
			w.focus = w.btnRemoveKey
		}
	}

	if w.x != nil {
		caption := Title
		if v.Title != "" {
			caption = v.Title + " - " + Title
		}
		w.x.setTitle(caption)
		// During a session the window gets out of the way; afterwards it
		// comes back.
		if v.InSession && !prev.InSession {
			w.x.minimize()
		} else if !v.InSession && prev.InSession {
			w.bringToFront()
		}
	}
	if v.Screen >= 0 {
		w.view = v
		w.rendered = true
		w.maybeWarnServerKey()
	}
	if w.focus != nil && !w.focus.focusable() && len(w.dialogs) == 0 {
		w.focusDefault()
	}
	w.fitButtons()
}

// buttonW returns the width, in 96-DPI units, a button needs for its text:
// at least minW, at most the content width.
func (w *Window) buttonW(b *widget, minW int) int {
	return min(contentW, max(minW, w.fonts.width(b.font, b.text)*96/w.px(96)+32))
}

// fitButtons sizes the window's buttons to their (translated) text, keeping
// each one's anchor: Connect and Close stay right-aligned, Disconnect stays
// centered and Remove key stays left-aligned. The address field gives up the
// room Connect takes.
func (w *Window) fitButtons() {
	if w.fonts == nil {
		return
	}
	right := 24 + contentW
	b := w.btnConnect
	b.w = w.buttonW(b, 102)
	b.x = right - b.w
	w.edtAddr.w = b.x - 10 - w.edtAddr.x
	b = w.btnClose
	b.w = w.buttonW(b, 100)
	b.x = right - b.w
	b = w.btnDisconnect
	b.w = w.buttonW(b, 160)
	b.x = clientW/2 - b.w/2
	w.btnRemoveKey.w = w.buttonW(w.btnRemoveKey, 160)
}

// focusDefault focuses the first focusable widget of the screen.
func (w *Window) focusDefault() {
	w.focus = nil
	for _, wd := range w.active() {
		if wd.focusable() {
			w.focus = wd
			return
		}
	}
}

// moveFocus moves the focus to the next (or previous) focusable widget.
func (w *Window) moveFocus(back bool) {
	var ws []*widget
	for _, wd := range w.active() {
		if wd.focusable() {
			ws = append(ws, wd)
		}
	}
	if len(ws) == 0 {
		return
	}
	i := -1
	for j, wd := range ws {
		if wd == w.focus {
			i = j
		}
	}
	if back {
		i = (i - 1 + len(ws)) % len(ws)
		if i < 0 {
			i = len(ws) - 1
		}
	} else {
		i = (i + 1) % len(ws)
	}
	w.focus = ws[i]
	if w.focus.kind == kindField {
		w.focus.field.selectAll()
	}
}

// handle processes one X event and reports whether the window was closed.
func (w *Window) handle(ev interface{}) bool {
	x := w.x
	switch e := ev.(type) {
	case xproto.ExposeEvent:
		if e.Count == 0 {
			w.x.put(w.back)
		}
	case xproto.MapNotifyEvent:
		w.mapped = true
	case xproto.UnmapNotifyEvent:
		w.mapped = false
	case xproto.FocusInEvent:
		w.hasFocus = true
		x.demandAttention(false)
	case xproto.FocusOutEvent:
		w.hasFocus = false
	case xproto.ClientMessageEvent:
		if e.Type == x.atoms["WM_PROTOCOLS"] && xproto.Atom(e.Data.Data32[0]) == x.atoms["WM_DELETE_WINDOW"] {
			return true
		}
	case xproto.MappingNotifyEvent:
		if km, err := xkeys.Load(x.c); err == nil {
			x.km = km
		}
	case xproto.SelectionRequestEvent:
		x.selectionRequest(e)
	case xproto.SelectionClearEvent:
		x.clip = ""
	case xproto.SelectionNotifyEvent:
		if s, ok := x.pasted(e); ok && w.focus != nil && w.focus.kind == kindField && w.focus.enabled {
			w.focus.field.insert(s)
		}
	case xproto.KeyPressEvent:
		x.time = e.Time
		repeat := e.Detail == w.lastRelease.Detail && e.Time == w.lastRelease.Time
		w.key(e, repeat)
	case xproto.KeyReleaseEvent:
		w.lastRelease = e
	case xproto.ButtonPressEvent:
		x.time = e.Time
		if e.Detail == 1 {
			w.mouseDown(int(e.EventX), int(e.EventY))
		}
	case xproto.ButtonReleaseEvent:
		x.time = e.Time
		if e.Detail == 1 {
			w.mouseUp(int(e.EventX), int(e.EventY))
		}
	case xproto.MotionNotifyEvent:
		w.mouseMove(int(e.EventX), int(e.EventY), e.State&xproto.ButtonMask1 != 0)
	case xproto.LeaveNotifyEvent:
		w.hover = nil
		x.setCursor(cursorArrow)
	}
	return false
}

func (w *Window) at(px, py int) *widget {
	p := image.Pt(px, py)
	for _, wd := range w.active() {
		if wd.kind == kindLabel || wd.kind == kindKeyBox {
			continue
		}
		r := w.rect(wd)
		if wd.kind == kindLink {
			r.Max.X = r.Min.X + w.fonts.width(wd.font, wd.text)
		}
		if p.In(r) {
			return wd
		}
	}
	return nil
}

func (w *Window) mouseDown(px, py int) {
	wd := w.at(px, py)
	w.pressed = wd
	if wd == nil || !wd.enabled {
		return
	}
	if wd.focusable() {
		w.focus = wd
	}
	if wd.kind == kindField {
		r := w.rect(wd)
		wd.field.move(wd.caretAt(w.fonts, px-r.Min.X-w.px(5)+wd.field.scroll), false)
	}
}

func (w *Window) mouseMove(px, py int, dragging bool) {
	wd := w.at(px, py)
	w.hover = wd
	switch {
	case wd != nil && wd.kind == kindField && wd.enabled:
		w.x.setCursor(cursorText)
	case wd != nil && wd.kind == kindLink && wd.enabled:
		w.x.setCursor(cursorHand)
	default:
		w.x.setCursor(cursorArrow)
	}
	if dragging && w.pressed != nil && w.pressed.kind == kindField && w.pressed.enabled {
		f := w.pressed
		r := w.rect(f)
		f.field.move(f.caretAt(w.fonts, px-r.Min.X-w.px(5)+f.field.scroll), true)
	}
}

func (w *Window) mouseUp(px, py int) {
	wd := w.pressed
	w.pressed = nil
	if wd != nil && wd == w.at(px, py) && wd.enabled && wd.onClick != nil {
		wd.onClick()
	}
}

// key handles a key press: dialog keys, focus movement, activation and
// editing of the address field. Auto-repeat (repeat) only edits: a held
// Enter must not click one button after the other as the screens change.
func (w *Window) key(e xproto.KeyPressEvent, repeat bool) {
	ks := w.x.km.Lookup(uint8(e.Detail), e.State)
	ctrl := e.State&xkeys.ModControl != 0
	shift := e.State&xkeys.ModShift != 0
	inField := w.focus != nil && w.focus.kind == kindField
	activate := ks == xkeys.Return || ks == xkeys.KPEnter || ks == xkeys.Escape || ks == ' ' && !inField
	if repeat && activate {
		return
	}
	if len(w.dialogs) > 0 && ks == xkeys.Escape {
		d := w.dialogs[len(w.dialogs)-1]
		w.closeDialog(d, d.cancel)
		return
	}
	switch ks {
	case xkeys.Tab:
		w.moveFocus(shift)
		return
	case xkeys.ISOLeftTab:
		w.moveFocus(true)
		return
	}
	f := w.focus
	if f == nil || !f.enabled {
		return
	}
	switch f.kind {
	case kindButton, kindLink:
		if ks == xkeys.Return || ks == xkeys.KPEnter || ks == ' ' {
			if f.onClick != nil {
				f.onClick()
			}
		}
		return
	case kindField:
	default:
		return
	}
	fd := f.field
	switch {
	case ks == xkeys.Return || ks == xkeys.KPEnter:
		if f == w.edtAddr {
			w.connect()
		}
	case ks == xkeys.BackSpace:
		fd.erase(true)
	case ks == xkeys.Delete || ks == xkeys.KPDelete:
		fd.erase(false)
	case ks == xkeys.Left || ks == xkeys.KPLeft:
		if ctrl {
			fd.move(fd.wordLeft(), shift)
		} else if a, _ := fd.selection(); !shift && fd.caret != fd.anchor {
			fd.move(a, false)
		} else {
			fd.move(fd.caret-1, shift)
		}
	case ks == xkeys.Right || ks == xkeys.KPRight:
		if ctrl {
			fd.move(fd.wordRight(), shift)
		} else if _, b := fd.selection(); !shift && fd.caret != fd.anchor {
			fd.move(b, false)
		} else {
			fd.move(fd.caret+1, shift)
		}
	case ks == xkeys.Home || ks == xkeys.KPHome:
		fd.move(0, shift)
	case ks == xkeys.End || ks == xkeys.KPEnd:
		fd.move(len(fd.text), shift)
	case ctrl && (ks == 'a' || ks == 'A'):
		fd.selectAll()
	case ctrl && (ks == 'c' || ks == 'C'), ctrl && ks == xkeys.Insert:
		if s := fd.selected(); s != "" {
			w.x.copy(s)
		}
	case ctrl && (ks == 'x' || ks == 'X'):
		if s := fd.selected(); s != "" && w.x.copy(s) {
			fd.erase(true)
		}
	case ctrl && (ks == 'v' || ks == 'V'), shift && ks == xkeys.Insert:
		w.x.requestPaste()
	case ctrl || e.State&xkeys.ModMod1 != 0:
		// Other shortcuts do nothing.
	default:
		if r := xkeys.Rune(ks); r != 0 {
			fd.insert(string(r))
		}
	}
}

// redraw paints everything into the back buffer and uploads it.
func (w *Window) redraw() {
	c := &canvas{img: w.back, fonts: w.fonts}
	c.fill(w.back.Rect, colBg)
	if len(w.dialogs) > 0 {
		w.drawDialog(c, w.dialogs[len(w.dialogs)-1])
	} else {
		for _, wd := range w.widgets {
			if wd.visible {
				wd.draw(c, w.rect(wd), w.scale, wd == w.focus && w.hasFocus, wd == w.hover, wd == w.pressed)
			}
		}
	}
	w.x.put(w.back)
}

func (w *Window) drawDialog(c *canvas, d *dialog) {
	top := w.px(24)
	if d.badge != nil {
		const bandH = 70
		c.fill(image.Rect(0, 0, w.px(clientW), w.px(bandH)), colVerified)
		r := image.Rect(w.px(24), w.px(19), w.px(24+32), w.px(19+32))
		c.disc(r, colWhite)
		checkMark(c, r, colVerified)
		tr := image.Rect(w.px(70), w.px(12), w.px(clientW-24), w.px(bandH))
		h := c.textBox(fontHeading, d.badge.heading, tr, alignLeft, colWhite)
		tr.Min.Y += h + w.px(2)
		c.textBox(fontNormal, d.badge.server, tr, alignLeft, colWhite)
		top = w.px(bandH + 14)
	}
	x := 24
	if d.icon != iconNone {
		col := colWarning
		if d.icon == iconError {
			col = colError
		}
		r := image.Rect(w.px(24), top-w.px(2), w.px(24+32), top+w.px(30))
		c.disc(r, col)
		lh := w.fonts.lineHeight(fontHeading)
		tw := w.fonts.width(fontHeading, "!")
		c.text(fontHeading, "!", r.Min.X+(r.Dx()-tw)/2, r.Min.Y+(r.Dy()-lh)/2, colWhite)
		x = 70
	}
	r := image.Rect(w.px(x), top, w.px(clientW-24), w.px(clientH))
	h := c.textBox(fontHeading, d.heading, r, alignLeft, colText)
	r.Min.Y += h + w.px(10)
	c.textBox(fontNormal, d.body, r, alignLeft, colText)
	for _, b := range d.buttons {
		b.draw(c, w.rect(b), w.scale, b == w.focus && w.hasFocus, b == w.hover, b == w.pressed)
	}
}
