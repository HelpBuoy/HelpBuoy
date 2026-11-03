//go:build windows

package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"

	"github.com/mevdschee/help-me-win/client-windows/update"
)

// maybeOfferUpdate offers the release in the current view, once, when no
// dialog is open. The controller only puts a release in the view while the
// client is idle (no request, no session).
func (w *Window) maybeOfferUpdate() {
	r := w.view.Update
	if r == nil || w.updateAsked || w.dialogs > 0 || w.wnd.Hwnd() == 0 {
		return
	}
	w.updateAsked = true
	rel := *r
	// Ask outside the current message handler, so a render in progress is
	// not held up by the message box.
	go w.wnd.UiThread(func() { w.offerUpdate(rel) })
}

// offerUpdate asks whether to install r, then downloads it, starts msiexec
// and closes the app, which ends any session and disconnects.
func (w *Window) offerUpdate(r update.Release) {
	if w.view.Update == nil {
		w.updateAsked = false // no longer idle; offer it when it is again
		return
	}
	w.ctrl.DismissUpdate()
	version := w.ctrl.Version()
	msg := fmt.Sprintf("help-me-win %s is available (you have %s).\n\n"+
		"Download and install it now? help-me-win closes during the installation.\n\n"+
		"Update checks can be turned off in Settings.", r.Version, version)
	w.dialogs++
	ret, _ := w.wnd.Hwnd().MessageBox(msg, "Update available", co.MB_YESNO|co.MB_ICONINFORMATION)
	w.dialogs--
	if ret != co.ID_YES {
		return
	}
	w.updating = "Downloading help-me-win " + r.Version + "..."
	w.apply(w.view)
	go func() {
		path, err := update.Download(context.Background(), r, os.TempDir())
		if w.wnd.Hwnd() == 0 {
			return
		}
		w.wnd.UiThread(func() {
			if err == nil {
				// msiexec shows the installer; the MSI replaces the running
				// version, so the app closes to release help-me-win.exe.
				log := filepath.Join(os.TempDir(), "help-me-win-update.log")
				err = exec.Command("msiexec", "/i", path, "/l*v", log).Start()
			}
			if err != nil {
				w.updating = ""
				w.apply(w.view)
				ui.MsgError(w.wnd, Title, "The update could not be installed", err.Error())
				return
			}
			w.wnd.Hwnd().PostMessage(co.WM_CLOSE, 0, 0)
		})
	}()
}
