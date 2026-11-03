//go:build windows

package ui

import (
	"strings"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"

	"github.com/mevdschee/help-me-win/client-windows/app"
)

// settingsDlg is the open settings window. It follows the controller's View
// for "Change relay address".
type settingsDlg struct {
	w        *Window
	dlg      *ui.Modal
	info     *ui.Edit
	edtRelay *ui.Edit
	btnRelay *ui.Button
	lblRelay *ui.Static
	relay    app.RelayChange
}

// settingsText is the read-only summary at the top of the settings window.
func settingsText(in app.Info) string {
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
	return strings.Join(lines, "\r\n")
}

// showSettings opens the settings and about window: what this PC knows about
// its relay and key, Change relay address, the update check, View key,
// Reset and Remove key.
func (w *Window) showSettings() {
	in := w.ctrl.Info()
	sc := func(v int) int { return v * w.dpi / 96 }
	dlg := ui.NewModal(w.wnd, ui.OptsModal().
		Title("Settings").
		Size(sc(480), sc(404)))
	s := &settingsDlg{w: w, dlg: dlg}
	s.info = ui.NewEdit(dlg, ui.OptsEdit().
		Text(settingsText(in)).
		Position(sc(16), sc(16)).
		Width(sc(448)).Height(sc(186)).
		CtrlStyle(co.ES_MULTILINE|co.ES_READONLY|co.ES_AUTOVSCROLL).
		WndStyle(co.WS_CHILD|co.WS_VISIBLE|co.WS_TABSTOP|co.WS_VSCROLL))
	lblChange := ui.NewStatic(dlg, ui.OptsStatic().Text("Change relay address:").
		Position(sc(16), sc(216)).Size(sc(448), sc(18)))
	s.edtRelay = ui.NewEdit(dlg, ui.OptsEdit().Text(in.RelayAddress).
		Position(sc(16), sc(238)).Width(sc(330)).Height(sc(26)))
	s.btnRelay = ui.NewButton(dlg, ui.OptsButton().Text("Co&nnect").
		Position(sc(356), sc(237)).Width(sc(108)).Height(sc(28)))
	s.lblRelay = ui.NewStatic(dlg, ui.OptsStatic().
		Position(sc(16), sc(272)).Size(sc(448), sc(38)).CtrlStyle(co.SS_LEFT|co.SS_NOPREFIX))
	chkUpdates := ui.NewCheckBox(dlg, ui.OptsCheckBox().Text("Check for &updates on startup").
		Position(sc(16), sc(318)).Size(sc(448), sc(22)).
		State(map[bool]co.BST{true: co.BST_CHECKED, false: co.BST_UNCHECKED}[in.CheckUpdates]))
	btnReset := ui.NewButton(dlg, ui.OptsButton().Text("Re&set...").
		Position(sc(16), sc(358)).Width(sc(96)).Height(sc(30)))
	btnRemove := ui.NewButton(dlg, ui.OptsButton().Text("Remove &key...").
		Position(sc(120), sc(358)).Width(sc(120)).Height(sc(30)))
	btnViewKey := ui.NewButton(dlg, ui.OptsButton().Text("&View key...").
		Position(sc(248), sc(358)).Width(sc(96)).Height(sc(30)))
	btnClose := ui.NewButton(dlg, ui.OptsButton().Text("Close").
		Position(sc(368), sc(358)).Width(sc(96)).Height(sc(30)).CtrlStyle(co.BS_DEFPUSHBUTTON))

	closeDlg := func() { dlg.Hwnd().SendMessage(co.WM_CLOSE, 0, 0) }
	connect := func() { w.ctrl.ChangeRelay(s.edtRelay.Text()) }
	dlg.On().WmCreate(func(_ ui.WmCreate) int {
		for _, c := range []ui.Window{s.info, lblChange, s.edtRelay, s.btnRelay, s.lblRelay,
			chkUpdates, btnReset, btnRemove, btnViewKey, btnClose} {
			c.Hwnd().SendMessage(co.WM_SETFONT, win.WPARAM(w.fonts[fontNormal]), 1)
		}
		s.edtRelay.LimitText(300)
		w.settings = s
		s.apply(w.view)
		btnClose.Focus()
		return 0
	})
	dlg.On().WmCtlColorStatic(func(p ui.WmCtlColor) win.HBRUSH {
		hdc := p.Hdc()
		hdc.SetBkMode(co.BKMODE_TRANSPARENT)
		switch h := p.HwndControl(); {
		case h == s.info.Hwnd():
			hdc.SetTextColor(win.GetSysColor(co.COLOR_WINDOWTEXT))
			br, _ := win.GetSysColorBrush(co.COLOR_WINDOW)
			return br
		case h == s.lblRelay.Hwnd() && s.relay.Error != "":
			hdc.SetTextColor(win.RGB(170, 20, 20))
		case h == s.lblRelay.Hwnd() && s.relay.Done == "":
			hdc.SetTextColor(win.GetSysColor(co.COLOR_GRAYTEXT))
		default:
			hdc.SetTextColor(win.GetSysColor(co.COLOR_WINDOWTEXT))
		}
		br, _ := win.GetSysColorBrush(co.COLOR_BTNFACE)
		return br
	})
	dlg.On().WmDestroy(func() {
		w.settings = nil
		w.ctrl.EndRelayChange()
	})
	dlg.On().WmCommandAccelMenu(uint16(co.ID_CANCEL), closeDlg)
	dlg.On().WmCommandAccelMenu(uint16(co.ID_OK), func() {
		// Enter in the address field connects; elsewhere it closes.
		if win.GetFocus() == s.edtRelay.Hwnd() {
			if s.relay.Blocked == "" && !s.relay.Connecting {
				connect()
			}
			return
		}
		closeDlg()
	})
	s.btnRelay.On().BnClicked(connect)
	chkUpdates.On().BnClicked(func() { w.ctrl.SetCheckUpdates(chkUpdates.IsChecked()) })
	btnClose.On().BnClicked(closeDlg)
	btnViewKey.On().BnClicked(func() { w.showKey(dlg) })
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
	w.settings = nil
	w.maybeOfferUpdate()
}

// apply shows the relay change part of v. It runs on the UI thread.
func (s *settingsDlg) apply(v app.View) {
	r := v.Relay
	prev := s.relay
	s.relay = r
	idle := r.Blocked == "" && !r.Connecting
	s.edtRelay.Hwnd().EnableWindow(idle)
	s.btnRelay.Hwnd().EnableWindow(idle)
	status := "The key of this PC stays the same. If the new relay does not know it yet, " +
		"this window shows the key to send to a helper there."
	switch {
	case r.Connecting:
		setText(s.btnRelay, "Connecting...")
		status = "Connecting to the new relay..."
	case r.Blocked != "":
		setText(s.btnRelay, "Co&nnect")
		status = "Not possible now. " + r.Blocked
	default:
		setText(s.btnRelay, "Co&nnect")
		if r.Error != "" {
			status = r.Error + " The current relay is still used."
		} else if r.Done != "" {
			status = r.Done
		}
	}
	setText(s.lblRelay, status)
	s.lblRelay.Hwnd().InvalidateRect(nil, true) // its color depends on s.relay
	setText(s.info, settingsText(s.w.ctrl.Info()))
	if prev.Connecting && !r.Connecting && r.Error != "" {
		s.edtRelay.Focus()
		s.edtRelay.SetSelection(0, -1)
	}
}
