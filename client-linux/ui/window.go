// Package ui is the computer's window on Linux and macOS (PLAN.md 6.2 and
// 6.3). It draws its own controls with the Go fonts into an RGBA buffer, so
// it needs no toolkit; a Host shows the buffer and delivers input. On Linux
// that is xwin, which speaks the X11 protocol directly with jezek/xgb and
// needs no C libraries; the macOS client brings an AppKit host. Like the
// Windows window, it only renders the app.View it is given and forwards
// button presses to the app.Controller.
package ui

import (
	"context"
	"image"
	"strings"
	"time"

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

// Window is the computer's only window.
type Window struct {
	host   Host
	opened bool // host.Open succeeded
	ctrl   *app.Controller
	icons  [][]byte
	scale  float64
	fonts  *fonts
	back   *image.RGBA

	calls chan func()
	quit  chan struct{}

	// ScreenAddress
	lblHeading, lblAddrHint, lblAddrError *widget
	edtAddr, btnConnect, chkProxy         *widget
	// ScreenKey
	lblKeyIntro, lblKeyFp, lblServerFp, lblKeyNotice *widget
	edtKey, btnCopy, lnkChange                       *widget
	// ScreenMain
	lblComment, lblWaiting, lblInfoTitle, lblWho, lblStatus *widget
	btnDisconnect                                           *widget
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

	// Permissions, when set before Run, are checked first: while one of
	// them is not granted the window asks for them instead of starting the
	// controller, and it starts once all are granted.
	Permissions []Permission
	// PermissionsNote is shown below the permissions, in gray.
	PermissionsNote                           string
	perms                                     []permRow // nil when the permissions screen is not shown
	lblPermHeading, lblPermIntro, lblPermNote *widget

	widgets []*widget
	dialogs []*dialog // the last one is shown

	focus    *widget
	hover    *widget
	pressed  *widget
	hasFocus bool // the window has the keyboard focus

	serverAsked string
	copiedAt    time.Time

	view     app.View
	rendered bool
}

// Permission is something the user has to allow in the system settings
// before the app can work, such as Screen Recording on macOS. At most two
// fit in the window.
type Permission struct {
	Name    string      // "Screen Recording"
	Text    string      // what it is for, one or two short lines
	Granted func() bool // polled while the permissions are shown
	Open    func()      // opens the place to allow it
}

// permRow is the widgets of one Permission.
type permRow struct {
	p                Permission
	lblName, lblText *widget
	btnOpen          *widget
}

// dialog is a modal question drawn over the whole window.
type dialog struct {
	icon    iconKind
	heading string
	body    string
	buttons []*widget
	cancel  int // index of the button Esc and closing choose
	done    func(int)
}

type iconKind int

const (
	iconNone iconKind = iota
	iconWarning
	iconError
)

// New returns the X11 window; it opens with Run. icons are PNG files for
// the window manager.
func New(icons [][]byte) *Window { return NewWithHost(&xwin{}, icons) }

// NewWithHost returns a window shown by host; it opens with Run. icons are
// PNG files the host may use.
func NewWithHost(host Host, icons [][]byte) *Window {
	w := &Window{host: host, icons: icons, calls: make(chan func()), quit: make(chan struct{})}
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
	w.lblAddrHint = label(24, 58, 412, 42, fontNormal, alignLeft)
	w.lblAddrHint.text = i18n.Trans("Type the server address a techie gave you, for example helpbuoy.example.com.")
	w.edtAddr = newWidget(kindField, 24, 106, 300, 28, fontNormal)
	w.edtAddr.field.limit = 300
	w.btnConnect = button(i18n.Trans("Connect"), 334, 106, 102, 28, fontNormal, w.connect)
	w.btnConnect.isDefault = true
	w.chkProxy = newWidget(kindCheck, 24, 142, 412, 22, fontNormal)
	w.chkProxy.text = i18n.Trans("Use system proxy settings")
	w.chkProxy.checked = true
	w.chkProxy.onClick = func() {
		w.chkProxy.checked = !w.chkProxy.checked
		w.ctrl.SetUseSystemProxy(w.chkProxy.checked)
	}
	w.lblAddrError = label(24, 172, 412, 70, fontNormal, alignLeft)
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

	w.lblComment = label(24, 16, 412, 30, fontHeading, alignCenter)
	w.lblWaiting = label(24, 52, 412, 40, fontBig, alignCenter)
	w.lblInfoTitle = label(24, 100, 412, 26, fontHeading, alignCenter)
	w.lblWho = label(24, 128, 412, 58, fontNormal, alignCenter)
	w.lblStatus = label(24, 192, 412, 60, fontNormal, alignCenter)
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

	w.lblPermHeading = label(24, 22, 412, 30, fontHeading, alignLeft)
	w.lblPermHeading.text = i18n.Trans("Allow HelpBuoy to share this screen")
	w.lblPermIntro = label(24, 60, 412, 40, fontNormal, alignLeft)
	w.lblPermIntro.text = i18n.Trans("A techie can only see and control this computer after you allow these in the system settings.")
	w.lblPermNote = label(24, 262, 412, 56, fontNormal, alignLeft)
	w.lblPermNote.color = colGray

	w.widgets = []*widget{
		w.lblHeading, w.lblAddrHint, w.edtAddr, w.btnConnect, w.chkProxy, w.lblAddrError,
		w.lblKeyIntro, w.edtKey, w.btnCopy, w.lblKeyFp, w.lblServerFp, w.lblKeyNotice, w.lnkChange,
		w.lblComment, w.lblWaiting, w.lblInfoTitle, w.lblWho, w.lblStatus, w.btnDisconnect,
		w.lblKeyErr, w.btnRemoveKey,
		w.lblWarnHeading, w.lblWarnText, w.btnContinue,
		w.lblUnsupHeading, w.lblUnsup, w.btnClose,
		w.lblPermHeading, w.lblPermIntro, w.lblPermNote,
	}
	return w
}

// showPermissions builds and shows the permissions screen. It reports
// false, and shows nothing, when every permission is granted already.
func (w *Window) showPermissions() bool {
	missing := false
	for _, p := range w.Permissions {
		if !p.Granted() {
			missing = true
		}
	}
	if !missing {
		return false
	}
	for i, p := range w.Permissions[:min(2, len(w.Permissions))] {
		y := 108 + i*76
		r := permRow{p: p}
		r.lblName = newWidget(kindLabel, 24, y, 250, 22, fontNormal)
		r.lblText = newWidget(kindLabel, 24, y+22, 250, 40, fontNormal)
		r.lblText.text = p.Text
		r.lblText.color = colGray
		r.btnOpen = newWidget(kindButton, 290, y+6, 146, 32, fontNormal)
		r.btnOpen.text = i18n.Trans("Open settings")
		r.btnOpen.onClick = p.Open
		for _, wd := range []*widget{r.lblName, r.lblText, r.btnOpen} {
			wd.visible = true
			w.widgets = append(w.widgets, wd)
		}
		w.perms = append(w.perms, r)
	}
	w.lblPermNote.text = w.PermissionsNote
	for _, wd := range []*widget{w.lblPermHeading, w.lblPermIntro, w.lblPermNote} {
		wd.visible = true
	}
	w.updatePermissions()
	w.focusDefault()
	return true
}

// updatePermissions shows which permissions are granted and reports
// whether all are.
func (w *Window) updatePermissions() bool {
	all := true
	for _, r := range w.perms {
		ok := r.p.Granted()
		if ok {
			r.lblName.text = i18n.Trans("{0}: allowed", r.p.Name)
			r.lblName.color = colText
		} else {
			r.lblName.text = i18n.Trans("{0}: not allowed yet", r.p.Name)
			r.lblName.color = colError
			all = false
		}
		r.btnOpen.enabled = !ok
	}
	if w.focus != nil && !w.focus.focusable() {
		w.focusDefault()
	}
	return all
}

// hidePermissions removes the permissions screen.
func (w *Window) hidePermissions() {
	drop := map[*widget]bool{}
	for _, r := range w.perms {
		drop[r.lblName], drop[r.lblText], drop[r.btnOpen] = true, true, true
	}
	ws := w.widgets[:0]
	for _, wd := range w.widgets {
		if !drop[wd] {
			ws = append(ws, wd)
		}
	}
	w.widgets = ws
	w.perms = nil
	for _, wd := range []*widget{w.lblPermHeading, w.lblPermIntro, w.lblPermNote} {
		wd.visible = false
	}
	w.focus, w.hover, w.pressed = nil, nil, nil
}

// Run opens the window, starts the controller and handles events until the
// window is closed. It returns an error when there is no usable display.
func (w *Window) Run(ctrl *app.Controller) error {
	w.ctrl = ctrl
	scale, err := w.host.Open(Title, clientW, clientH, w.icons)
	if err != nil {
		return err
	}
	w.scale = max(1, scale)
	w.opened = true
	defer w.host.Close()
	if w.fonts, err = newFonts(w.scale); err != nil {
		return err
	}
	w.fitButtons()
	w.back = image.NewRGBA(image.Rect(0, 0, w.px(clientW), w.px(clientH)))
	w.host.Show()
	events := w.host.Events()

	w.apply(app.View{Screen: -1})
	if w.Unsupported != "" {
		w.lblUnsup.text = w.Unsupported
		for _, wd := range []*widget{w.lblUnsupHeading, w.lblUnsup, w.btnClose} {
			wd.visible = true
		}
		w.focus = w.btnClose
	} else if !w.showPermissions() {
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
			if w.handle(e) {
				ctrl.Close()
				w.closeDialogs()
				return nil
			}
		case f := <-w.calls:
			f()
		case <-tick.C:
			if w.perms != nil && w.updatePermissions() {
				w.hidePermissions()
				ctrl.Start()
			}
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

// ConfirmTechie implements app.UI with a dialog in the window that asks
// whether the techie may take control. The server vouches for the techie, so
// it shows only the techie's name.
func (w *Window) ConfirmTechie(ctx context.Context, h session.Techie) bool {
	d := &dialog{}
	techieDialog(d, h)
	d.setButtons(0, 1, i18n.Trans("Allow"), i18n.Trans("Cancel"))
	return w.ask(ctx, d) == 0
}

// ConfirmLock implements app.UI with a dialog in the window that asks
// whether to lock this PC to the server of company (PLAN.md 12.1).
func (w *Window) ConfirmLock(ctx context.Context, company string) bool {
	d := &dialog{
		heading: i18n.Trans("Get help only through {0}?", company),
		body: i18n.Trans("To protect you from scam calls, do you want this PC to get help via HelpBuoy only "+
			"through {0}? Other helpdesks can then only reach this PC after {0} allowed them.", company),
	}
	d.setButtons(0, 1, i18n.Trans("Lock to {0}", company), i18n.Trans("Not now"))
	return w.ask(ctx, d) == 0
}

// ask shows d and returns the button pressed, or d.cancel when the
// question became moot (ctx ended) or the window closed.
func (w *Window) ask(ctx context.Context, d *dialog) int {
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
	select {
	case i := <-answer:
		if ctx.Err() != nil {
			return d.cancel
		}
		return i
	case <-ctx.Done():
		// The question became moot (the techie gave up, the server
		// connection was lost): close it as if Cancel was pressed.
		w.do(func() { w.closeDialog(d, d.cancel) })
		return d.cancel
	case <-w.quit:
		return d.cancel
	}
}

// techieDialog sets the texts of the approval dialog for techie h; the
// buttons stay.
func techieDialog(d *dialog, h session.Techie) {
	d.heading = i18n.Trans("Allow {0} to take control of this computer?", `"`+h.Name+`"`)
	d.body = i18n.Trans("You can take back control at any time by pressing Esc.")
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
	if w.view.KeyLine == "" || !w.host.Copy(w.view.KeyLine) {
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
	w.host.Activate()
	if !w.hasFocus {
		w.host.DemandAttention(true)
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
	show(addr, w.lblHeading, w.lblAddrHint, w.edtAddr, w.btnConnect, w.chkProxy, w.lblAddrError)
	if addr {
		w.lblAddrHint.text = i18n.Trans("Type the server address a techie gave you, for example helpbuoy.example.com.")
		if v.LockedTo != "" {
			w.lblAddrHint.text = i18n.Trans("This PC gets help through {0}. Other servers can only connect after {0} allowed them.", v.LockedTo)
		}
		if entered && v.Address != "" {
			w.edtAddr.field.set(v.Address)
		}
		w.edtAddr.enabled = !v.Connecting
		w.btnConnect.enabled = !v.Connecting
		w.chkProxy.enabled = !v.Connecting
		w.chkProxy.checked = v.UseSystemProxy
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
	who := main && v.Company != ""
	show(main, w.lblWaiting, w.lblStatus, w.btnDisconnect, w.lblComment)
	show(who, w.lblInfoTitle, w.lblWho)
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
			status = v.Instruction
		}
		if who {
			w.lblInfoTitle.text = v.Company
			lines := []string{i18n.Trans("Phone: {0}", v.Phone)}
			if v.Description != "" {
				lines = append(lines, v.Description)
			}
			lines = append(lines, i18n.Trans("Server: {0}", v.ServerAddress))
			w.lblWho.text = strings.Join(lines, "\n")
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

	if w.opened {
		caption := Title
		if v.Screen == app.ScreenMain && v.Company != "" {
			caption = v.Company + " - " + Title
		}
		w.host.SetTitle(caption)
		// During a session the window gets out of the way; afterwards it
		// comes back.
		if v.InSession && !prev.InSession {
			w.host.Minimize()
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

// handle processes one event of the host and reports whether the window
// was closed.
func (w *Window) handle(ev Event) bool {
	switch e := ev.(type) {
	case ExposeEvent:
		w.host.Put(w.back)
	case FocusEvent:
		w.hasFocus = e.In
		if e.In {
			w.host.DemandAttention(false)
		}
	case CloseEvent:
		return true
	case PasteEvent:
		if w.focus != nil && w.focus.kind == kindField && w.focus.enabled {
			w.focus.field.insert(e.Text)
		}
	case KeyEvent:
		w.key(e)
	case MouseEvent:
		switch e.Kind {
		case MouseDown:
			w.mouseDown(e.X, e.Y)
		case MouseUp:
			w.mouseUp(e.X, e.Y)
		case MouseMove:
			w.mouseMove(e.X, e.Y, e.Drag)
		case MouseLeave:
			w.hover = nil
			w.host.SetCursor(CursorArrow)
		}
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
		if wd.kind == kindCheck { // the box and its label, not the empty rest
			r.Max.X = min(r.Max.X, r.Min.X+w.px(22)+w.fonts.width(wd.font, wd.text)+w.px(2))
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
		w.host.SetCursor(CursorText)
	case wd != nil && (wd.kind == kindLink || wd.kind == kindCheck) && wd.enabled:
		w.host.SetCursor(CursorHand)
	default:
		w.host.SetCursor(CursorArrow)
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
// editing of the address field. Auto-repeat only edits: a held Enter must
// not click one button after the other as the screens change.
func (w *Window) key(k KeyEvent) {
	inField := w.focus != nil && w.focus.kind == kindField
	activate := k.Key == KeyEnter || k.Key == KeyEscape || k.Char == ' ' && !inField
	if k.Repeat && activate {
		return
	}
	if len(w.dialogs) > 0 && k.Key == KeyEscape {
		d := w.dialogs[len(w.dialogs)-1]
		w.closeDialog(d, d.cancel)
		return
	}
	switch k.Key {
	case KeyTab:
		w.moveFocus(k.Shift)
		return
	case KeyBackTab:
		w.moveFocus(true)
		return
	}
	f := w.focus
	if f == nil || !f.enabled {
		return
	}
	switch f.kind {
	case kindButton, kindLink, kindCheck:
		if k.Key == KeyEnter || k.Char == ' ' {
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
	case k.Key == KeyEnter:
		if f == w.edtAddr {
			w.connect()
		}
	case k.Key == KeyBackspace:
		fd.erase(true)
	case k.Key == KeyDelete:
		fd.erase(false)
	case k.Key == KeyLeft:
		if k.Word {
			fd.move(fd.wordLeft(), k.Shift)
		} else if a, _ := fd.selection(); !k.Shift && fd.caret != fd.anchor {
			fd.move(a, false)
		} else {
			fd.move(fd.caret-1, k.Shift)
		}
	case k.Key == KeyRight:
		if k.Word {
			fd.move(fd.wordRight(), k.Shift)
		} else if _, b := fd.selection(); !k.Shift && fd.caret != fd.anchor {
			fd.move(b, false)
		} else {
			fd.move(fd.caret+1, k.Shift)
		}
	case k.Key == KeyHome:
		fd.move(0, k.Shift)
	case k.Key == KeyEnd:
		fd.move(len(fd.text), k.Shift)
	case k.Cmd && k.Char == 'a':
		fd.selectAll()
	case k.Cmd && k.Char == 'c', k.Cmd && k.Key == KeyInsert:
		if s := fd.selected(); s != "" {
			w.host.Copy(s)
		}
	case k.Cmd && k.Char == 'x':
		if s := fd.selected(); s != "" && w.host.Copy(s) {
			fd.erase(true)
		}
	case k.Cmd && k.Char == 'v', k.Shift && k.Key == KeyInsert:
		w.host.RequestPaste()
	case k.Text != "":
		fd.insert(k.Text)
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
	w.host.Put(w.back)
}

func (w *Window) drawDialog(c *canvas, d *dialog) {
	top := w.px(24)
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
