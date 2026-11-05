//go:build windows

// Package ui is the computer's window (PLAN.md 6.2 and 6.3), built with
// windigo. It only renders the app.View it is given and forwards button
// presses to the app.Controller; all logic lives in package app.
package ui

import (
	"context"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"

	"github.com/helpbuoy/helpbuoy/client-windows/app"
	"github.com/helpbuoy/helpbuoy/client-windows/session"
	"github.com/helpbuoy/helpbuoy/proto/i18n"
)

// Title is the window title.
const Title = "HelpBuoy"

// dialogWidth is the width of the task dialogs in dialog units, wide
// enough for a SHA256 fingerprint on one line (task dialogs do not break
// long words, they cut them off).
const dialogWidth = 320

// Computer area size in 96-DPI units.
const clientW, clientH = 460, 330

// statusY is the position of the waiting screen's status line; it moves
// down by titleH when the server's title is shown above it.
const statusY, titleH = 160, 26

// fontKind selects one of the window's fonts.
type fontKind int

const (
	fontNormal fontKind = iota
	fontHeading
	fontBig
)

// item is a control with its position in 96-DPI units, so the layout can be
// redone when the window moves to a monitor with another DPI.
type item struct {
	ctl        ui.Window
	x, y, w, h int
	font       fontKind
}

// Window is the computer's only top-level window.
type Window struct {
	wnd    *ui.Main
	ctrl   *app.Controller
	iconID uint16

	// ScreenAddress
	lblHeading   *ui.Static
	lblAddrHint  *ui.Static
	edtAddr      *ui.Edit
	btnConnect   *ui.Button
	lblAddrError *ui.Static
	// ScreenKey
	lblKeyIntro  *ui.Static
	edtKey       *ui.Edit
	btnCopy      *ui.Button
	lblKeyFp     *ui.Static
	lblServerFp  *ui.Static
	lblKeyNotice *ui.Static
	lnkChange    *ui.SysLink
	// ScreenMain
	lblWaiting    *ui.Static
	lblStatus     *ui.Static
	lblInfoTitle  *ui.Static // the server's title, above the status line
	btnDisconnect *ui.Button
	lblComment    *ui.Static
	// ScreenKeyError
	lblKeyErr    *ui.Static
	btnRemoveKey *ui.Button

	dialogs     int    // modal windows open
	updateAsked bool   // the update in the view was offered
	serverAsked string // new server fingerprint the user was warned about
	updating    string // status while an update is downloaded

	items []item
	dpi   int
	fonts [3]win.HFONT

	view     app.View
	rendered bool
}

