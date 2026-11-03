package views

import (
	"strconv"
	"strings"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/mevdschee/helpbuoy/desk/config"
	"github.com/mevdschee/helpbuoy/desk/identity"
	"github.com/mevdschee/helpbuoy/desk/localrelay"
	"github.com/mevdschee/helpbuoy/desk/relayconn"
	"github.com/mevdschee/helpbuoy/desk/ui"
	"github.com/mevdschee/helpbuoy/proto/relayaddr"
)

// connectScreen is the state of the first screen: the relay address and
// Connect, and the key to send when this key is not an agent on the relay.
type connectScreen struct {
	addr         ui.Field
	err          string
	changedKey   bool // the relay showed another host key than the pinned one
	confirmTrust bool
	copiedAt     time.Time
	boxH         float32 // height of the box in the last frame, to center it
}

func (s *connectScreen) init() {
	s.addr.Placeholder = "Local relay on this computer"
	s.addr.Edit.Max = 260
}

// connecting reports whether a Connect is in progress or done.
func (a *App) connecting() bool { return a.conn != nil || a.localWait }

// startConnect is the Connect button. An empty address (or localhost)
// starts the relay inside this app, with this agent's key added, and
// connects to it.
func (a *App) startConnect() {
	s := &a.cs
	if a.connecting() || a.key == nil {
		return
	}
	text := strings.TrimSpace(s.addr.Edit.String())
	if text == "" {
		text = "localhost"
	}
	ad, err := relayaddr.Parse(text)
	if err != nil {
		s.err = capitalize(err.Error()) + "."
		return
	}
	s.err, s.changedKey, s.confirmTrust = "", false, false
	s.addr.Edit.SetText(fieldText(ad))
	if a.cfg.SetRelayAddr(ad) {
		a.saveConfig()
	}
	a.addr = ad
	if config.IsLocal(ad) {
		a.localAdded = 0
		a.local = localrelay.Start(localrelay.Options{
			Port: ad.Port, Version: a.opts.Version, AgentName: identity.AgentName(), KeyLine: a.key.Line,
		})
		a.localWait = true // the connection starts once the relay listens
		return
	}
	a.openConn(ad.String())
}

// openConn starts the relay connection to addr with the pinned host key.
func (a *App) openConn(addr string) {
	a.presence, a.me = nil, ""
	a.status = relayconn.StatusEvent{}
	a.hostKey = nil
	if k, err := config.ParseHostKey(a.cfg.RelayHostKey); err == nil && a.cfg.RelayHostKey != "" {
		a.hostKey = k
	}
	a.conn = relayconn.Start(relayconn.Options{Addr: addr, Signer: a.key.Signer, HostKey: a.cfg.RelayHostKey})
	a.backend = a.conn
}

// disconnect leaves the relay, stops the local relay and goes back to the
// connect screen. Open sessions end.
func (a *App) disconnect() {
	for len(a.tabs) > fixedTabs {
		a.closeTab(fixedTabs)
	}
	if a.conn != nil {
		a.conn.Close()
		a.conn, a.backend = nil, nil
	}
	if a.local != nil {
		a.local.Stop()
		a.local = nil
	}
	a.localWait, a.reached, a.notAgent = false, false, false
	a.presence, a.me = nil, ""
	a.status = relayconn.StatusEvent{}
	a.opening = map[string]bool{}
	a.waiting = map[string]*waitInfo{}
	a.activate(0)
}

// pollLocal starts the connection once the local relay listens, or goes
// back to the connect screen if it could not start.
func (a *App) pollLocal() {
	if !a.localWait || a.local == nil {
		return
	}
	switch st := a.local.Status(); st.State {
	case localrelay.StateRunning, localrelay.StateExternal:
		a.localWait = false
		a.openConn("127.0.0.1:" + strconv.Itoa(a.addr.Port))
	case localrelay.StateExited:
		msg := "The relay on this computer could not start."
		if st.Err != nil {
			msg = "The relay on this computer could not start: " + st.Err.Error()
		}
		a.disconnect()
		a.cs.err = msg
	}
}

// statusBeforeOnline handles a status of a connection that has not been
// online yet: errors that need the user go back to the connect screen.
func (a *App) statusBeforeOnline(ev relayconn.StatusEvent) {
	switch ev.Status {
	case relayconn.StatusAuthFailed:
		a.notAgent = true // shows the key screen; the connection keeps retrying
	case relayconn.StatusUnreachable:
		msg := "Nothing answered at this address."
		if ev.Err != nil {
			msg = "Could not connect: " + ev.Err.Error()
		}
		a.disconnect()
		a.cs.err = msg
	case relayconn.StatusIdentityChanged:
		a.disconnect()
		a.cs.err = "The relay at this address shows a different key than the first time."
		a.cs.changedKey = true
	}
}

// drawStart draws the connect screen, or the key screen when the relay does
// not know this key as an agent, in a centered box.
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
	if a.connecting() && a.notAgent {
		y = a.drawNotAgent(x, y, cw)
	} else {
		y = a.drawConnect(x, y, cw)
	}
	s.boxH = y - box.Y + pad
}

func (a *App) drawConnect(x, y, w float32) float32 {
	c := a.ui
	s := &a.cs
	busy := a.connecting()
	c.Text(ui.Bold, ui.SizeHeading, "Connect to your client", x, y, ui.ColText)
	y += 36
	y += c.Paragraph(ui.Regular, ui.SizeBody, "Type the relay address you told the client to use, for example helpme.example.com.", x, y, w, ui.ColMuted)
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
	y += c.Paragraph(ui.Regular, ui.SizeSmall, "Leave empty to run a local relay on this computer, with you as its agent.", x, y, w, ui.ColMuted)
	if s.err != "" {
		y += 10
		y += c.Paragraph(ui.Regular, ui.SizeSmall, s.err, x, y, w, ui.ColError)
	}
	if s.changedKey {
		y += 6
		y += c.Paragraph(ui.Regular, ui.SizeSmall, "Someone may be intercepting the connection, or the relay was reinstalled. Check with the relay admin (helpbuoy-relay hostkey) before trusting the new key.", x, y, w, ui.ColMuted)
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

// drawNotAgent is the key screen: the relay answered but does not know
// this key as an agent. It keeps retrying, so adding the key is enough.
func (a *App) drawNotAgent(x, y, w float32) float32 {
	c := a.ui
	s := &a.cs
	c.Text(ui.Bold, ui.SizeHeading, "Send your key to get added", x, y, ui.ColText)
	y += 36
	y += c.Paragraph(ui.Regular, ui.SizeBody, "The relay at "+a.addr.String()+" does not know this agent yet. Send this public key to an agent on it (Register agent tab) or to its admin (helpbuoy-relay agent add).", x, y, w, ui.ColMuted)
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
	if c.Button("start/back", rl.NewRectangle(x, y, 220, 34), "Use a different relay address", ui.Flat, false) {
		a.disconnect()
	}
	return y + 34
}

// fieldText is how an address shows in the field: empty for the local
// relay on the default port.
func fieldText(ad relayaddr.Addr) string {
	if config.IsLocal(ad) && ad.Port == relayaddr.DefaultPort {
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
