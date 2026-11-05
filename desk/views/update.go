package views

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/helpbuoy/helpbuoy/desk/ui"
	"github.com/helpbuoy/helpbuoy/proto/i18n"
	"github.com/helpbuoy/helpbuoy/proto/update"
)

// updateDelay is how long after the start the update check runs, so it
// never holds up starting the app. A release it finds is offered on the
// Connect page and the Server tab.
const updateDelay = 10 * time.Second

type checkResult struct {
	r   *update.Release
	err error
}

type downloadResult struct {
	path string
	err  error
}

// updater is the update check, run once updateDelay after the start, and
// the offer that follows.
type updater struct {
	checkAt     time.Time // when to start the check; zero once started or never
	res         <-chan checkResult
	release     *update.Release // newer release to offer, until dismissed
	download    <-chan downloadResult
	downloading bool
	err         string
}

// scheduleUpdate lets pollUpdate run Options.CheckUpdate updateDelay from
// now. Without it there is no check.
func (a *App) scheduleUpdate() {
	if a.opts.CheckUpdate != nil {
		a.upd.checkAt = time.Now().Add(updateDelay)
	}
}

// pollUpdate starts the check when it is due and handles its result and the
// download, once per frame.
func (a *App) pollUpdate() {
	u := &a.upd
	if !u.checkAt.IsZero() && time.Now().After(u.checkAt) {
		u.checkAt = time.Time{}
		ch := make(chan checkResult, 1)
		u.res = ch
		check := a.opts.CheckUpdate
		go func() {
			r, err := check(context.Background())
			ch <- checkResult{r, err}
		}()
	}
	if u.res != nil {
		select {
		case res := <-u.res:
			u.res = nil
			if res.err == nil && res.r != nil {
				u.release = res.r
			}
		default:
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
				u.err = i18n.Trans("The update could not be installed: {0}", err)
				a.dismissUpdate()
				return
			}
			a.quit = true
		default:
		}
	}
}

// dismissUpdate drops the offer.
func (a *App) dismissUpdate() {
	a.upd.release = nil
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
	c.Text(ui.Bold, ui.SizeBody, i18n.Trans("HelpBuoy Desk {0} is available", u.release.Version), x, y, ui.ColText)
	y += 26
	if u.downloading {
		y += c.Paragraph(ui.Regular, ui.SizeSmall, i18n.Trans("Downloading HelpBuoy Desk {0}…", u.release.Version), x, y, w, ui.ColMuted)
		return y + 12
	}
	y += c.Paragraph(ui.Regular, ui.SizeSmall, i18n.Trans("You have {0}. HelpBuoy Desk closes during the installation.", a.opts.Version), x, y, w, ui.ColMuted)
	y += 10
	installL, laterL := i18n.Trans("Install now"), i18n.Trans("Not now")
	wi, wl := a.btnW(installL, 140), a.btnW(laterL, 110)
	if c.Button("update/install", rl.NewRectangle(x, y, wi, 34), installL, ui.Primary, blocked != "") {
		a.installUpdate()
	}
	if c.Button("update/later", rl.NewRectangle(x+wi+10, y, wl, 34), laterL, ui.Normal, false) {
		a.dismissUpdate()
	}
	if blocked != "" {
		c.Text(ui.Regular, ui.SizeSmall, blocked, x+wi+10+wl+12, y+9, ui.ColMuted)
	}
	return y + 46
}
