package views

import (
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/mevdschee/help-me-win/helper/identity"
	"github.com/mevdschee/help-me-win/helper/localrelay"
	"github.com/mevdschee/help-me-win/helper/ui"
)

// settingsPage is the state of the Settings page.
type settingsPage struct {
	copiedAt   time.Time
	confirmKey bool
}

// reset drops open confirmations.
func (p *settingsPage) reset() { p.confirmKey = false }

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

// drawSettings shows what this helper knows about its relay and key, the
// log of the relay inside this app while it runs, and Remove key. The relay
// address is chosen on the connect screen (Disconnect on the Clients tab).
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

	// The relay inside this app, while it runs.
	if a.local != nil {
		y = section(c, "Relay on this computer", x, y, w)
		y += c.Paragraph(ui.Regular, ui.SizeSmall, "Clients use this computer's public address, which usually needs a port forward on the router.", x, y, w, ui.ColMuted)
		y += 8
		st := a.local.Status()
		stText, stCol := st.State.String(), ui.ColMuted
		switch st.State {
		case localrelay.StateRunning, localrelay.StateExternal:
			stCol = ui.ColOK
		case localrelay.StateExited:
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
	a.key = nil
	a.keyRemoved = true
}
