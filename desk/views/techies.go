package views

import (
	"slices"
	"sort"
	"strings"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/helpbuoy/helpbuoy/desk/ui"
	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/proto/i18n"
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
	contentH := float32(0)
	for _, h := range hs {
		contentH += a.techieRowH(h)
	}
	off := c.BeginScroll("techies", list, contentH)
	y := list.Y + off
	for _, h := range hs {
		rh := a.techieRowH(h)
		a.drawTechieRow(h, rl.NewRectangle(list.X, y, list.Width, rh))
		y += rh
	}
	c.EndScroll()
}

// keyRowH is the height of one key in a techie's key list.
const keyRowH = 36

// techieRowH is the height of a techie row, with its keys when they show.
func (a *App) techieRowH(h control.TechieInfo) float32 {
	if a.keysOpen != h.Name {
		return rowH
	}
	return rowH + float32(len(h.Keys))*keyRowH + 8 + 30 + 14
}

// keysKnown reports whether the server tells the keys of techies; an older
// server has one key per techie and no commands to change them.
func (a *App) keysKnown() bool {
	return a.presence != nil && slices.ContainsFunc(a.presence.Techies, func(h control.TechieInfo) bool { return len(h.Keys) > 0 })
}

// daysAgo says how many calendar days before now t was: today, yesterday
// or a number of days.
func daysAgo(t, now time.Time) string {
	y1, m1, d1 := t.Local().Date()
	y2, m2, d2 := now.Local().Date()
	days := int(time.Date(y2, m2, d2, 12, 0, 0, 0, time.UTC).Sub(time.Date(y1, m1, d1, 12, 0, 0, 0, time.UTC)).Hours() / 24)
	switch {
	case days <= 0:
		return i18n.Trans("today")
	case days == 1:
		return i18n.Trans("yesterday")
	}
	return i18n.Trans("{0} days ago", days)
}

// keySeen describes when a key was last used: online now, last seen some
// days ago or never.
func keySeen(k control.TechieKeyInfo, now time.Time) string {
	switch {
	case k.Online:
		return i18n.Trans("Online now")
	case k.LastSeen.IsZero():
		return i18n.Trans("Never connected")
	}
	return i18n.Trans("Last seen {0}", daysAgo(k.LastSeen, now))
}

// techieStatus is the line under a techie's name: whom it helps, the
// fingerprint of the key it is online with, or when it was last seen.
func (a *App) techieStatus(h control.TechieInfo) (string, rl.Color) {
	if names := helping(a.presence, h.Name); len(names) > 0 {
		return i18n.Trans("Helping {0}", strings.Join(names, ", ")), ui.ColAccent
	}
	if h.Online {
		return h.Fingerprint, ui.ColMuted
	}
	if len(h.Keys) == 0 {
		return i18n.Trans("Offline · {0}", h.Fingerprint), ui.ColMuted
	}
	var last time.Time
	for _, k := range h.Keys {
		if k.LastSeen.After(last) {
			last = k.LastSeen
		}
	}
	if last.IsZero() {
		return i18n.Trans("Offline · never connected"), ui.ColMuted
	}
	return i18n.Trans("Offline · last seen {0}", daysAgo(last, time.Now())), ui.ColMuted
}

// drawTechieRow draws one techie: name, a status line, and on hover Keys
// and, for every techie but yourself, Remove, confirmed with a second click.
func (a *App) drawTechieRow(h control.TechieInfo, r rl.Rectangle) {
	c := a.ui
	id := "techie/" + h.Name
	hover, _ := c.Row(id, r, ui.ColPanel, false)
	hover = hover && c.Hover(rl.NewRectangle(r.X, r.Y, r.Width, rowH))
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
	open := a.keysOpen == h.Name
	if open {
		a.drawTechieKeys(h, x, r.Y+rowH, r.X+r.Width-x-16)
	}
	confirming := a.confirmRemove == h.Fingerprint
	removeL, cancelL, keysL := i18n.Trans("Remove"), i18n.Trans("Cancel"), i18n.Trans("Keys")
	wConfirm, wCancel, wRemove := a.btnW(removeL, 74), a.btnW(cancelL, 68), a.btnW(removeL, 66)
	wKeys := a.btnW(keysL, 66)
	showKeys := hover && !open && a.keysKnown()
	showRemove := hover && !me
	actionW := float32(0)
	switch {
	case confirming:
		actionW = wConfirm + 6 + wCancel + 2
	default:
		if showKeys {
			actionW += wKeys + 4
		}
		if showRemove {
			actionW += wRemove + 4
		}
	}
	w := r.X + r.Width - x - actionW - 16
	name := h.Name
	if me {
		name = i18n.Trans("{0} (you)", name)
	}
	c.Text(ui.Regular, ui.SizeBody, c.Ellipsize(ui.Regular, ui.SizeBody, name, w), x, r.Y+8, nameCol)
	sub, subCol := a.techieStatus(h)
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
	ay := r.Y + (rowH-30)/2
	switch {
	case confirming:
		if c.Button(id+"/confirm", rl.NewRectangle(ax, ay, wConfirm, 30), removeL, ui.Danger, false) {
			a.unregisterTechie(h)
		}
		if c.Button(id+"/cancel", rl.NewRectangle(ax+wConfirm+6, ay, wCancel, 30), cancelL, ui.Flat, false) {
			a.confirmRemove = ""
		}
	default:
		bx := ax
		if showKeys {
			if c.Button(id+"/keys", rl.NewRectangle(bx, ay, wKeys, 30), keysL, ui.Flat, false) {
				a.keysOpen, a.confirmTKey, a.confirmRemove = h.Name, "", ""
			}
			bx += wKeys + 4
		}
		if showRemove {
			if c.Button(id+"/remove", rl.NewRectangle(bx, ay, wRemove, 30), removeL, ui.Flat, !a.online()) {
				a.confirmRemove = h.Fingerprint
			}
		}
	}
}

