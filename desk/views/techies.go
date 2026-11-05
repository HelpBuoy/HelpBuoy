package views

import (
	"sort"
	"strings"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/mevdschee/helpbuoy/desk/ui"
	"github.com/mevdschee/helpbuoy/proto/control"
	"github.com/mevdschee/helpbuoy/proto/i18n"
)

// sortedTechies returns the techies of the presence, online first, each
// group sorted by name.
func sortedTechies(p *control.Presence) []control.TechieInfo {
	if p == nil {
		return nil
	}
	hs := append([]control.TechieInfo(nil), p.Techies...)
	sort.SliceStable(hs, func(i, j int) bool {
		if hs[i].Online != hs[j].Online {
			return hs[i].Online
		}
		return strings.ToLower(hs[i].Name) < strings.ToLower(hs[j].Name)
	})
	return hs
}

// helping returns the computers a techie is in session with, by name.
func helping(p *control.Presence, techie string) []string {
	var names []string
	if p == nil {
		return nil
	}
	for _, c := range p.Computers {
		if c.InSessionWith == techie {
			names = append(names, c.Name)
		}
	}
	sort.Strings(names)
	return names
}

// drawTechiesPage draws the Techies tab: every techie on the server with an
// online dot, and whom they are helping right now.
func (a *App) drawTechiesPage(r rl.Rectangle) {
	c := a.ui
	rl.DrawRectangleRec(r, ui.ColPanel)
	c.Text(ui.Bold, ui.SizeHeading, i18n.Trans("Techies"), r.X+16, r.Y+14, ui.ColText)
	a.addButton("techies/add", i18n.Trans("Add techie"), tabRegisterTechie, r)
	list := rl.NewRectangle(r.X, r.Y+56, r.Width, r.Height-56)
	rl.DrawLineV(rl.NewVector2(r.X, list.Y), rl.NewVector2(r.X+r.Width, list.Y), ui.ColBorder)
	if a.presence == nil {
		return
	}
	hs := sortedTechies(a.presence)
	if len(hs) == 0 {
		c.Text(ui.Regular, ui.SizeSmall, i18n.Trans("No techies on this server."), list.X+16, list.Y+14, ui.ColMuted)
		return
	}
	off := c.BeginScroll("techies", list, float32(len(hs))*rowH)
	y := list.Y + off
	for _, h := range hs {
		a.drawTechieRow(h, rl.NewRectangle(list.X, y, list.Width, rowH))
		y += rowH
	}
	c.EndScroll()
}

// drawTechieRow draws one techie: name, a status line, and Remove on hover
// for every techie but yourself, confirmed with a second click.
func (a *App) drawTechieRow(h control.TechieInfo, r rl.Rectangle) {
	c := a.ui
	id := "techie/" + h.Fingerprint
	hover, _ := c.Row(id, r, ui.ColPanel, false)
	rl.DrawLineV(rl.NewVector2(r.X+16, r.Y+r.Height), rl.NewVector2(r.X+r.Width, r.Y+r.Height), ui.ColHover)
	x := r.X + 16
	nameCol := ui.ColText
	if h.Online {
		rl.DrawCircleV(rl.NewVector2(x+4, r.Y+17), 4, ui.ColOK)
	} else {
		nameCol = ui.ColMuted
		rl.DrawCircleLines(int32(x+4), int32(r.Y+17), 4, ui.ColBorder)
	}
	x += 16

	me := h.Name == a.me
	confirming := a.confirmRemove == h.Fingerprint
	removeL, cancelL := i18n.Trans("Remove"), i18n.Trans("Cancel")
	wConfirm, wCancel, wRemove := a.btnW(removeL, 74), a.btnW(cancelL, 68), a.btnW(removeL, 66)
	actionW := float32(0)
	switch {
	case confirming:
		actionW = wConfirm + 6 + wCancel + 2
	case hover && !me:
		actionW = wRemove + 4
	}
	w := r.X + r.Width - x - actionW - 16
	name := h.Name
	if me {
		name = i18n.Trans("{0} (you)", name)
	}
	c.Text(ui.Regular, ui.SizeBody, c.Ellipsize(ui.Regular, ui.SizeBody, name, w), x, r.Y+8, nameCol)
	sub, subCol := h.Fingerprint, ui.ColMuted
	switch names := helping(a.presence, h.Name); {
	case len(names) > 0:
		sub, subCol = i18n.Trans("Helping {0}", strings.Join(names, ", ")), ui.ColAccent
	case !h.Online:
		sub = i18n.Trans("Offline · {0}", h.Fingerprint)
	}
	if n, ok := a.notes[h.Fingerprint]; ok {
		sub, subCol = n.text, ui.ColError
		if hover {
			c.Tooltip(n.text)
		}
	}
	if confirming {
		sub, subCol = i18n.Trans("Remove this techie?"), ui.ColError
	}
	c.Text(ui.Regular, ui.SizeSmall, c.Ellipsize(ui.Regular, ui.SizeSmall, sub, w), x, r.Y+30, subCol)

	ax := r.X + r.Width - actionW - 8
	ay := r.Y + (r.Height-30)/2
	switch {
	case confirming:
		if c.Button(id+"/confirm", rl.NewRectangle(ax, ay, wConfirm, 30), removeL, ui.Danger, false) {
			a.unregisterTechie(h)
		}
		if c.Button(id+"/cancel", rl.NewRectangle(ax+wConfirm+6, ay, wCancel, 30), cancelL, ui.Flat, false) {
			a.confirmRemove = ""
		}
	case hover && !me:
		if c.Button(id+"/remove", rl.NewRectangle(ax, ay, wRemove, 30), removeL, ui.Flat, !a.online()) {
			a.confirmRemove = h.Fingerprint
		}
	}
}

// unregisterTechie removes another techie from the server.
func (a *App) unregisterTechie(h control.TechieInfo) {
	a.confirmRemove = ""
	if a.backend == nil {
		return
	}
	fp := h.Fingerprint
	a.run(a.backend.UnregisterTechie(h.Name), func(err error) {
		if err != nil {
			a.notes[fp] = note{text: i18n.Trans("Could not remove: {0}", err), until: time.Now().Add(6 * time.Second)}
		}
	})
}