// New creates the window; it appears with Run. iconID is the icon resource.
func New(iconID uint16) *Window {
	w := &Window{iconID: iconID}
	w.wnd = ui.NewMain(
		ui.OptsMain().
			Title(Title).
			Center(true).
			Size(ui.Dpi(clientW, clientH)).
			ClassIconId(iconID),
	)
	p := w.wnd
	static := func(text string, style co.SS) *ui.Static {
		return ui.NewStatic(p, ui.OptsStatic().Text(text).Size(1, 1).CtrlStyle(style|co.SS_NOPREFIX))
	}
	button := func(text string, style co.BS) *ui.Button {
		return ui.NewButton(p, ui.OptsButton().Text(text).CtrlStyle(style))
	}
	link := func(text string) *ui.SysLink {
		// Not LWS_TRANSPARENT: the link paints the WM_CTLCOLORSTATIC brush,
		// which avoids stale backgrounds after the window is restored.
		return ui.NewSysLink(p, ui.OptsSysLink().Text(text).Size(1, 1).CtrlStyle(0))
	}

	w.lblHeading = static(i18n.Trans("Connect to a techie"), co.SS_LEFT)
	w.lblAddrHint = static(i18n.Trans("Type the server address a techie gave you, for example helpme.example.com."), co.SS_LEFT)
	w.edtAddr = ui.NewEdit(p, ui.OptsEdit())
	w.btnConnect = button(i18n.Trans("&Connect"), co.BS_DEFPUSHBUTTON)
	w.lblAddrError = static("", co.SS_LEFT)

	w.lblKeyIntro = static(i18n.Trans("Paste this key into a chat with a techie (for example WhatsApp)."), co.SS_LEFT)
	w.edtKey = ui.NewEdit(p, ui.OptsEdit().CtrlStyle(co.ES_MULTILINE|co.ES_READONLY|co.ES_AUTOVSCROLL))
	w.btnCopy = button(i18n.Trans("C&opy key"), co.BS_PUSHBUTTON)
	w.lblKeyFp = static("", co.SS_LEFT)
	w.lblServerFp = static("", co.SS_LEFT)
	w.lblKeyNotice = static("", co.SS_LEFT)
	w.lnkChange = link("<a>" + i18n.Trans("Use a different server address") + "</a>")

	w.lblWaiting = static("", co.SS_CENTER)
	w.lblStatus = static("", co.SS_CENTER)
	w.lblInfoTitle = static("", co.SS_CENTER)
	w.btnDisconnect = button(i18n.Trans("&Disconnect"), co.BS_PUSHBUTTON)
	w.lblComment = static("", co.SS_CENTER)

	w.lblKeyErr = static("", co.SS_LEFT)
	w.btnRemoveKey = button(i18n.Trans("Remove &key..."), co.BS_PUSHBUTTON)

	w.items = []item{
		{w.lblHeading, 24, 22, 412, 30, fontHeading},
		{w.lblAddrHint, 24, 62, 412, 36, fontNormal},
		{w.edtAddr, 24, 106, 300, 26, fontNormal},
		{w.btnConnect, 334, 105, 102, 28, fontNormal},
		{w.lblAddrError, 24, 146, 412, 80, fontNormal},

		{w.lblKeyIntro, 24, 18, 412, 36, fontNormal},
		{w.edtKey, 24, 56, 412, 62, fontNormal},
		{w.btnCopy, 24, 128, 412, 46, fontBig},
		// Label and fingerprint on separate lines, so a long translated
		// label never pushes the fingerprint out of view.
		{w.lblKeyFp, 24, 182, 412, 34, fontNormal},
		{w.lblServerFp, 24, 217, 412, 34, fontNormal},
		{w.lblKeyNotice, 24, 256, 412, 40, fontNormal},
		{w.lnkChange, 24, 302, 412, 20, fontNormal},

		{w.lblComment, 24, 44, 412, 34, fontHeading},
		{w.lblWaiting, 24, 104, 412, 40, fontBig},
		{w.lblInfoTitle, 24, 152, 412, 28, fontHeading},
		{w.lblStatus, 24, statusY, 412, 64, fontNormal},
		{w.btnDisconnect, 150, 258, 160, 32, fontNormal},

		{w.lblKeyErr, 24, 24, 412, 190, fontNormal},
		{w.btnRemoveKey, 24, 226, 160, 32, fontNormal},
	}
	w.events()
	return w
}

// Run shows the window and runs the message loop until it is closed. The
// controller is started once the window exists.
func (w *Window) Run(ctrl *app.Controller) int {
	w.ctrl = ctrl
	return w.wnd.RunAsMain()
}

// Render implements app.UI. It runs v on the UI thread and returns after it
// was applied.
func (w *Window) Render(v app.View) {
	if w.wnd.Hwnd() == 0 {
		return
	}
	w.wnd.UiThread(func() { w.apply(v) })
}

