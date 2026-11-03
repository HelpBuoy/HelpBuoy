package views

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/mevdschee/helpbuoy/desk/ui"
	"github.com/mevdschee/helpbuoy/proto/update"
)

// updateWait is how long the start waits for the update check before it
// connects anyway. A later result is offered on the Server tab.
const updateWait = 5 * time.Second

type checkResult struct {
	r   *update.Release
	err error
}

type downloadResult struct {
	path string
	err  error
}

// updater is the update check at the start and the offer that follows. The
// start connects to the stored server only once the check is done and any
// offer was declined, like the Windows client.
type updater struct {
	checking    bool // the start waits for the check
	deadline    time.Time
	res         <-chan checkResult
	release     *update.Release // newer release to offer, until dismissed
	download    <-chan downloadResult
	downloading bool
	err         string
	autoConnect bool // connect to the stored server once the check allows it
}

// startUpdate runs Options.CheckUpdate in the background. Without it the
// stored server is connected to right away.
func (a *App) startUpdate() {
	u := &a.upd
	if a.opts.CheckUpdate == nil {
		a.connectStored()
		return
	}
	ch := make(chan checkResult, 1)
	u.res, u.checking, u.deadline = ch, true, time.Now().Add(updateWait)
	check := a.opts.CheckUpdate
	go func() {
		r, err := check(context.Background())
		ch <- checkResult{r, err}
	}()
}

// pollUpdate handles the check result, the wait running out and the
// download, once per frame.
func (a *App) pollUpdate() {
	u := &a.upd
	if u.res != nil {
		select {
		case res := <-u.res:
			u.res = nil
			waited := u.checking
			u.checking = false
			if res.err == nil && res.r != nil {
				u.release = res.r // the offer decides when to connect
			} else if waited {
				a.connectStored()
			}
		default:
			if u.checking && time.Now().After(u.deadline) {
				u.checking = false
				a.connectStored()
			}
		}
	}
	if u.download != nil {
		select {
		case res := <-u.download:
			u.download, u.downloading = nil, false
			err := res.err
			if err == nil {
				// msiexec shows the installer; the MSI replaces the running
				// version, so the app quits to release helpbuoy-desk.exe.
				log := filepath.Join(os.TempDir(), "helpbuoy-desk-update.log")
				err = exec.Command("msiexec", "/i", res.path, "/l*v", log).Start()
			}
			if err != nil {
				u.err = "The update could not be installed: " + err.Error()
				a.dismissUpdate()
				return
			}
			a.quit = true
		default:
		}
	}
}

// connectStored connects to the stored server, once, if the app was ever
// connected to it (its host key is pinned) and nothing else happened since.
func (a *App) connectStored() {
	u := &a.upd
	if !u.autoConnect {
		return
	}
	u.autoConnect = false
	if !a.connecting() && !a.reached && a.key != nil {
		a.startConnect()
	}
}

// dismissUpdate drops the offer; the start then connects.
func (a *App) dismissUpdate() {
	a.upd.release = nil
	a.connectStored()
}

// installUpdate downloads the offered MSI; pollUpdate starts it.
func (a *App) installUpdate() {
	u := &a.upd
	if u.release == nil || u.downloading {
		return
	}
	r := *u.release
	ch := make(chan downloadResult, 1)
	u.download, u.downloading, u.err = ch, true, ""
	go func() {
		path, err := update.Download(context.Background(), r, os.TempDir())
		ch <- downloadResult{path, err}
	}()
}

// drawUpdateOffer draws the offer with Install now and Not now (or the
// download in progress) and returns the bottom y. blocked disables Install
// now with a reason.
func (a *App) drawUpdateOffer(x, y, w float32, blocked string) float32 {
	c := a.ui
	u := &a.upd
	if u.err != "" {
		y += c.Paragraph(ui.Regular, ui.SizeSmall, u.err, x, y, w, ui.ColError)
		y += 12
	}
	if u.release == nil {
		return y
	}
	c.Text(ui.Bold, ui.SizeBody, "HelpBuoy Desk "+u.release.Version+" is available", x, y, ui.ColText)
	y += 26
	if u.downloading {
		y += c.Paragraph(ui.Regular, ui.SizeSmall, "Downloading HelpBuoy Desk "+u.release.Version+"…", x, y, w, ui.ColMuted)
		return y + 12
	}
	y += c.Paragraph(ui.Regular, ui.SizeSmall, "You have "+a.opts.Version+". HelpBuoy Desk closes during the installation.", x, y, w, ui.ColMuted)
	y += 10
	if c.Button("update/install", rl.NewRectangle(x, y, 140, 34), "Install now", ui.Primary, blocked != "") {
		a.installUpdate()
	}
	if c.Button("update/later", rl.NewRectangle(x+150, y, 110, 34), "Not now", ui.Normal, false) {
		a.dismissUpdate()
	}
	if blocked != "" {
		c.Text(ui.Regular, ui.SizeSmall, blocked, x+272, y+9, ui.ColMuted)
	}
	return y + 46
}
