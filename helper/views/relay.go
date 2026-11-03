package views

import (
	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/mevdschee/help-me-win/helper/ui"
)

func label(c *ui.Context, text string, x, y float32) float32 {
	c.Text(ui.Regular, ui.SizeSmall, text, x, y, ui.ColMuted)
	return y + 20
}

// drawRelay is the Relay tab: the connection status, the address and
// fingerprint to read out to users, and Disconnect.
func (a *App) drawRelay(x, y, w float32) float32 {
	c := a.ui
	c.Text(ui.Bold, ui.SizeHeading, "Relay", x, y, ui.ColText)
	y += 40
	text, detail, col := a.statusText()
	rl.DrawCircleV(rl.NewVector2(x+5, y+9), 4.5, col)
	c.Text(ui.Regular, ui.SizeBody, text, x+16, y, ui.ColText)
	y += 24
	if detail != "" {
		y += c.Paragraph(ui.Regular, ui.SizeSmall, detail, x+16, y, w-16, ui.ColMuted)
	}
	y += 12
	if a.demo == nil {
		y = label(c, "Address", x, y)
		c.Text(ui.Regular, ui.SizeBody, c.Ellipsize(ui.Regular, ui.SizeBody, a.addr.String(), w), x, y, ui.ColText)
		y += 30
	}
	y = label(c, "Relay fingerprint (clients show this on first contact)", x, y)
	if fp := a.relayFingerprint(); fp != "" {
		c.Text(ui.Mono, ui.SizeBody, fp, x, y, ui.ColText)
	} else {
		c.Text(ui.Regular, ui.SizeBody, "Not known yet; pinned on the first connection.", x, y, ui.ColMuted)
	}
	y += 34
	if a.demo == nil {
		busy := a.sessionCount() > 0
		r := rl.NewRectangle(x, y, 130, 34)
		if c.Button("relay/disconnect", r, "Disconnect", ui.Normal, busy) {
			a.disconnect()
		}
		if busy {
			c.Text(ui.Regular, ui.SizeSmall, "End the open sessions first.", x+142, y+9, ui.ColMuted)
		}
		y += 46
	}
	if a.cfgErr != nil {
		y += c.Paragraph(ui.Regular, ui.SizeSmall, "Settings file: "+a.cfgErr.Error(), x, y, w, ui.ColError)
		y += 8
	}

	y += 12
	c.Text(ui.Regular, ui.SizeSmall, "Help-Me-Win helper "+a.opts.Version, x, y, ui.ColMuted)
	return y + 20
}