// ConfirmTechie implements app.UI with a modal dialog that asks whether the
// techie may take control. A changed techie key gets its own dialog with an
// error icon, a warning headline and Cancel as the default button.
func (w *Window) ConfirmTechie(ctx context.Context, h session.Techie, oldFP string) bool {
	if w.wnd.Hwnd() == 0 {
		return false
	}
	who := h.Name
	cfg := win.TASKDIALOGCONFIG{
		WindowTitle: Title,
		Flags:       co.TDF_ALLOW_DIALOG_CANCELLATION | co.TDF_POSITION_RELATIVE_TO_WINDOW,
		Width:       dialogWidth,
	}
	quoted := `"` + who + `"`
	if oldFP == "" {
		cfg.HMainIcon = win.TdcIconTdi(co.TDICON_WARNING)
		cfg.MainInstruction = i18n.Trans("Allow {0} to take control of this computer?", quoted)
		cfg.Content = i18n.Trans("Key fingerprint:") + "\n" + h.Fingerprint + "\n\n" +
			i18n.Trans("Only allow this if you asked this person for help. You can take back "+
				"control at any time by pressing Esc.")
		cfg.Buttons = []win.TASKDIALOG_BUTTON{
			{Id: co.ID_OK, Text: i18n.Trans("&Allow")},
			{Id: co.ID_CANCEL, Text: i18n.Trans("&Cancel")},
		}
		cfg.DefaultButtonId = uint16(co.ID_OK)
	} else {
		cfg.HMainIcon = win.TdcIconTdi(co.TDICON_ERROR)
		cfg.MainInstruction = i18n.Trans("Warning: the key of {0} has changed", quoted)
		cfg.Content = i18n.Trans("Key last time:") + "\n" + oldFP + "\n\n" +
			i18n.Trans("Key now:") + "\n" + h.Fingerprint + "\n\n" +
			i18n.Trans("This may be someone pretending to be a techie. Do not continue "+
				"unless the techie confirmed by phone that they have a new key.")
		cfg.Buttons = []win.TASKDIALOG_BUTTON{
			{Id: co.ID_CANCEL, Text: i18n.Trans("&Cancel")},
			{Id: co.ID_OK, Text: i18n.Trans("Allow &anyway")},
		}
		cfg.DefaultButtonId = uint16(co.ID_CANCEL)
	}
	// When the question becomes moot (the techie gave up, the server
	// connection was lost) the dialog is closed as if Cancel was pressed.
	stop := context.AfterFunc(ctx, func() {
		w.wnd.UiThread(w.closeTaskDialog)
	})
	defer stop()
	var ok bool
	w.wnd.UiThread(func() {
		if ctx.Err() != nil {
			return
		}
		w.bringToFront()
		cfg.HwndParent = w.wnd.Hwnd()
		id, err := win.TaskDialogIndirect(&cfg)
		ok = err == nil && id == co.ID_OK && ctx.Err() == nil
	})
	return ok
}

// closeTaskDialog closes the task dialog owned by the main window, if one
// is open; task dialogs treat WM_CLOSE as Cancel.
func (w *Window) closeTaskDialog() {
	main := w.wnd.Hwnd()
	if main == 0 {
		return
	}
	if dlg := main.GetLastActivePopup(); dlg != 0 && dlg != main {
		if cls, err := dlg.GetClassName(); err == nil && cls == "#32770" {
			dlg.PostMessage(co.WM_CLOSE, 0, 0)
		}
	}
}

func (w *Window) events() {
	w.wnd.On().WmCreate(func(_ ui.WmCreate) int {
		w.dpi = int(w.wnd.Hwnd().GetDpiForWindow())
		if w.dpi == 0 {
			w.dpi = ui.DpiY(96)
		}
		w.createFonts()
		w.resizeClient()
		w.fitButtons()
		w.layout()
		w.edtAddr.LimitText(300)
		w.apply(app.View{Screen: -1}) // hide everything until the first view
		w.ctrl.Start()
		return 0
	})
	w.wnd.On().WmDestroy(func() {
		w.ctrl.Close()
		for _, f := range w.fonts {
			if f != 0 {
				f.DeleteObject()
			}
		}
	})
	w.wnd.On().Wm(co.WM_DPICHANGED, func(p ui.Wm) uintptr {
		w.dpi = int(uint16(p.WParam))
		// lParam points to the suggested window rectangle.
		rc := *(**win.RECT)(unsafe.Pointer(&p.LParam))
		w.wnd.Hwnd().SetWindowPos(0, win.POINT{X: rc.Left, Y: rc.Top},
			win.SIZE{Cx: rc.Right - rc.Left, Cy: rc.Bottom - rc.Top}, co.SWP_NOZORDER|co.SWP_NOACTIVATE)
		w.createFonts()
		w.resizeClient()
		w.fitButtons()
		w.layout()
		return 0
	})
	w.wnd.On().WmCtlColorStatic(func(p ui.WmCtlColor) win.HBRUSH {
		hdc := p.Hdc()
		hdc.SetBkMode(co.BKMODE_TRANSPARENT)
		errColor := win.RGB(170, 20, 20)
		switch h := p.HwndControl(); {
		case h == w.lblAddrError.Hwnd() && (w.view.CheckingUpdate || w.updating != ""):
			hdc.SetTextColor(win.GetSysColor(co.COLOR_GRAYTEXT))
		case h == w.lblAddrError.Hwnd(), h == w.lblKeyErr.Hwnd():
			hdc.SetTextColor(errColor)
		case h == w.lblStatus.Hwnd() && w.view.Error != "":
			hdc.SetTextColor(errColor)
		case h == w.lblStatus.Hwnd(), h == w.lblKeyNotice.Hwnd():
			hdc.SetTextColor(win.GetSysColor(co.COLOR_GRAYTEXT))
		case h == w.edtKey.Hwnd():
			// The key box is read-only but should look like text to copy.
			hdc.SetTextColor(win.GetSysColor(co.COLOR_WINDOWTEXT))
			br, _ := win.GetSysColorBrush(co.COLOR_WINDOW)
			return br
		default:
			hdc.SetTextColor(win.GetSysColor(co.COLOR_WINDOWTEXT))
		}
		br, _ := win.GetSysColorBrush(co.COLOR_BTNFACE)
		return br
	})
	// Enter in the address field.
	w.wnd.On().WmCommandAccelMenu(uint16(co.ID_OK), func() {
		if w.view.Screen == app.ScreenAddress && !w.view.Connecting && w.updating == "" {
			w.connect()
		}
	})
	w.btnConnect.On().BnClicked(w.connect)
	w.btnCopy.On().BnClicked(func() { w.copyText(w.view.KeyLine, w.btnCopy) })
	w.btnRemoveKey.On().BnClicked(w.confirmRemoveKey)
	w.lnkChange.On().NmClick(func(_ *win.NMLINK) { w.ctrl.Disconnect() })
	w.btnDisconnect.On().BnClicked(func() {
		if w.view.InSession {
			w.ctrl.EndSession()
		} else {
			w.ctrl.Disconnect()
		}
	})
}

