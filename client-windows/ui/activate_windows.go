package ui

import (
	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/win"

	"github.com/helpbuoy/helpbuoy/proto/i18n"
)

// ShowRunning brings the main window of the running copy, process pid, to
// the front. If there is no such window, it says that HelpBuoy is already
// running.
func ShowRunning(pid int) {
	if pid > 0 {
		for _, h := range win.EnumWindows() {
			if !h.IsWindowVisible() {
				continue
			}
			if _, p, err := h.GetWindowThreadProcessId(); err != nil || int(p) != pid {
				continue
			}
			if owner, err := h.GetWindow(co.GW_OWNER); err == nil && owner != 0 {
				continue // a dialog; its owner is the main window
			}
			if h.IsIconic() {
				h.ShowWindow(co.SW_RESTORE)
			}
			h.SetForegroundWindow()
			return
		}
	}
	win.HWND(0).MessageBox(i18n.Trans("HelpBuoy is already running. Look for its window or its button on the taskbar."),
		Title, co.MB_ICONINFORMATION|co.MB_OK)
}
