//go:build windows

package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"

	"github.com/helpbuoy/helpbuoy/proto/i18n"
	"github.com/helpbuoy/helpbuoy/proto/update"
)

// checkForUpdate is the update link on the waiting screen. It asks for the
// latest release and, when it is newer, installs it; otherwise it says this
// version is the latest.
func (w *Window) checkForUpdate() {
	if w.updating != "" {
		return
	}
	w.updating = i18n.Trans("Checking for updates...")
	w.apply(w.view)
	go func() {
		r, err := w.ctrl.LatestRelease(context.Background())
		if w.wnd.Hwnd() == 0 {
			return
		}
		w.wnd.UiThread(func() {
			if err == nil && r != nil {
				w.install(*r)
				return
			}
			w.updating = ""
			w.apply(w.view)
			w.dialogs++
			if err != nil {
				ui.MsgError(w.wnd, Title, i18n.Trans("Could not check for updates"), err.Error())
			} else {
				ui.MsgOk(w.wnd, Title, i18n.Trans("HelpBuoy is up to date"),
					i18n.Trans("You have the latest version, {0}.", w.ctrl.Version()))
			}
			w.dialogs--
		})
	}()
}

// install downloads r, starts msiexec and closes the app, which disconnects.
// The MSI starts the new version when it is done.
func (w *Window) install(r update.Release) {
	w.updating = i18n.Trans("Downloading HelpBuoy {0}...", r.Version)
	w.apply(w.view)
	go func() {
		path, err := update.Download(context.Background(), r, os.TempDir())
		if w.wnd.Hwnd() == 0 {
			return
		}
		w.wnd.UiThread(func() {
			if err == nil {
				// msiexec shows the installer; the MSI replaces the running
				// version, so the app closes to release helpbuoy.exe.
				log := filepath.Join(os.TempDir(), "helpbuoy-update.log")
				err = exec.Command("msiexec", "/i", path, "/l*v", log).Start()
			}
			if err != nil {
				w.updating = ""
				w.apply(w.view)
				w.dialogs++
				ui.MsgError(w.wnd, Title, i18n.Trans("The update could not be installed"), err.Error())
				w.dialogs--
				return
			}
			w.wnd.Hwnd().PostMessage(co.WM_CLOSE, 0, 0)
		})
	}()
}