func (w *Window) connect() {
	w.ctrl.Connect(w.edtAddr.Text())
}

// scale converts 96-DPI units to pixels at the window's DPI.
func (w *Window) scale(v int) int32 { return int32(v * w.dpi / 96) }

// createFonts creates the fonts for the current DPI from the system message
// font, which Windows reports at the system DPI.
func (w *Window) createFonts() {
	var ncm win.NONCLIENTMETRICS
	ncm.SetCbSize()
	if err := win.SystemParametersInfo(co.SPI_GETNONCLIENTMETRICS,
		ncm.CbSize(), unsafe.Pointer(&ncm), 0); err != nil {
		return
	}
	sysDpi := int32(ui.DpiY(96))
	base := ncm.LfMessageFont
	base.Height = base.Height * int32(w.dpi) / sysDpi
	for i, f := range w.fonts {
		if f != 0 {
			f.DeleteObject()
		}
		lf := base
		switch fontKind(i) {
		case fontHeading:
			lf.Height = lf.Height * 135 / 100
			lf.Weight = co.FW_SEMIBOLD
		case fontBig:
			lf.Height = lf.Height * 160 / 100
			lf.Weight = co.FW_SEMIBOLD
		}
		w.fonts[i], _ = win.CreateFontIndirect(&lf)
	}
}

// resizeClient sets the window size so the client area is clientW x clientH
// at the current DPI.
func (w *Window) resizeClient() {
	h := w.wnd.Hwnd()
	wr, err1 := h.GetWindowRect()
	cr, err2 := h.GetClientRect()
	if err1 != nil || err2 != nil {
		return
	}
	frameW := (wr.Right - wr.Left) - (cr.Right - cr.Left)
	frameH := (wr.Bottom - wr.Top) - (cr.Bottom - cr.Top)
	h.SetWindowPos(0, win.POINT{}, win.SIZE{Cx: w.scale(clientW) + frameW, Cy: w.scale(clientH) + frameH},
		co.SWP_NOZORDER|co.SWP_NOMOVE|co.SWP_NOACTIVATE)
}

func (w *Window) layout() {
	for _, it := range w.items {
		h := it.ctl.Hwnd()
		h.MoveWindow(win.POINT{X: w.scale(it.x), Y: w.scale(it.y)},
			win.SIZE{Cx: w.scale(it.w), Cy: w.scale(it.h)}, true)
		h.SendMessage(co.WM_SETFONT, win.WPARAM(w.fonts[it.font]), 1)
	}
}

// contentW is the width of the content between the 24-unit side margins.
const contentW = clientW - 48

// item returns the layout entry of c.
func (w *Window) item(c ui.Window) *item {
	for i := range w.items {
		if w.items[i].ctl == c {
			return &w.items[i]
		}
	}
	return nil
}

