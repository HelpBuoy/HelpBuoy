package views

import (
	"strconv"
	"strings"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/mevdschee/help-me-win/helper/identity"
	"github.com/mevdschee/help-me-win/helper/localrelay"
	"github.com/mevdschee/help-me-win/helper/relayconn"
	"github.com/mevdschee/help-me-win/helper/ui"
	"github.com/mevdschee/help-me-win/proto/relayaddr"
)

// settingsPage is the state of the Settings page.
type settingsPage struct {
	addr       ui.Field
	addrErr    string
	port       ui.Field
	portErr    string
	copiedAt   time.Time
	confirmKey bool
	confirmPin bool
}

func (p *settingsPage) init() {
	p.addr.Placeholder = "relay.example.com:2222"
	p.addr.Edit.Max = 260
	p.port.Edit.Max = 5
}

func section(c *ui.Context, title string, x, y, w float32) float32 {
	y += 8
	c.Text(ui.Bold, ui.SizeBody, title, x, y, ui.ColText)
	y += 26
	rl.DrawLineV(rl.NewVector2(x, y), rl.NewVector2(x+w, y), ui.ColBorder)
	return y + 14
}

func label(c *ui.Context, text string, x, y float32) float32 {
	c.Text(ui.Regular, ui.SizeSmall, text, x, y, ui.ColMuted)
	return y + 20
}

func (a *App) drawSettings(x, y, w float32) float32 {
	c := a.ui
	p := &a.set
	c.Text(ui.Bold, ui.SizeHeading, "Settings", x, y, ui.ColText)
	y += 34
	if a.cfgErr != nil {
		y += c.Paragraph(ui.Regular, ui.SizeSmall, "Settings file: "+a.cfgErr.Error(), x, y, w, ui.ColError)
		y += 8
	}

	// Relay.
	y = section(c, "Relay", x, y, w)
	y = label(c, "Relay address (hostname:port), the same one clients use", x, y)
	fieldW := min(w-130, 420)
	res := c.TextField("set/addr", rl.NewRectangle(x, y, fieldW, 38), &p.addr)
	if res.Changed {
		p.addrErr = ""
	}
	if c.Button("set/connect", rl.NewRectangle(x+fieldW+10, y, 110, 38), "Connect", ui.Primary, a.demo != nil) || res.Submitted {
		a.applyAddr()
	}
	y += 46
	if p.addrErr != "" {
		y += c.Paragraph(ui.Regular, ui.SizeSmall, p.addrErr, x, y, w, ui.ColError)
		y += 6
	}
	text, detail, col := a.statusText()
	rl.DrawCircleV(rl.NewVector2(x+5, y+9), 4.5, col)
	c.Text(ui.Regular, ui.SizeBody, text, x+16, y, ui.ColText)
	y += 24
	if detail != "" {
		y += c.Paragraph(ui.Regular, ui.SizeSmall, detail, x+16, y, w-16, ui.ColMuted)
	}
	y += 10
	y = label(c, "Relay fingerprint (clients show this on first contact)", x, y)
	if fp := a.relayFingerprint(); fp != "" {
		c.Text(ui.Mono, ui.SizeBody, fp, x, y, ui.ColText)
	} else {
		c.Text(ui.Regular, ui.SizeBody, "Not known yet; pinned on the first connection.", x, y, ui.ColMuted)
	}
	y += 30
	if a.status.Status == relayconn.StatusIdentityChanged && a.demo == nil {
		y += c.Paragraph(ui.Regular, ui.SizeSmall, "The relay at this address shows a different key than the first time. Someone may be intercepting the connection, or the relay was reinstalled. Check with the relay admin (hmw-relay hostkey) before trusting the new key.", x, y, w, ui.ColError)
		y += 8
		if p.confirmPin {
			if c.Button("set/pin/confirm", rl.NewRectangle(x, y, 190, 34), "Trust the new key", ui.Danger, false) {
				a.cfg.ForgetHostKey()
				a.saveConfig()
				p.confirmPin = false
				a.connect()
			}
			if c.Button("set/pin/cancel", rl.NewRectangle(x+200, y, 90, 34), "Cancel", ui.Normal, false) {
				p.confirmPin = false
			}
		} else if c.Button("set/pin", rl.NewRectangle(x, y, 230, 34), "Forget the pinned key…", ui.Normal, false) {
			p.confirmPin = true
		}
		y += 44
	}

	// This helper's key.
	y = section(c, "This helper", x, y, w)
	if a.key != nil {
		y = label(c, "Key fingerprint", x, y)
		c.Text(ui.Mono, ui.SizeBody, a.key.Fingerprint, x, y, ui.ColText)
		y += 28
		y = label(c, "Public key, for the relay admin (hmw-relay helper add)", x, y)
		c.Text(ui.Mono, ui.SizeSmall, c.Ellipsize(ui.Mono, ui.SizeSmall, a.key.Line, w), x, y, ui.ColText)
		y += 26
		if c.Button("set/copy", rl.NewRectangle(x, y, 160, 34), "Copy public key", ui.Normal, false) {
			rl.SetClipboardText(a.key.Line)
			p.copiedAt = c.Now
		}
		if c.Now.Sub(p.copiedAt) < 2*time.Second {
			c.TextIn(ui.Regular, ui.SizeSmall, "Copied", rl.NewRectangle(x+172, y, 100, 34), ui.ColOK)
		}
		y += 46
	}

	// Local relay.
	y = section(c, "Local relay", x, y, w)
	enabled := a.cfg.LocalRelay.Enabled
	if c.Checkbox("set/local", rl.NewRectangle(x, y, w, 28), "Run the relay on this machine", &enabled) && a.demo == nil {
		a.setLocal(enabled)
	}
	y += 36
	y = label(c, "Port", x, y)
	pr := c.TextField("set/port", rl.NewRectangle(x, y, 100, 36), &p.port)
	if pr.Changed {
		p.portErr = ""
	}
	if c.Button("set/port/apply", rl.NewRectangle(x+110, y, 80, 36), "Apply", ui.Normal, a.demo != nil) || pr.Submitted {
		a.applyPort()
	}
	y += 44
	if p.portErr != "" {
		c.Text(ui.Regular, ui.SizeSmall, p.portErr, x, y, ui.ColError)
		y += 22
	}
	y += c.Paragraph(ui.Regular, ui.SizeSmall, "The app starts "+localrelay.ExeName()+" from its own folder, connects to it on 127.0.0.1, and stops it on exit. Clients use the public address above, which usually needs a port forward on the router.", x, y, w, ui.ColMuted)
	y += 8
	if a.local != nil {
		st := a.local.Status()
		stText, stCol := st.State.String(), ui.ColMuted
		switch st.State {
		case localrelay.StateRunning, localrelay.StateExternal:
			stCol = ui.ColOK
		case localrelay.StateNotFound, localrelay.StateExited:
			stCol = ui.ColError
		}
		if st.Err != nil {
			stText += ": " + st.Err.Error()
		}
		rl.DrawCircleV(rl.NewVector2(x+5, y+9), 4.5, stCol)
		y += c.Paragraph(ui.Regular, ui.SizeBody, "Relay "+stText, x+16, y, w-16, ui.ColText)
		y += 6
		if len(st.Lines) > 0 {
			lines := st.Lines
			if len(lines) > 10 {
				lines = lines[len(lines)-10:]
			}
			box := rl.NewRectangle(x, y, w, float32(len(lines))*18+16)
			rl.DrawRectangleRec(box, ui.ColBackground)
			rl.DrawRectangleLinesEx(box, 1, ui.ColBorder)
			for i, l := range lines {
				c.Text(ui.Mono, 12, c.Ellipsize(ui.Mono, 12, l, w-16), x+8, y+8+float32(i)*18, ui.ColText)
			}
			y += box.Height + 8
		}
	}

	// Key removal.
	y = section(c, "Remove key", x, y, w)
	y += c.Paragraph(ui.Regular, ui.SizeSmall, "Deletes this app's key. The app closes; the next start creates a new key, which the relay admin must add again.", x, y, w, ui.ColMuted)
	y += 10
	if p.confirmKey {
		if c.Button("set/key/confirm", rl.NewRectangle(x, y, 160, 36), "Remove key", ui.Danger, a.demo != nil) {
			a.removeKey()
		}
		if c.Button("set/key/cancel", rl.NewRectangle(x+170, y, 90, 36), "Cancel", ui.Normal, false) {
			p.confirmKey = false
		}
	} else if c.Button("set/key", rl.NewRectangle(x, y, 160, 36), "Remove key…", ui.Normal, a.key == nil) {
		p.confirmKey = true
	}
	y += 48
	c.Text(ui.Regular, ui.SizeSmall, "help-me-win helper "+a.opts.Version, x, y, ui.ColMuted)
	return y + 20
}

