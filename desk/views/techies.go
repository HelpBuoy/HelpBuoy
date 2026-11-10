package views

import (
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
	if !a.limited() {
		a.addButton("techies/add", i18n.Trans("Add techie"), tabRegisterTechie, r)
	}
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
		contentH += a.techieRowH(h, list.Width)
	}
	off := c.BeginScroll("techies", list, contentH)
	y := list.Y + off
	for _, h := range hs {
		rh := a.techieRowH(h, list.Width)
		a.drawTechieRow(h, rl.NewRectangle(list.X, y, list.Width, rh))
		y += rh
	}
	c.EndScroll()
}

// limitState is a techie's customers being changed on the Techies tab.
type limitState struct {
	name string          // techie being edited, or ""
	some bool            // limited to the customers in only
	only map[string]bool // customer ids
	busy bool
}

// techieRowH is the height of a techie row, with the customer choice when
// its limit is being changed.
func (a *App) techieRowH(h control.TechieInfo, w float32) float32 {
	if a.limitEdit.name != h.Name {
		return rowH
	}
	return rowH + a.limitEditH(w-48)
}

func (a *App) limitEditH(w float32) float32 {
	h := float32(34)
	if a.limitEdit.some {
		_, ch := a.layoutChips(a.customerChips(func(string) bool { return false }, false), 0, 0, w)
		h += ch + 8
	}
	return h + 30 + 14
}

// startLimit opens the customer choice of techie h.
func (a *App) startLimit(h control.TechieInfo) {
	a.confirmRemove = ""
	a.limitEdit = limitState{name: h.Name, some: h.Limited, only: map[string]bool{}}
	for _, id := range h.Customers {
		a.limitEdit.only[id] = true
	}
}

// drawLimitEdit draws the customer choice of a techie below its row: all
// customers, or some, with Save and Cancel.
func (a *App) drawLimitEdit(h control.TechieInfo, x, y, w float32) {
	c := a.ui
	e := &a.limitEdit
	id := "limit/" + h.Fingerprint
	all := !e.some
	if c.Checkbox(id+"/all", rl.NewRectangle(x, y, min(w, 420), 28), i18n.Trans("All customers, and computers without customer"), &all) {
		e.some = !all
	}
	y += 34
	var ids []string
	if e.some {
		chips := a.customerChips(func(id string) bool { return e.only[id] }, false)
		if key, ok := a.drawChips(id+"/only", chips, x, y, w, e.busy); ok {
			e.only[key] = !e.only[key]
		}
		_, ch := a.layoutChips(chips, x, y, w)
		y += ch + 8
		ids = []string{}
		for _, cu := range a.customerList() {
			if e.only[cu.ID] {
				ids = append(ids, cu.ID)
			}
		}
	}
	saveL, cancelL := i18n.Trans("Save"), i18n.Trans("Cancel")
	wSave, wCancel := a.btnW(saveL, 80), a.btnW(cancelL, 68)
	ok := !e.busy && a.online() && (!e.some || len(ids) > 0)
	if c.Button(id+"/save", rl.NewRectangle(x, y, wSave, 30), saveL, ui.Primary, !ok) {
		a.limitTechie(h, ids)
	}
	if c.Button(id+"/cancel", rl.NewRectangle(x+wSave+6, y, wCancel, 30), cancelL, ui.Flat, false) {
		a.limitEdit = limitState{}
	}
	if e.some && len(ids) == 0 {
		c.Text(ui.Regular, ui.SizeSmall, i18n.Trans("Choose at least one customer."), x+wSave+6+wCancel+12, y+7, ui.ColWarn)
	}
}

// limitTechie sends LimitTechie: ids nil lifts the limit.
func (a *App) limitTechie(h control.TechieInfo, ids []string) {
	if a.backend == nil {
		return
	}
	a.limitEdit.busy = true
	fp := h.Fingerprint
	a.run(a.backend.LimitTechie(h.Name, ids), func(err error) {
		if err != nil {
			a.limitEdit.busy = false
			a.notes[fp] = note{text: i18n.Trans("Could not change the customers: {0}", err), until: time.Now().Add(6 * time.Second)}
			return
		}
		a.limitEdit = limitState{}
	})
}

// drawTechieRow draws one techie: name, a status line, and Remove on hover
// for every techie but yourself, confirmed with a second click.
func (a *App) drawTechieRow(h control.TechieInfo, r rl.Rectangle) {
	c := a.ui
	id := "techie/" + h.Fingerprint
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
	manage := !me && !a.limited()
	editing := a.limitEdit.name == h.Name
	if editing {
		a.drawLimitEdit(h, x, r.Y+rowH, r.X+r.Width-x-16)
	}
	confirming := a.confirmRemove == h.Fingerprint
	removeL, cancelL, custL := i18n.Trans("Remove"), i18n.Trans("Cancel"), i18n.Trans("Customers")
	wConfirm, wCancel, wRemove := a.btnW(removeL, 74), a.btnW(cancelL, 68), a.btnW(removeL, 66)
	wCust := a.btnW(custL, 80)
	canLimit := manage && a.hasCustomers() && !editing
	actionW := float32(0)
	switch {
	case confirming:
		actionW = wConfirm + 6 + wCancel + 2
	case hover && manage:
		actionW = wRemove + 4
		if canLimit {
			actionW += wCust + 4
		}
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
	case h.Limited:
		only := i18n.Trans("Only for {0}", strings.Join(a.customerNames(h.Customers), ", "))
		if len(h.Customers) == 0 {
			only = i18n.Trans("No customers")
		}
		sub, subCol = only, ui.ColWarn
		if !h.Online {
			sub = i18n.Trans("Offline · {0}", only)
		}
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
	ay := r.Y + (rowH-30)/2
	switch {
	case confirming:
		if c.Button(id+"/confirm", rl.NewRectangle(ax, ay, wConfirm, 30), removeL, ui.Danger, false) {
			a.unregisterTechie(h)
		}
		if c.Button(id+"/cancel", rl.NewRectangle(ax+wConfirm+6, ay, wCancel, 30), cancelL, ui.Flat, false) {
			a.confirmRemove = ""
		}
	case hover && manage:
		bx := ax
		if canLimit {
			if c.Button(id+"/customers", rl.NewRectangle(bx, ay, wCust, 30), custL, ui.Flat, !a.online()) {
				a.startLimit(h)
			}
			bx += wCust + 4
		}
		if c.Button(id+"/remove", rl.NewRectangle(bx, ay, wRemove, 30), removeL, ui.Flat, !a.online()) {
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