// place moves c to its layout entry at the current DPI.
func (w *Window) place(c ui.Window) {
	if it := w.item(c); it != nil {
		c.Hwnd().MoveWindow(win.POINT{X: w.scale(it.x), Y: w.scale(it.y)},
			win.SIZE{Cx: w.scale(it.w), Cy: w.scale(it.h)}, true)
	}
}

// textW returns the width of text in font f, in 96-DPI units. The &
// accelerator prefix is not drawn, so it is not measured.
func (w *Window) textW(f fontKind, text string) int {
	h := w.wnd.Hwnd()
	hdc, err := h.GetDC()
	if err != nil {
		return 0
	}
	defer h.ReleaseDC(hdc)
	old, _ := hdc.SelectObjectFont(w.fonts[f])
	defer hdc.SelectObjectFont(old)
	sz, err := hdc.GetTextExtentPoint32(stripAccel(text))
	if err != nil || w.dpi == 0 {
		return 0
	}
	return int(sz.Cx) * 96 / w.dpi
}

// stripAccel removes the & accelerator prefixes; && stays a single &.
func stripAccel(s string) string {
	var b []rune
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] == '&' {
			if i+1 < len(rs) && rs[i+1] == '&' {
				i++
			} else {
				continue
			}
		}
		b = append(b, rs[i])
	}
	return string(b)
}

// fitButtons sizes the window's buttons to their (translated) text, at
// least their English width and at most the content width, keeping each
// one's anchor: Connect stays right-aligned (the address field gives up the
// room), Disconnect stays centered and Remove key stays left-aligned. It
// only updates the layout entries; layout or place moves the controls.
func (w *Window) fitButtons() {
	fit := func(b *ui.Button, minW int) *item {
		it := w.item(b)
		it.w = min(contentW, max(minW, w.textW(it.font, b.Text())+32))
		return it
	}
	right := 24 + contentW
	c := fit(w.btnConnect, 102)
	c.x = right - c.w
	w.item(w.edtAddr).w = c.x - 10 - 24
	d := fit(w.btnDisconnect, 160)
	d.x = clientW/2 - d.w/2
	fit(w.btnRemoveKey, 160)
}

// moveStatus puts the status line of the waiting screen below the server's
// title when it is shown, and in the title's place when not.
func (w *Window) moveStatus(belowTitle bool) {
	y := statusY
	if belowTitle {
		y += titleH
	}
	for i := range w.items {
		if w.items[i].ctl == w.lblStatus && w.items[i].y != y {
			w.items[i].y = y
			it := w.items[i]
			w.lblStatus.Hwnd().MoveWindow(win.POINT{X: w.scale(it.x), Y: w.scale(it.y)},
				win.SIZE{Cx: w.scale(it.w), Cy: w.scale(it.h)}, true)
		}
	}
}

func show(c ui.Window, on bool) {
	if on {
		c.Hwnd().ShowWindow(co.SW_SHOW)
	} else {
		c.Hwnd().ShowWindow(co.SW_HIDE)
	}
}

func setText(c ui.Window, s string) {
	if cur, err := c.Hwnd().GetWindowText(); err != nil || cur != s {
		c.Hwnd().SetWindowText(s)
	}
}