// drawTechieKeys draws the keys of techie h below its row, one per computer
// the techie uses, each with when it was last used and Remove, then Add key
// and Close.
func (a *App) drawTechieKeys(h control.TechieInfo, x, y, w float32) {
	c := a.ui
	now := time.Now()
	removeL, cancelL := i18n.Trans("Remove"), i18n.Trans("Cancel")
	wRemove, wCancel := a.btnW(removeL, 66), a.btnW(cancelL, 68)
	for _, k := range h.Keys {
		id := "techiekey/" + k.Fingerprint
		mine := a.key != nil && k.Fingerprint == a.key.Fingerprint
		canRemove := len(h.Keys) > 1 && !mine
		confirming := a.confirmTKey == k.Fingerprint
		actionW := float32(0)
		switch {
		case confirming:
			actionW = wRemove + 6 + wCancel
		case canRemove:
			actionW = wRemove
		}
		tw := w - actionW - 12
		seen, col := keySeen(k, now), ui.ColMuted
		if k.Online {
			col = ui.ColOK
		}
		if mine {
			seen = i18n.Trans("{0} · this computer", seen)
		}
		if n, ok := a.notes[k.Fingerprint]; ok {
			seen, col = n.text, ui.ColError
		}
		if confirming {
			seen, col = i18n.Trans("Remove this key? {0} can then no longer connect from that computer.", h.Name), ui.ColError
		}
		c.Text(ui.Mono, ui.SizeSmall, c.Ellipsize(ui.Mono, ui.SizeSmall, k.Fingerprint, tw), x, y+2, ui.ColText)
		c.Text(ui.Regular, ui.SizeSmall, c.Ellipsize(ui.Regular, ui.SizeSmall, seen, tw), x, y+19, col)
		ax, ay := x+w-actionW, y+3
		switch {
		case confirming:
			if c.Button(id+"/confirm", rl.NewRectangle(ax, ay, wRemove, 30), removeL, ui.Danger, !a.online()) {
				a.removeTechieKey(h.Name, k.Fingerprint)
			}
			if c.Button(id+"/cancel", rl.NewRectangle(ax+wRemove+6, ay, wCancel, 30), cancelL, ui.Flat, false) {
				a.confirmTKey = ""
			}
		case canRemove:
			if c.Button(id+"/remove", rl.NewRectangle(ax, ay, wRemove, 30), removeL, ui.Flat, !a.online()) {
				a.confirmTKey = k.Fingerprint
			}
		}
		y += keyRowH
	}
	y += 8
	addL, closeL := i18n.Trans("Add key"), i18n.Trans("Close")
	wAdd, wClose := a.btnW(addL, 80), a.btnW(closeL, 68)
	if c.Button("techiekeys/"+h.Name+"/add", rl.NewRectangle(x, y, wAdd, 30), addL, ui.Normal, a.backend == nil) {
		a.openAddKey(h.Name)
	}
	if c.Button("techiekeys/"+h.Name+"/close", rl.NewRectangle(x+wAdd+6, y, wClose, 30), closeL, ui.Flat, false) {
		a.keysOpen, a.confirmTKey = "", ""
	}
}

// removeTechieKey removes one key of a techie from the server.
func (a *App) removeTechieKey(name, fp string) {
	a.confirmTKey = ""
	if a.backend == nil {
		return
	}
	a.run(a.backend.RemoveTechieKey(name, fp), func(err error) {
		if err != nil {
			a.notes[fp] = note{text: i18n.Trans("Could not remove: {0}", err), until: time.Now().Add(6 * time.Second)}
		}
	})
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
