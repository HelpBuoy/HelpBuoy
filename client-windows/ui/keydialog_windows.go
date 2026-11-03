//go:build windows

package ui

import (
	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
)

// copyKeyText is the label of the Copy key buttons.
const copyKeyText = "C&opy key"

// showKey opens the View key window over parent: the public key line to
// send to a helper, its fingerprint and a big Copy key button, as on the
// first-run window.
func (w *Window) showKey(parent ui.Parent) {
	in := w.ctrl.Info()
	if in.KeyLine == "" {
		return
	}
	sc := func(v int) int { return v * w.dpi / 96 }
	dlg := ui.NewModal(parent, ui.OptsModal().
		Title("Key of this PC").
		Size(sc(444), sc(272)))
	intro := ui.NewStatic(dlg, ui.OptsStatic().
		Text("Paste this key into a chat with your helper (for example WhatsApp).").
		Position(sc(16), sc(16)).Size(sc(412), sc(36)).CtrlStyle(co.SS_LEFT|co.SS_NOPREFIX))
	edtKey := ui.NewEdit(dlg, ui.OptsEdit().Text(in.KeyLine).
		Position(sc(16), sc(54)).Width(sc(412)).Height(sc(62)).
		CtrlStyle(co.ES_MULTILINE|co.ES_READONLY|co.ES_AUTOVSCROLL))
	btnCopy := ui.NewButton(dlg, ui.OptsButton().Text(copyKeyText).
		Position(sc(16), sc(126)).Width(sc(412)).Height(sc(46)).CtrlStyle(co.BS_DEFPUSHBUTTON))
	lblFp := ui.NewStatic(dlg, ui.OptsStatic().Text("Key fingerprint: "+in.KeyFingerprint).
		Position(sc(16), sc(184)).Size(sc(412), sc(18)).CtrlStyle(co.SS_LEFT|co.SS_NOPREFIX))
	btnClose := ui.NewButton(dlg, ui.OptsButton().Text("Close").
		Position(sc(332), sc(226)).Width(sc(96)).Height(sc(30)))

	closeDlg := func() { dlg.Hwnd().SendMessage(co.WM_CLOSE, 0, 0) }
	copyKey := func() { w.copyText(in.KeyLine, btnCopy) }
	dlg.On().WmCreate(func(_ ui.WmCreate) int {
		for _, c := range []ui.Window{intro, edtKey, lblFp, btnClose} {
			c.Hwnd().SendMessage(co.WM_SETFONT, win.WPARAM(w.fonts[fontNormal]), 1)
		}
		btnCopy.Hwnd().SendMessage(co.WM_SETFONT, win.WPARAM(w.fonts[fontBig]), 1)
		btnCopy.Focus()
		return 0
	})
	dlg.On().WmCtlColorStatic(func(p ui.WmCtlColor) win.HBRUSH {
		hdc := p.Hdc()
		hdc.SetBkMode(co.BKMODE_TRANSPARENT)
		hdc.SetTextColor(win.GetSysColor(co.COLOR_WINDOWTEXT))
		if p.HwndControl() == edtKey.Hwnd() {
			br, _ := win.GetSysColorBrush(co.COLOR_WINDOW)
			return br
		}
		br, _ := win.GetSysColorBrush(co.COLOR_BTNFACE)
		return br
	})
	dlg.On().WmCommandAccelMenu(uint16(co.ID_CANCEL), closeDlg)
	dlg.On().WmCommandAccelMenu(uint16(co.ID_OK), copyKey)
	btnCopy.On().BnClicked(copyKey)
	btnClose.On().BnClicked(closeDlg)
	w.dialogs++
	dlg.ShowModal()
	w.dialogs--
	w.maybeOfferUpdate()
}
