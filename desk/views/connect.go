package views

import (
	"strconv"
	"strings"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/mevdschee/helpbuoy/desk/config"
	"github.com/mevdschee/helpbuoy/desk/identity"
	"github.com/mevdschee/helpbuoy/desk/localserver"
	"github.com/mevdschee/helpbuoy/desk/serverconn"
	"github.com/mevdschee/helpbuoy/desk/ui"
	"github.com/mevdschee/helpbuoy/proto/serveraddr"
)

// connectScreen is the state of the first screen: the server address and
// Connect, and the key to send when this key is not a techie on the server.
type connectScreen struct {
	addr         ui.Field
	err          string
	changedKey   bool // the server showed another host key than the pinned one
	confirmTrust bool
	copiedAt     time.Time
	boxH         float32 // height of the box in the last frame, to center it
}

func (s *connectScreen) init() {
	s.addr.Placeholder = "Local server on this computer"
	s.addr.Edit.Max = 260
}

// connecting reports whether a Connect is in progress or done.
func (a *App) connecting() bool { return a.conn != nil || a.localWait }

// startConnect is the Connect button. An empty address (or localhost)
// starts the server inside this app, with this techie's key added, and
// connects to it.
func (a *App) startConnect() {
	s := &a.cs
	if a.connecting() || a.key == nil {
		return
	}
	a.upd.autoConnect = false
	text := strings.TrimSpace(s.addr.Edit.String())
	if text == "" {
		text = "localhost"
	}
	ad, err := serveraddr.Parse(text)
	if err != nil {
		s.err = capitalize(err.Error()) + "."
		return
	}
	s.err, s.changedKey, s.confirmTrust = "", false, false
	s.addr.Edit.SetText(fieldText(ad))
	if a.cfg.SetServerAddr(ad) {
		a.saveConfig()
	}
	a.addr = ad
	if config.IsLocal(ad) {
		a.localAdded = 0
		a.local = localserver.Start(localserver.Options{
			Port: ad.Port, Version: a.opts.Version, TechieName: identity.TechieName(), KeyLine: a.key.Line,
		})
		a.localWait = true // the connection starts once the server listens
		return
	}
	a.openConn(ad.String())
}

// openConn starts the server connection to addr with the pinned host key.
func (a *App) openConn(addr string) {
	a.presence, a.me = nil, ""
	a.setServerInfo(nil)
	a.status = serverconn.StatusEvent{}
	a.hostKey = nil
	if k, err := config.ParseHostKey(a.cfg.ServerHostKey); err == nil && a.cfg.ServerHostKey != "" {
		a.hostKey = k
	}
	a.conn = serverconn.Start(serverconn.Options{Addr: addr, Signer: a.key.Signer, HostKey: a.cfg.ServerHostKey})
	a.backend = a.conn
}

// disconnect leaves the server, stops the local server and goes back to the
// connect screen. Open sessions end.
func (a *App) disconnect() {
	for i := len(a.tabs) - 1; i >= 0; i-- {
		a.closeTab(i) // only session tabs close
	}
	a.closeRegister()
	if a.conn != nil {
		a.conn.Close()
		a.conn, a.backend = nil, nil
	}
	if a.local != nil {
		a.local.Stop()
		a.local = nil
	}
	a.localWait, a.reached, a.notTechie = false, false, false
	a.presence, a.me = nil, ""
	a.setServerInfo(nil)
	a.status = serverconn.StatusEvent{}
	a.opening = map[string]bool{}
	a.waiting = map[string]*waitInfo{}
	a.activate(0)
}

// pollLocal starts the connection once the local server listens, or goes
// back to the connect screen if it could not start.
func (a *App) pollLocal() {
	if !a.localWait || a.local == nil {
		return
	}
	switch st := a.local.Status(); st.State {
	case localserver.StateRunning, localserver.StateExternal:
		a.localWait = false
		a.openConn("127.0.0.1:" + strconv.Itoa(a.addr.Port))
	case localserver.StateExited:
		msg := "The server on this computer could not start."
		if st.Err != nil {
			msg = "The server on this computer could not start: " + st.Err.Error()
		}
		a.disconnect()
		a.cs.err = msg
	}
}

// statusBeforeOnline handles a status of a connection that has not been
// online yet: errors that need the user go back to the connect screen.
func (a *App) statusBeforeOnline(ev serverconn.StatusEvent) {
	switch ev.Status {
	case serverconn.StatusAuthFailed:
		a.notTechie = true // shows the key screen; the connection keeps retrying
	case serverconn.StatusUnreachable:
		msg := "Nothing answered at this address."
		if ev.Err != nil {
			msg = "Could not connect: " + ev.Err.Error()
		}
		a.disconnect()
		a.cs.err = msg
	case serverconn.StatusIdentityChanged:
		a.disconnect()
		a.cs.err = "The server at this address shows a different key than the first time."
		a.cs.changedKey = true
	}
}

// drawStart draws the connect screen, or the key screen when the server does
// not know this key as a techie, in a centered box.
func (a *App) drawStart() {
	s := &a.cs
	w, h := ui.WindowSize()
	bw := min(w-48, 560)
	bh := max(s.boxH, 200)
	box := rl.NewRectangle((w-bw)/2, max((h-bh)/2, 24), bw, bh)
	rl.DrawRectangleRounded(box, 0.04, 8, ui.ColPanel)
	rl.DrawRectangleRoundedLinesEx(box, 0.04, 8, 1, ui.ColBorder)
	const pad = 32
	x, y, cw := box.X+pad, box.Y+pad, box.Width-2*pad
	if a.connecting() && a.notTechie {
		y = a.drawNotTechie(x, y, cw)
	} else {
		y = a.drawConnect(x, y, cw)
	}
	s.boxH = y - box.Y + pad
}

