//go:build windows

// Package ui is the client's window (PLAN.md 6.2 and 6.3), built with
// windigo. It only renders the app.View it is given and forwards button
// presses to the app.Controller; all logic lives in package app.
package ui

import (
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"

	"github.com/mevdschee/help-me-win/client-windows/app"
)

// Title is the window title.
const Title = "help-me-win"

// Client area size in 96-DPI units.
const clientW, clientH = 460, 330

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

// Window is the client's only top-level window.
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
	lblRelayFp   *ui.Static
	lblKeyNotice *ui.Static
	lnkChange    *ui.SysLink
	// ScreenMain
	lblName   *ui.Static
	btnHelp   *ui.Button
	lblStatus *ui.Static
	// ScreenKeyError
	lblKeyErr    *ui.Static
	btnRemoveKey *ui.Button
	// all screens
	lnkSettings *ui.SysLink

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

	w.lblHeading = static("Connect to your helper", co.SS_LEFT)
	w.lblAddrHint = static("Type the relay address your helper gave you, for example relay.example.com:2222.", co.SS_LEFT)
	w.edtAddr = ui.NewEdit(p, ui.OptsEdit())
	w.btnConnect = button("&Connect", co.BS_DEFPUSHBUTTON)
	w.lblAddrError = static("", co.SS_LEFT)

	w.lblKeyIntro = static("Paste this key into a chat with your helper (for example WhatsApp).", co.SS_LEFT)
	w.edtKey = ui.NewEdit(p, ui.OptsEdit().CtrlStyle(co.ES_MULTILINE|co.ES_READONLY|co.ES_AUTOVSCROLL))
	w.btnCopy = button("C&opy key", co.BS_PUSHBUTTON)
	w.lblKeyFp = static("", co.SS_LEFT)
	w.lblRelayFp = static("", co.SS_LEFT)
	w.lblKeyNotice = static("", co.SS_LEFT)
	w.lnkChange = link(`<a>Use a different relay address</a>`)

	w.lblName = static("", co.SS_CENTER)
	w.btnHelp = button("&Request help", co.BS_PUSHBUTTON)
	w.lblStatus = static("", co.SS_CENTER)

	w.lblKeyErr = static("", co.SS_LEFT)
	w.btnRemoveKey = button("Remove &key...", co.BS_PUSHBUTTON)

	w.lnkSettings = link(`<a>Settings</a>`)

	w.items = []item{
		{w.lblHeading, 24, 22, 412, 30, fontHeading},
		{w.lblAddrHint, 24, 62, 412, 36, fontNormal},
		{w.edtAddr, 24, 106, 300, 26, fontNormal},
		{w.btnConnect, 334, 105, 102, 28, fontNormal},
		{w.lblAddrError, 24, 146, 412, 80, fontNormal},

		{w.lblKeyIntro, 24, 18, 412, 36, fontNormal},
		{w.edtKey, 24, 56, 412, 62, fontNormal},
		{w.btnCopy, 24, 128, 412, 46, fontBig},
		{w.lblKeyFp, 24, 186, 412, 18, fontNormal},
		{w.lblRelayFp, 24, 206, 412, 18, fontNormal},
		{w.lblKeyNotice, 24, 234, 412, 40, fontNormal},
		{w.lnkChange, 24, 298, 260, 20, fontNormal},

		{w.lblName, 24, 44, 412, 34, fontHeading},
		{w.btnHelp, 80, 100, 300, 84, fontBig},
		{w.lblStatus, 24, 204, 412, 60, fontNormal},

		{w.lblKeyErr, 24, 24, 412, 190, fontNormal},
		{w.btnRemoveKey, 24, 226, 160, 32, fontNormal},

		{w.lnkSettings, 370, 298, 66, 20, fontNormal},
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

// ConfirmHelperKeyChange implements app.UI with a modal dialog.
func (w *Window) ConfirmHelperKeyChange(name, oldFP, newFP string) bool {
	if w.wnd.Hwnd() == 0 {
		return false
	}
	var ok bool
	w.wnd.UiThread(func() {
		w.bringToFront()
		ok = ui.MsgOkCancel(w.wnd, Title,
			"The key of helper "+name+" has changed",
			"This helper used a different key last time. If you did not expect this, "+
				"ask your helper by phone before you continue.\n\n"+
				"Before: "+oldFP+"\nNow: "+newFP,
			"&Continue anyway")
	})
	return ok
}

func (w *Window) events() {
	w.wnd.On().WmCreate(func(_ ui.WmCreate) int {
		w.dpi = int(w.wnd.Hwnd().GetDpiForWindow())
		if w.dpi == 0 {
			w.dpi = ui.DpiY(96)
		}
		w.createFonts()
		w.resizeClient()
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
		w.layout()
		return 0
	})
	w.wnd.On().WmCtlColorStatic(func(p ui.WmCtlColor) win.HBRUSH {
		hdc := p.Hdc()
		hdc.SetBkMode(co.BKMODE_TRANSPARENT)
		errColor := win.RGB(170, 20, 20)
		switch h := p.HwndControl(); {
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
		if w.view.Screen == app.ScreenAddress && !w.view.Connecting {
			w.connect()
		}
	})
	w.btnConnect.On().BnClicked(w.connect)
	w.btnCopy.On().BnClicked(w.copyKey)
	w.btnHelp.On().BnClicked(func() {
		if w.view.Button == app.ButtonCancel {
			w.ctrl.CancelRequest()
		} else {
			w.ctrl.RequestHelp()
		}
	})
	w.btnRemoveKey.On().BnClicked(w.confirmRemoveKey)
	w.lnkChange.On().NmClick(func(_ *win.NMLINK) { w.ctrl.ChangeAddress() })
	w.lnkSettings.On().NmClick(func(_ *win.NMLINK) { w.showSettings() })
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
		w.edtAddr.Hwnd().EnableWindow(!v.Connecting)
		w.btnConnect.Hwnd().EnableWindow(!v.Connecting)
		if v.Connecting {
			setText(w.btnConnect, "Connecting...")
			setText(w.lblAddrError, "")
		} else {
			setText(w.btnConnect, "&Connect")
			setText(w.lblAddrError, v.Error)
			if entered || prev.Connecting {
				w.edtAddr.Focus()
				w.edtAddr.SetSelection(0, -1)
			}
		}
	}

	key := v.Screen == app.ScreenKey
	for _, c := range []ui.Window{w.lblKeyIntro, w.edtKey, w.btnCopy, w.lblKeyFp, w.lblRelayFp, w.lblKeyNotice} {
		show(c, key)
	}
	show(w.lnkChange, key && v.CanChangeAddress)
	if key {
		setText(w.edtKey, v.KeyLine)
		setText(w.lblKeyFp, "Key fingerprint: "+v.KeyFingerprint)
		setText(w.lblRelayFp, "Relay fingerprint: "+v.RelayFingerprint)
		notice := v.Notice
		if notice == "" {
			notice = "Waiting for your helper to add this key. This window changes by itself when that is done."
		}
		setText(w.lblKeyNotice, notice)
		if entered {
			w.btnCopy.Focus()
		}
	}

	main := v.Screen == app.ScreenMain
	show(w.lblName, main)
	show(w.btnHelp, main)
	show(w.lblStatus, main)
	if main {
		setText(w.lblName, v.Name)
		if v.Button == app.ButtonCancel {
			setText(w.btnHelp, "&Cancel request")
		} else {
			setText(w.btnHelp, "&Request help")
		}
		w.btnHelp.Hwnd().EnableWindow(v.ButtonEnabled)
		status := v.Error
		if status == "" {
			status = v.Notice
		}
		setText(w.lblStatus, status)
		if entered {
			w.btnHelp.Focus()
		}
	}

	keyErr := v.Screen == app.ScreenKeyError
	show(w.lblKeyErr, keyErr)
	show(w.btnRemoveKey, keyErr)
	if keyErr {
		setText(w.lblKeyErr, v.Error+"\n\nFolder: "+v.KeyDir+
			"\n\nThe key is never replaced on its own. Remove key deletes it and makes a new one; "+
			"your helper will have to register this PC again.")
	}
	show(w.lnkSettings, v.Screen >= app.ScreenAddress && v.Screen <= app.ScreenMain)

	// During a session the window gets out of the way; afterwards it comes
	// back with the Request help button.
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

// copyKey puts the public key line on the clipboard as Unicode text.
func (w *Window) copyKey() {
	text := w.view.KeyLine
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
	setText(w.btnCopy, "Copied")
	time.AfterFunc(2*time.Second, func() {
		if w.wnd.Hwnd() != 0 {
			w.wnd.UiThread(func() { setText(w.btnCopy, "C&opy key") })
		}
	})
}

func (w *Window) confirmRemoveKey() {
	if !ui.MsgOkCancel(w.wnd, Title, "Remove the key of this PC?",
		"This deletes the key and the settings of this app. A new key is made, and your "+
			"helper will have to register this PC again before you can ask for help.",
		"&Remove key") {
		return
	}
	if err := w.ctrl.RemoveKey(); err != nil {
		ui.MsgError(w.wnd, Title, "The key could not be removed", err.Error())
	}
}