// applyAddr stores the typed relay address and reconnects.
func (a *App) applyAddr() {
	p := &a.set
	ad, err := relayaddr.Parse(p.addr.Edit.String())
	if err != nil {
		p.addrErr = err.Error()
		return
	}
	p.addrErr = ""
	p.addr.Edit.SetText(ad.String())
	a.cfg.SetRelayAddr(ad)
	a.saveConfig()
	a.connect()
}

// applyPort stores the local relay port and restarts the local relay.
func (a *App) applyPort() {
	p := &a.set
	n, err := strconv.Atoi(strings.TrimSpace(p.port.Edit.String()))
	if err != nil || n < 1 || n > 65535 {
		p.portErr = "The port must be a number from 1 to 65535."
		return
	}
	p.portErr = ""
	if n == a.cfg.LocalRelay.ListenPort() {
		return
	}
	a.cfg.LocalRelay.Port = n
	a.saveConfig()
	if a.cfg.LocalRelay.Enabled {
		a.startLocal()
		a.connect()
	}
}

// setLocal turns the local relay on or off.
func (a *App) setLocal(on bool) {
	a.cfg.LocalRelay.Enabled = on
	a.saveConfig()
	a.startLocal()
	a.connect()
}

// removeKey deletes the app key after confirmation and ends everything that
// uses it.
func (a *App) removeKey() {
	a.set.confirmKey = false
	if a.dir == "" {
		return
	}
	if err := identity.Remove(a.dir); err != nil {
		a.cfgErr = err
		return
	}
	for len(a.tabs) > 0 {
		a.closeTab(0)
	}
	if a.conn != nil {
		a.conn.Close()
		a.conn, a.backend = nil, nil
	}
	if a.local != nil {
		a.local.Stop()
		a.local = nil
	}
	a.key = nil
	a.keyRemoved = true
}