// apply shows v. It runs on the UI thread.
func (w *Window) apply(v app.View) {
	prev := w.view
	entered := !w.rendered || prev.Screen != v.Screen

	addr := v.Screen == app.ScreenAddress
	for _, c := range []ui.Window{w.lblHeading, w.lblAddrHint, w.edtAddr, w.btnConnect, w.lblAddrError} {
		show(c, addr)
	}
	if addr {
		if entered && v.Address != "" {
			w.edtAddr.SetText(v.Address)
		}
		w.edtAddr.Hwnd().EnableWindow(!v.Connecting && w.updating == "")
		w.btnConnect.Hwnd().EnableWindow(!v.Connecting && w.updating == "")
		if w.updating != "" {
			setText(w.lblAddrError, w.updating)
		} else if v.Connecting {
			setText(w.btnConnect, i18n.Trans("Connecting..."))
			if v.CheckingUpdate {
				setText(w.lblAddrError, i18n.Trans("Checking for updates..."))
			} else {
				setText(w.lblAddrError, "")
			}
		} else {
			setText(w.btnConnect, i18n.Trans("&Connect"))
			setText(w.lblAddrError, v.Error)
			if entered || prev.Connecting {
				w.edtAddr.Focus()
				w.edtAddr.SetSelection(0, -1)
			}
		}
	}

	key := v.Screen == app.ScreenKey
	for _, c := range []ui.Window{w.lblKeyIntro, w.edtKey, w.btnCopy, w.lblKeyFp, w.lblServerFp, w.lblKeyNotice} {
		show(c, key)
	}
	show(w.lnkChange, key)
	if key {
		setText(w.edtKey, v.KeyLine)
		setText(w.lblKeyFp, i18n.Trans("Key fingerprint:\n{0}", v.KeyFingerprint))
		setText(w.lblServerFp, i18n.Trans("Server fingerprint:\n{0}", v.ServerFingerprint))
		notice := v.Notice
		if notice == "" {
			notice = i18n.Trans("Waiting for a techie to add this key. This window changes by itself when that is done.")
		}
		setText(w.lblKeyNotice, notice)
		if entered {
			w.btnCopy.Focus()
		}
	}

	main := v.Screen == app.ScreenMain
	show(w.lblWaiting, main)
	show(w.lblStatus, main)
	title := main && v.Ready && v.Error == "" && v.Notice == "" && w.updating == "" && v.Title != ""
	show(w.lblInfoTitle, title)
	w.moveStatus(title)
	show(w.btnDisconnect, main)
	show(w.lblComment, main)
	if main {
		setText(w.lblComment, v.KeyComment)
		waiting := ""
		if v.Ready {
			waiting = i18n.Trans("Waiting for a techie")
		}
		setText(w.lblWaiting, waiting)
		status := v.Error
		if status == "" {
			status = v.Notice
		}
		if status == "" && v.Ready {
			// The server's instruction line, or the default text.
			setText(w.lblInfoTitle, v.Title)
			status = v.Instruction
		}
		if w.updating != "" {
			status = w.updating
		}
		setText(w.lblStatus, status)
		// During a session (window minimized) the button ends just the
		// session; afterwards it disconnects again.
		if v.InSession {
			setText(w.btnDisconnect, i18n.Trans("&End session"))
		} else {
			setText(w.btnDisconnect, i18n.Trans("&Disconnect"))
		}
		w.fitButtons()
		w.place(w.btnDisconnect)
		w.btnDisconnect.Hwnd().EnableWindow(w.updating == "")
	}

	keyErr := v.Screen == app.ScreenKeyError
	show(w.lblKeyErr, keyErr)
	show(w.btnRemoveKey, keyErr)
	if keyErr {
		setText(w.lblKeyErr, v.Error+"\n\n"+i18n.Trans("Folder: {0}", v.KeyDir)+"\n\n"+
			i18n.Trans("The key is never replaced on its own. Remove key deletes it and makes a new one; "+
				"a techie will have to register this PC again."))
	}

	caption := Title
	if v.Title != "" {
		caption = v.Title + " - " + Title
	}
	setText(w.wnd, caption)

	// During a session the window gets out of the way; afterwards it comes
	// back.
	if v.InSession && !prev.InSession {
		w.wnd.Hwnd().ShowWindow(co.SW_MINIMIZE)
	} else if !v.InSession && prev.InSession {
		w.bringToFront()
	}
	if v.Screen >= 0 {
		w.view = v
		w.rendered = true
	}
	w.lblStatus.Hwnd().InvalidateRect(nil, true) // its color depends on w.view
	w.lblAddrError.Hwnd().InvalidateRect(nil, true)
	w.maybeOfferUpdate()
	w.maybeWarnServerKey()
}

// maybeWarnServerKey shows the server key change warning once per new key.
func (w *Window) maybeWarnServerKey() {
	hk := w.view.ServerKeyChange
	if hk == nil || w.serverAsked == hk.NewFingerprint || w.dialogs > 0 || w.wnd.Hwnd() == 0 {
		return
	}
	w.serverAsked = hk.NewFingerprint
	change := *hk
	// Ask outside the current message handler, like the update offer.
	go w.wnd.UiThread(func() { w.warnServerKey(change) })
}

