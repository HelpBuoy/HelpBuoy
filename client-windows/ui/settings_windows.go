//go:build windows

package ui

import (
	"strings"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
)

// showSettings opens the settings and about window: what this PC knows about
// its relay and key, with Reset and Remove key.
func (w *Window) showSettings() {
	in := w.ctrl.Info()
	or := func(s, alt string) string {
		if s == "" {
			return alt
		}
		return s
	}
	lines := []string{
		"Relay address: " + or(in.RelayAddress, "not set"),
		"Relay fingerprint: " + or(in.RelayFingerprint, "not connected"),
		"Registered as: " + or(in.Name, "not registered yet"),
		"",
		"Key fingerprint: " + or(in.KeyFingerprint, "no key"),
		"Key: " + in.KeyLine,
		"Key folder: " + in.KeyDir,
	}
	if in.LastHelper != "" {
		lines = append(lines, "", "Last helper: "+in.LastHelper)
	}
	lines = append(lines, "", "help-me-win "+in.Version)
	text := strings.Join(lines, "\r\n")

	dpi := w.dpi
	sc := func(v int) int { return v * dpi / 96 }
	dlg := ui.NewModal(w.wnd, ui.OptsModal().
		Title("Settings").
		Size(sc(480), sc(330)))
	info := ui.NewEdit(dlg, ui.OptsEdit().
		Text(text).
		Position(sc(16), sc(16)).
		Width(sc(448)).Height(sc(250)).
		CtrlStyle(co.ES_MULTILINE|co.ES_READONLY|co.ES_AUTOVSCROLL).
		WndStyle(co.WS_CHILD|co.WS_VISIBLE|co.WS_TABSTOP|co.WS_VSCROLL))
	btnReset := ui.NewButton(dlg, ui.OptsButton().Text("Re&set...").
		Position(sc(16), sc(282)).Width(sc(96)).Height(sc(30)))
	btnRemove := ui.NewButton(dlg, ui.OptsButton().Text("Remove &key...").
		Position(sc(120), sc(282)).Width(sc(120)).Height(sc(30)))
	btnClose := ui.NewButton(dlg, ui.OptsButton().Text("Close").
		Position(sc(368), sc(282)).Width(sc(96)).Height(sc(30)).CtrlStyle(co.BS_DEFPUSHBUTTON))

	closeDlg := func() { dlg.Hwnd().SendMessage(co.WM_CLOSE, 0, 0) }
	dlg.On().WmCreate(func(_ ui.WmCreate) int {
		for _, c := range []ui.Window{info, btnReset, btnRemove, btnClose} {
			c.Hwnd().SendMessage(co.WM_SETFONT, win.WPARAM(w.fonts[fontNormal]), 1)
		}
		btnClose.Focus()
		return 0
	})
	dlg.On().WmCommandAccelMenu(uint16(co.ID_CANCEL), closeDlg)
	dlg.On().WmCommandAccelMenu(uint16(co.ID_OK), closeDlg)
	btnClose.On().BnClicked(closeDlg)
	btnReset.On().BnClicked(func() {
		if !ui.MsgOkCancel(dlg, Title, "Forget the relay settings?",
			"This forgets the relay address and the relay and helper keys this PC has "+
				"learned, and starts over with the first window. The key of this PC is kept, "+
				"so your helper does not have to register it again.",
			"&Reset") {
			return
		}
		if err := w.ctrl.Reset(); err != nil {
			ui.MsgError(dlg, Title, "The settings could not be removed", err.Error())
		}
		closeDlg()
	})
	btnRemove.On().BnClicked(func() {
		closeDlg()
		w.confirmRemoveKey()
	})
	dlg.ShowModal()
}
