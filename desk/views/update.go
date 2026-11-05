package views

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/helpbuoy/helpbuoy/desk/ui"
	"github.com/helpbuoy/helpbuoy/proto/i18n"
	"github.com/helpbuoy/helpbuoy/proto/update"
)

type checkResult struct {
	r   *update.Release
	err error
}

type downloadResult struct {
	path string
	err  error
}

// updater is the update that starts when the user clicks Check for updates in the top
// right corner: the check, the download and the dialog that tells how it
// went.
type updater struct {
	check    <-chan checkResult
	download <-chan downloadResult
	version  string  // the release being downloaded
	dialog   *dialog // a message the user still has to close
}

// dialog is a modal message with an OK button.
type dialog struct {
	title, body string
}

// busy reports whether a check or download is running.
func (u *updater) busy() bool { return u.check != nil || u.download != nil }

// updateLinkW is the room the update link takes in the top right corner, 0
// where the app does not update itself.
func (a *App) updateLinkW() float32 {
	if a.opts.CheckUpdate == nil {
		return 0
	}
	label := i18n.Trans("Check for updates")
	if a.upd.busy() {
		label = a.updateStatus()
	}
	return a.ui.Measure(ui.Regular, ui.SizeSmall, label).X + 32
}

// updateStatus is shown instead of update while updating.
func (a *App) updateStatus() string {
	if a.upd.download != nil {
		return i18n.Trans("Downloading HelpBuoy Desk {0}…", a.upd.version)
	}
	return i18n.Trans("Checking for updates…")
}

// drawUpdateLink draws "Check for updates" in the top right corner, in a bar of height
// h, where the app updates itself (Options.CheckUpdate). Clicking it checks
// for a newer release and installs it; meanwhile it shows the progress.
func (a *App) drawUpdateLink(h float32) {
	if a.opts.CheckUpdate == nil {
		return
	}
	c := a.ui
	w, _ := ui.WindowSize()
	label := i18n.Trans("Check for updates")
	if a.upd.busy() {
		label = a.updateStatus()
	}
	m := c.Measure(ui.Regular, ui.SizeSmall, label)
	x, y := w-16-m.X, (h-m.Y)/2
	if c.Link("update", x, y, label, a.upd.busy()) {
		a.checkForUpdate()
	}
}

// checkForUpdate starts the check; pollUpdate handles the result.
func (a *App) checkForUpdate() {
	if a.upd.busy() || a.opts.CheckUpdate == nil {
		return
	}
	ch := make(chan checkResult, 1)
	a.upd.check = ch
	check := a.opts.CheckUpdate
	go func() {
		r, err := check(context.Background())
		ch <- checkResult{r, err}
	}()
}

// pollUpdate handles the check and the download, once per frame.
func (a *App) pollUpdate() {
	u := &a.upd
	if u.check != nil {
		select {
		case res := <-u.check:
			u.check = nil
			switch {
			case res.err != nil:
				u.dialog = &dialog{i18n.Trans("Could not check for updates"), res.err.Error()}
			case res.r == nil:
				u.dialog = &dialog{i18n.Trans("HelpBuoy Desk is up to date"),
					i18n.Trans("You have the latest version, {0}.", a.opts.Version)}
			case a.sessionCount() > 0:
				u.dialog = &dialog{i18n.Trans("HelpBuoy Desk {0} is available", res.r.Version),
					i18n.Trans("End the open sessions first, then click Check for updates again.")}
			default:
				a.startDownload(*res.r)
			}
		default:
		}
	}
	if u.download != nil {
		select {
		case res := <-u.download:
			u.download = nil
			err := res.err
			if err == nil {
				// msiexec shows the installer; the MSI replaces the running
				// version, so the app quits to release helpbuoy-desk.exe.
				// The MSI starts the new version when it is done.
				log := filepath.Join(os.TempDir(), "helpbuoy-desk-update.log")
				err = exec.Command("msiexec", "/i", res.path, "/l*v", log).Start()
			}
			if err != nil {
				u.dialog = &dialog{i18n.Trans("The update could not be installed"), err.Error()}
				return
			}
			a.quit = true
		default:
		}
	}
}

// startDownload downloads the MSI of r; pollUpdate starts it.
func (a *App) startDownload(r update.Release) {
	ch := make(chan downloadResult, 1)
	a.upd.download, a.upd.version = ch, r.Version
	go func() {
		path, err := update.Download(context.Background(), r, os.TempDir())
		ch <- downloadResult{path, err}
	}()
}

// dialogShown reports whether the update dialog is drawn this frame. It
// waits while a session is shown, so no input meant for the remote computer
// goes to the dialog, or the other way around.
func (a *App) dialogShown() bool {
	return a.upd.dialog != nil && a.activeSession() == nil
}

// drawDialog draws the update dialog over everything, dimming the rest. OK,
// Enter and Esc close it.
func (a *App) drawDialog() {
	d := a.upd.dialog
	c := a.ui
	w, h := ui.WindowSize()
	rl.DrawRectangleRec(rl.NewRectangle(0, 0, w, h), rl.Fade(ui.ColText, 0.35))
	const pad = 24
	bw := min(w-48, 440)
	inner := bw - 2*pad
	lines := len(c.Wrap(ui.Regular, ui.SizeBody, d.body, inner))
	bh := pad + 30 + float32(lines)*ui.SizeBody*1.35 + 20 + 34 + pad
	box := rl.NewRectangle((w-bw)/2, (h-bh)/2, bw, bh)
	rl.DrawRectangleRounded(box, 0.04, 8, ui.ColPanel)
	rl.DrawRectangleRoundedLinesEx(box, 0.04, 8, 1, ui.ColBorder)
	x, y := box.X+pad, box.Y+pad
	c.Text(ui.Bold, ui.SizeHeading, d.title, x, y, ui.ColText)
	y += 30
	y += c.Paragraph(ui.Regular, ui.SizeBody, d.body, x, y, inner, ui.ColMuted)
	y += 20
	okL := i18n.Trans("OK")
	bw2 := a.btnW(okL, 90)
	if c.Button("dialog/ok", rl.NewRectangle(box.X+box.Width-pad-bw2, y, bw2, 34), okL, ui.Primary, false) ||
		rl.IsKeyPressed(rl.KeyEnter) || rl.IsKeyPressed(rl.KeyKpEnter) || rl.IsKeyPressed(rl.KeyEscape) {
		a.upd.dialog = nil
	}
}