// warnServerKey asks whether to trust the server's new key. Cancel, the
// default, keeps the computer stopped.
func (w *Window) warnServerKey(hk session.HostKeyChangedError) {
	if w.view.ServerKeyChange == nil || w.view.ServerKeyChange.NewFingerprint != hk.NewFingerprint {
		return
	}
	w.bringToFront()
	cfg := win.TASKDIALOGCONFIG{
		HwndParent:      w.wnd.Hwnd(),
		WindowTitle:     Title,
		Flags:           co.TDF_ALLOW_DIALOG_CANCELLATION | co.TDF_POSITION_RELATIVE_TO_WINDOW,
		Width:           dialogWidth,
		HMainIcon:       win.TdcIconTdi(co.TDICON_ERROR),
		MainInstruction: i18n.Trans("Warning: the identity of the server has changed"),
		Content: i18n.Trans("Key last time:") + "\n" + hk.OldFingerprint + "\n\n" +
			i18n.Trans("Key now:") + "\n" + hk.NewFingerprint + "\n\n" +
			i18n.Trans("This may be someone pretending to be your server. HelpBuoy stopped connecting. "+
				"Only continue if a techie confirmed by phone that the server has a new key "+
				"and the new fingerprint above matches what they read to you."),
		Buttons: []win.TASKDIALOG_BUTTON{
			{Id: co.ID_CANCEL, Text: i18n.Trans("&Cancel")},
			{Id: co.ID_OK, Text: i18n.Trans("&Trust the new key")},
		},
		DefaultButtonId: uint16(co.ID_CANCEL),
	}
	w.dialogs++
	id, err := win.TaskDialogIndirect(&cfg)
	w.dialogs--
	if err == nil && id == co.ID_OK {
		w.ctrl.TrustServerKey(hk.NewFingerprint)
	}
}

// bringToFront restores the window and tries to make it the foreground
// window; if Windows refuses, the taskbar button flashes instead.
func (w *Window) bringToFront() {
	h := w.wnd.Hwnd()
	if h.IsIconic() {
		h.ShowWindow(co.SW_RESTORE)
	} else {
		h.ShowWindow(co.SW_SHOW)
	}
	if !h.SetForegroundWindow() {
		var fi win.FLASHWINFO
		fi.SetCbSize()
		fi.Hwnd = h
		fi.DwFlags = co.FLASHW_ALL | co.FLASHW_TIMERNOFG
		win.FlashWindowEx(&fi)
	}
}

// copyText puts text (the public key line) on the clipboard as Unicode
// text and shows "Copied" on btn for a moment.
func (w *Window) copyText(text string, btn *ui.Button) {
	if text == "" {
		return
	}
	u := utf16.Encode([]rune(text + "\x00"))
	data := unsafe.Slice((*byte)(unsafe.Pointer(&u[0])), len(u)*2)
	hc, err := win.OpenClipboard(w.wnd.Hwnd())
	if err != nil {
		return
	}
	hc.EmptyClipboard()
	err = hc.SetClipboardData(co.CF_UNICODETEXT, data)
	hc.CloseClipboard()
	if err != nil {
		return
	}
	setText(btn, i18n.Trans("Copied"))
	time.AfterFunc(2*time.Second, func() {
		if w.wnd.Hwnd() != 0 {
			w.wnd.UiThread(func() {
				if btn.Hwnd().IsWindow() {
					setText(btn, i18n.Trans("C&opy key"))
				}
			})
		}
	})
}

// confirmRemoveKey asks before Remove key. It builds the task dialog itself
// rather than using ui.MsgOkCancel, whose Cancel button is not translated.
func (w *Window) confirmRemoveKey() {
	cfg := win.TASKDIALOGCONFIG{
		HwndParent:      w.wnd.Hwnd(),
		WindowTitle:     Title,
		Flags:           co.TDF_ALLOW_DIALOG_CANCELLATION | co.TDF_POSITION_RELATIVE_TO_WINDOW,
		HMainIcon:       win.TdcIconTdi(co.TDICON_WARNING),
		MainInstruction: i18n.Trans("Remove the key of this PC?"),
		Content: i18n.Trans("This deletes the key and the settings of this app. A new key is made, and your " +
			"techie will have to register this PC again before you can ask for help."),
		Buttons: []win.TASKDIALOG_BUTTON{
			{Id: co.ID_OK, Text: i18n.Trans("&Remove key")},
			{Id: co.ID_CANCEL, Text: i18n.Trans("&Cancel")},
		},
	}
	id, err := win.TaskDialogIndirect(&cfg)
	if err != nil || id != co.ID_OK {
		return
	}
	if err := w.ctrl.RemoveKey(); err != nil {
		ui.MsgError(w.wnd, Title, i18n.Trans("The key could not be removed"), err.Error())
	}
}
