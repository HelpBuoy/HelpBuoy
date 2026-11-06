package views

import (
	"context"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/desk/serverconn"
	"github.com/helpbuoy/helpbuoy/desk/ui"
	"github.com/helpbuoy/helpbuoy/proto/i18n"
	"github.com/helpbuoy/helpbuoy/proto/verify"
)

// verification is the verified server check of the current connection, for
// the Server tab (PLAN.md 12.5). The desk app checks the host key only: on
// its local server it connects to localhost, which is never in a file.
type verification struct {
	key    string             // host key and salt the check is for; "" for none
	cancel context.CancelFunc // ends the running check
	res    <-chan verify.Result
	got    *verify.Result
}

// startVerification starts a check when the connection comes online with a
// host key and salt it was not done for, and forgets it when the connection
// goes.
func (a *App) startVerification(ev serverconn.StatusEvent) {
	var hostKey ssh.PublicKey
	var salt []byte
	if ev.Status == serverconn.StatusOnline && ev.Hello != nil {
		hostKey = a.hostKey
		salt, _ = verify.ParseSalt(ev.Hello.Salt)
	}
	key := ""
	if a.opts.Verify != nil && hostKey != nil && salt != nil {
		key = string(hostKey.Marshal()) + string(salt)
	}
	if key == a.verif.key {
		return
	}
	if a.verif.cancel != nil {
		a.verif.cancel()
	}
	a.verif = verification{key: key}
	if key == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan verify.Result, 1)
	a.verif.cancel, a.verif.res = cancel, res
	check := a.opts.Verify
	go func() { res <- check(ctx, hostKey, salt) }()
}

// drawVerification shows whether the server is verified, below its
// fingerprint on the Server tab.
func (a *App) drawVerification(x, y, w float32) float32 {
	v := &a.verif
	if v.key == "" {
		return y
	}
	if v.got == nil {
		select {
		case r := <-v.res:
			v.got = &r
		default:
		}
	}
	c := a.ui
	y = label(c, i18n.Trans("Verified by HelpBuoy"), x, y)
	text, col := i18n.Trans("Checking..."), ui.ColMuted
	if v.got != nil {
		switch r := *v.got; r.State {
		case verify.Verified:
			text = i18n.Trans("Verified: {0}, for {1}", r.Company, strings.Join(r.Addresses, ", "))
			col = ui.ColText
		case verify.NotVerified:
			text = i18n.Trans("Not verified")
		default:
			text = i18n.Trans("Could not be checked")
		}
	}
	y += c.Paragraph(ui.Regular, ui.SizeBody, text, x, y, w, col)
	return y + 14
}