func (a *App) drawConnect(x, y, w float32) float32 {
	c := a.ui
	s := &a.cs
	busy := a.connecting() || a.upd.checking || a.upd.downloading
	if !a.connecting() && (a.upd.release != nil || a.upd.err != "") {
		y = a.drawUpdateOffer(x, y, w, "")
		rl.DrawLineV(rl.NewVector2(x, y), rl.NewVector2(x+w, y), ui.ColBorder)
		y += 24
	}
	c.Text(ui.Bold, ui.SizeHeading, "Connect to your computer", x, y, ui.ColText)
	y += 36
	y += c.Paragraph(ui.Regular, ui.SizeBody, "Type the server address you told the computer to use, for example helpme.example.com.", x, y, w, ui.ColMuted)
	y += 14
	fieldW := w - 130
	res := c.TextField("start/addr", rl.NewRectangle(x, y, fieldW, 38), &s.addr)
	if res.Changed {
		s.err, s.changedKey, s.confirmTrust = "", false, false
	}
	label := "Connect"
	if busy {
		label = "Connecting…"
	}
	if c.Button("start/connect", rl.NewRectangle(x+fieldW+10, y, 120, 38), label, ui.Primary, busy) || res.Submitted && !busy {
		a.startConnect()
	}
	y += 44
	if a.upd.checking {
		y += c.Paragraph(ui.Regular, ui.SizeSmall, "Checking for updates…", x, y, w, ui.ColMuted)
		return y
	}
	y += c.Paragraph(ui.Regular, ui.SizeSmall, "Leave empty to run a local server on this computer, with you as its techie.", x, y, w, ui.ColMuted)
	if s.err != "" {
		y += 10
		y += c.Paragraph(ui.Regular, ui.SizeSmall, s.err, x, y, w, ui.ColError)
	}
	if s.changedKey {
		y += 6
		y += c.Paragraph(ui.Regular, ui.SizeSmall, "Someone may be intercepting the connection, or the server was reinstalled. Check with the server admin (helpbuoy-server hostkey) before trusting the new key.", x, y, w, ui.ColMuted)
		y += 10
		if s.confirmTrust {
			if c.Button("start/trust/confirm", rl.NewRectangle(x, y, 190, 34), "Trust the new key", ui.Danger, false) {
				a.cfg.ForgetHostKey()
				a.saveConfig()
				s.changedKey, s.confirmTrust = false, false
				a.startConnect()
			}
			if c.Button("start/trust/cancel", rl.NewRectangle(x+200, y, 90, 34), "Cancel", ui.Normal, false) {
				s.confirmTrust = false
			}
		} else if c.Button("start/trust", rl.NewRectangle(x, y, 210, 34), "Trust the new key…", ui.Normal, false) {
			s.confirmTrust = true
		}
		y += 34
	}
	return y
}

// drawNotTechie is the key screen: the server answered but does not know
// this key as a techie. It keeps retrying, so adding the key is enough.
func (a *App) drawNotTechie(x, y, w float32) float32 {
	c := a.ui
	s := &a.cs
	c.Text(ui.Bold, ui.SizeHeading, "Send your key to get added", x, y, ui.ColText)
	y += 36
	y += c.Paragraph(ui.Regular, ui.SizeBody, "The server at "+a.addr.String()+" does not know this techie yet. Send this public key to a techie on it (Add techie on the Techies tab) or to its admin (helpbuoy-server techie add).", x, y, w, ui.ColMuted)
	y += 12
	for _, l := range c.Wrap(ui.Mono, ui.SizeSmall, a.key.Line, w) {
		c.Text(ui.Mono, ui.SizeSmall, l, x, y, ui.ColText)
		y += 18
	}
	y += 10
	if c.Button("start/copy", rl.NewRectangle(x, y, 170, 36), "Copy public key", ui.Primary, false) {
		rl.SetClipboardText(a.key.Line)
		s.copiedAt = c.Now
	}
	if c.Now.Sub(s.copiedAt) < 2*time.Second {
		c.TextIn(ui.Regular, ui.SizeSmall, "Copied", rl.NewRectangle(x+182, y, 100, 36), ui.ColOK)
	}
	y += 46
	c.Text(ui.Regular, ui.SizeSmall, "Key fingerprint: "+a.key.Fingerprint, x, y, ui.ColMuted)
	y += 24
	c.Text(ui.Regular, ui.SizeSmall, "This screen changes by itself once the key is added.", x, y, ui.ColMuted)
	y += 30
	if c.Button("start/back", rl.NewRectangle(x, y, 220, 34), "Use a different server address", ui.Flat, false) {
		a.disconnect()
	}
	return y + 34
}

// fieldText is how an address shows in the field: empty for the local
// server on the default port.
func fieldText(ad serveraddr.Addr) string {
	if config.IsLocal(ad) && ad.Port == serveraddr.DefaultPort {
		return ""
	}
	return ad.String()
}

// capitalize upper-cases the first letter of an error message.
func capitalize(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}
