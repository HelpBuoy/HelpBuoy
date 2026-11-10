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

// Customers (PLAN.md 13): the computers of a server are grouped per customer.
// The Computers tab shows a
// section per customer once the server has any; without customers it looks
// as it always did.

// custState is the customer part of the Computers tab.
type custState struct {
	filter    ui.Field
	collapsed map[string]bool // sections the techie closed, by customer id ("" is no customer)
	offline   map[string]bool // sections whose Offline part is open

	adding   bool // the Add customer field shows at the top of the list
	addField ui.Field
	addBusy  bool
	addErr   string

	renaming    string // customer id whose name is being edited
	renameField ui.Field
	renameBad   bool
	busy        map[string]bool // customer ids with a rename or remove in flight
	confirm     string          // customer id awaiting Remove confirmation

	moving   string          // computer fingerprint whose Move choice is open
	moveBusy map[string]bool // computers with a MoveComputer in flight

	lastUsed string // customer id of the last computer registered from this app
}

func (s *custState) init() {
	s.filter.Placeholder = i18n.Trans("Filter computers and customers")
	s.addField.Placeholder = i18n.Trans("Customer name, e.g. Acme Ltd")
	s.addField.Edit.Max = control.MaxCustomerName
	s.renameField.Edit.Max = control.MaxCustomerName
	s.collapsed, s.offline = map[string]bool{}, map[string]bool{}
	s.busy, s.moveBusy = map[string]bool{}, map[string]bool{}
}

// reset drops the open fields and confirmations, when the Computers tab is left.
func (s *custState) reset() {
	s.adding, s.addErr = false, ""
	s.addField.Edit.SetText("")
	s.renaming, s.confirm, s.moving = "", "", ""
}

// Field ids of the customer text fields.
const (
	addCustomerID    = "customers/add"
	renameCustomerID = "customers/rename"
	filterID         = "computers/filter"
)

// hasCustomers reports whether the server has customers.
func (a *App) hasCustomers() bool { return a.presence != nil && len(a.presence.Customers) > 0 }

// customerList returns the customers sorted by name.
func (a *App) customerList() []control.CustomerInfo {
	if a.presence == nil {
		return nil
	}
	cs := slices.Clone(a.presence.Customers)
	sort.SliceStable(cs, func(i, j int) bool { return strings.ToLower(cs[i].Name) < strings.ToLower(cs[j].Name) })
	return cs
}

// customerName returns the name of the customer with the id, or "".
func (a *App) customerName(id string) string {
	if a.presence == nil || id == "" {
		return ""
	}
	for _, cu := range a.presence.Customers {
		if cu.ID == id {
			return cu.Name
		}
	}
	return ""
}

// customerNames returns the names of customer ids, sorted.
func (a *App) customerNames(ids []string) []string {
	var names []string
	for _, id := range ids {
		if n := a.customerName(id); n != "" {
			names = append(names, n)
		}
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	return names
}

// chip is one choice in a row of chips.
type chip struct {
	key, label string
	on         bool
}

const (
	chipH   = 30
	chipGap = 6
)

// layoutChips places chips left to right in width w from x, y, wrapping to
// new lines. It returns their rectangles and the height they take.
func (a *App) layoutChips(chips []chip, x, y, w float32) ([]rl.Rectangle, float32) {
	rs := make([]rl.Rectangle, len(chips))
	cx, cy := x, y
	for i, ch := range chips {
		bw := min(a.btnW(ch.label, 56)+8, w)
		if cx > x && cx+bw > x+w {
			cx, cy = x, cy+chipH+chipGap
		}
		rs[i] = rl.NewRectangle(cx, cy, bw, chipH)
		cx += bw + chipGap
	}
	if len(chips) == 0 {
		return rs, 0
	}
	return rs, cy + chipH - y
}

// drawChips draws chips; chosen ones are filled. It returns the key of the
// chip clicked, or "" and false.
func (a *App) drawChips(id string, chips []chip, x, y, w float32, disabled bool) (string, bool) {
	c := a.ui
	rs, _ := a.layoutChips(chips, x, y, w)
	key, clicked := "", false
	for i, ch := range chips {
		kind := ui.Normal
		if ch.on {
			kind = ui.Primary
		}
		label := c.Ellipsize(ui.Bold, ui.SizeBody, ch.label, rs[i].Width-16)
		if c.Button(id+"/"+ch.key, rs[i], label, kind, disabled) {
			key, clicked = ch.key, true
		}
	}
	return key, clicked
}

// noCustomerKey is the chip key for "No customer"; real customer ids are never empty.
const noCustomerKey = "-"

// customerChips returns a chip per customer, after "No customer". on
// reports whether a customer id ("" for no customer) is chosen.
func (a *App) customerChips(on func(id string) bool) []chip {
	chips := []chip{{key: noCustomerKey, label: i18n.Trans("No customer"), on: on("")}}
	for _, cu := range a.customerList() {
		chips = append(chips, chip{key: cu.ID, label: cu.Name, on: on(cu.ID)})
	}
	return chips
}

// chipCustomer turns a chip key back into a customer id.
func chipCustomer(key string) string {
	if key == noCustomerKey {
		return ""
	}
	return key
}

// listItem is one line of the Computers list.
type listItem struct {
	kind   itemKind
	h      float32
	cu     string // customer id of the section ("" is no customer)
	ci     control.ComputerInfo
	n, on  int // computers and online computers, for headers
	filter bool
}

type itemKind int

const (
	itemComputer     itemKind = iota
	itemSection               // a customer's header
	itemOffline               // the Offline header of a section or the whole list
	itemNoneOnline            // "No computers online."
	itemEmptySection          // a customer without computers
	itemAddCustomer           // the Add customer field
)

const (
	sectionH  = 44
	offlineH  = 40
	addFieldH = 66
)

// moveH is the height of the Move choice below a computer row.
func (a *App) moveH(w float32) float32 {
	_, h := a.layoutChips(a.moveChips(control.ComputerInfo{}), 0, 0, w)
	return 26 + h + 12
}

// rowH returns the height of a computer row with its listing requests and,
// when open, its Move choice.
func (a *App) itemRowH(ci control.ComputerInfo, w float32) float32 {
	h := computerRowH(ci)
	if a.cust.moving == ci.Fingerprint {
		h += a.moveH(w - 48)
	}
	return h
}

// listItems lays out the Computers list for width w: online computers then
// a collapsible Offline part, per customer when the server has customers.
func (a *App) listItems(w float32) []listItem {
	var items []listItem
	row := func(ci control.ComputerInfo) {
		items = append(items, listItem{kind: itemComputer, h: a.itemRowH(ci, w), ci: ci, cu: ci.Customer})
	}
	online, offline := sortedComputers(a.presence)
	if a.cust.adding {
		items = append(items, listItem{kind: itemAddCustomer, h: addFieldH})
	}
	if !a.hasCustomers() {
		if len(online) == 0 {
			items = append(items, listItem{kind: itemNoneOnline, h: 44})
		}
		for _, ci := range online {
			row(ci)
		}
		if len(offline) > 0 {
			items = append(items, listItem{kind: itemOffline, h: offlineH, n: len(offline)})
			if a.offlineOpen {
				for _, ci := range offline {
					row(ci)
				}
			}
		}
		return items
	}

	filter := strings.ToLower(strings.TrimSpace(a.cust.filter.Edit.String()))
	matches := func(name string) bool { return strings.Contains(strings.ToLower(name), filter) }
	sections := a.customerList()
	sections = append(sections, control.CustomerInfo{ID: "", Name: i18n.Trans("No customer")})
	for _, cu := range sections {
		all := filter == "" || cu.ID != "" && matches(cu.Name)
		pick := func(cs []control.ComputerInfo) []control.ComputerInfo {
			var out []control.ComputerInfo
			for _, ci := range cs {
				if ci.Customer == cu.ID && (all || matches(ci.Name) || matches(ci.UserMachine)) {
					out = append(out, ci)
				}
			}
			return out
		}
		son, soff := pick(online), pick(offline)
		n := len(son) + len(soff)
		if n == 0 && (filter != "" && !all || cu.ID == "") {
			continue
		}
		items = append(items, listItem{kind: itemSection, h: sectionH, cu: cu.ID, n: n, on: len(son), filter: filter != ""})
		if a.cust.collapsed[cu.ID] && filter == "" {
			continue
		}
		if n == 0 {
			items = append(items, listItem{kind: itemEmptySection, h: 40, cu: cu.ID})
			continue
		}
		for _, ci := range son {
			row(ci)
		}
		if len(soff) > 0 {
			items = append(items, listItem{kind: itemOffline, h: offlineH, cu: cu.ID, n: len(soff), filter: filter != ""})
			if a.cust.offline[cu.ID] || filter != "" {
				for _, ci := range soff {
					row(ci)
				}
			}
		}
	}
	return items
}

// drawCustomerTop draws the filter field below the Computers title once the
// server has customers. It returns the y below it.
func (a *App) drawCustomerTop(r rl.Rectangle, top float32) float32 {
	if !a.hasCustomers() {
		return top
	}
	c := a.ui
	fw := min(r.Width-32, 360)
	c.TextField(filterID, rl.NewRectangle(r.X+16, top+10, fw, 34), &a.cust.filter)
	if a.cust.filter.Edit.String() != "" {
		clearL := i18n.Trans("Clear")
		if c.Button("computers/filter/clear", rl.NewRectangle(r.X+16+fw+6, top+12, a.btnW(clearL, 60), 30), clearL, ui.Flat, false) {
			a.cust.filter.Edit.SetText("")
		}
	}
	top += 54
	rl.DrawLineV(rl.NewVector2(r.X, top), rl.NewVector2(r.X+r.Width, top), ui.ColBorder)
	return top
}

// drawListItem draws one item of the Computers list in r.
func (a *App) drawListItem(it listItem, r rl.Rectangle) {
	c := a.ui
	indent := float32(0)
	if a.hasCustomers() {
		indent = 16
	}
	switch it.kind {
	case itemComputer:
		a.drawComputerRow(it.ci, rl.NewRectangle(r.X+indent, r.Y, r.Width-indent, r.Height))
	case itemSection:
		a.drawSection(it, r)
	case itemOffline:
		open := a.offlineOpen
		if a.hasCustomers() {
			open = a.cust.offline[it.cu] || it.filter
		}
		hover, clicked := c.Row("offline/header/"+it.cu, r, ui.ColPanel, false)
		arrow := "►"
		if open {
			arrow = "▼"
		}
		x := r.X + 16 + indent
		c.TextIn(ui.Regular, ui.SizeSmall, arrow, rl.NewRectangle(x, r.Y, 20, r.Height), ui.ColMuted)
		c.TextIn(ui.Bold, ui.SizeSmall, i18n.Trans("Offline ({0})", it.n), rl.NewRectangle(x+18, r.Y, r.Width, r.Height), ui.ColMuted)
		if hover && !it.filter {
			c.SetCursor(rl.MouseCursorPointingHand)
		}
		if clicked && !it.filter {
			if a.hasCustomers() {
				a.cust.offline[it.cu] = !a.cust.offline[it.cu]
			} else {
				a.offlineOpen = !a.offlineOpen
			}
		}
	case itemNoneOnline:
		c.Text(ui.Regular, ui.SizeSmall, i18n.Trans("No computers online."), r.X+16+indent, r.Y+14, ui.ColMuted)
	case itemEmptySection:
		c.Text(ui.Regular, ui.SizeSmall, i18n.Trans("No computers yet. Register one with Add computer, or move one here."), r.X+32+indent, r.Y+12, ui.ColMuted)
	case itemAddCustomer:
		a.drawAddCustomer(r)
	}
}

// drawSection draws a customer's header: an arrow, the name and how many of
// its computers are online. Clicking it opens or closes the section; Rename
// and Remove show on hover.
func (a *App) drawSection(it listItem, r rl.Rectangle) {
	c := a.ui
	s := &a.cust
	id := "section/" + it.cu
	editing := s.renaming == it.cu && it.cu != ""
	confirming := s.confirm == it.cu && it.cu != ""
	hover, clicked := c.Row(id, r, ui.ColBackground, false)
	rl.DrawLineV(rl.NewVector2(r.X, r.Y), rl.NewVector2(r.X+r.Width, r.Y), ui.ColBorder)
	x := r.X + 16
	if editing {
		a.drawCustomerRename(it.cu, rl.NewRectangle(x, r.Y+6, min(r.Width-32, 420), 30))
		return
	}
	arrow := "▼"
	if s.collapsed[it.cu] && !it.filter {
		arrow = "►"
	}
	c.TextIn(ui.Regular, ui.SizeSmall, arrow, rl.NewRectangle(x, r.Y, 20, r.Height), ui.ColMuted)
	x += 18

	renameL, removeL, cancelL := i18n.Trans("Rename"), i18n.Trans("Remove"), i18n.Trans("Cancel")
	wRename, wRemove := a.btnW(renameL, 66), a.btnW(removeL, 66)
	wConfirm, wCancel := a.btnW(removeL, 74), a.btnW(cancelL, 68)
	manage := it.cu != "" && !s.busy[it.cu]
	actionW := float32(0)
	switch {
	case confirming:
		actionW = wConfirm + 6 + wCancel + 2
	case hover && manage:
		actionW = wRename + 4 + wRemove + 4
	}
	name := a.customerName(it.cu)
	if it.cu == "" {
		name = i18n.Trans("No customer")
	}
	count := i18n.Trans("{0} of {1} online", it.on, it.n)
	if n, ok := a.notes["customer/"+it.cu]; ok {
		count = n.text
	}
	if confirming {
		count = i18n.Trans("Remove this customer? Its computers move to No customer.")
	}
	countCol := ui.ColMuted
	if confirming {
		countCol = ui.ColError
	} else if _, ok := a.notes["customer/"+it.cu]; ok {
		countCol = ui.ColError
	}
	avail := r.X + r.Width - x - actionW - 16
	nameW := min(c.Measure(ui.Bold, ui.SizeBody, name).X, avail*0.6)
	c.TextIn(ui.Bold, ui.SizeBody, c.Ellipsize(ui.Bold, ui.SizeBody, name, nameW), rl.NewRectangle(x, r.Y, nameW, r.Height), ui.ColText)
	cx := x + nameW + 12
	c.TextIn(ui.Regular, ui.SizeSmall, c.Ellipsize(ui.Regular, ui.SizeSmall, count, avail-nameW-12), rl.NewRectangle(cx, r.Y, avail-nameW-12, r.Height), countCol)

	ax := r.X + r.Width - actionW - 8
	ay := r.Y + (r.Height-30)/2
	action := false
	switch {
	case confirming:
		if c.Button(id+"/confirm", rl.NewRectangle(ax, ay, wConfirm, 30), removeL, ui.Danger, !a.online()) {
			a.removeCustomer(it.cu)
			action = true
		}
		if c.Button(id+"/cancel", rl.NewRectangle(ax+wConfirm+6, ay, wCancel, 30), cancelL, ui.Flat, false) {
			s.confirm = ""
			action = true
		}
	case hover && manage:
		if c.Button(id+"/rename", rl.NewRectangle(ax, ay, wRename, 30), renameL, ui.Flat, !a.online()) {
			a.startCustomerRename(it.cu)
			action = true
		}
		if c.Button(id+"/remove", rl.NewRectangle(ax+wRename+4, ay, wRemove, 30), removeL, ui.Flat, !a.online()) {
			s.confirm = it.cu
			action = true
		}
	}
	if hover && !it.filter {
		c.SetCursor(rl.MouseCursorPointingHand)
	}
	if clicked && !action && !it.filter {
		s.collapsed[it.cu] = !s.collapsed[it.cu]
	}
}

// drawAddCustomer draws the Add customer field with Add and Cancel. Enter
// adds, Esc cancels.
func (a *App) drawAddCustomer(r rl.Rectangle) {
	c := a.ui
	s := &a.cust
	rl.DrawRectangleRec(r, ui.ColBackground)
	x, y := r.X+16, r.Y+10
	addL, cancelL := i18n.Trans("Add"), i18n.Trans("Cancel")
	wAdd, wCancel := a.btnW(addL, 70), a.btnW(cancelL, 68)
	fw := min(r.Width-32-wAdd-6-wCancel-6, 420)
	res := c.TextField(addCustomerID, rl.NewRectangle(x, y, fw, 32), &s.addField)
	if res.Changed {
		s.addErr = ""
	}
	name := strings.TrimSpace(s.addField.Edit.String())
	ok := validCustomerName(name) && !s.addBusy && a.online()
	if c.Button("customers/add/ok", rl.NewRectangle(x+fw+6, y+1, wAdd, 30), addL, ui.Primary, !ok) || res.Submitted && ok {
		a.addCustomer(name)
	}
	if c.Button("customers/add/cancel", rl.NewRectangle(x+fw+6+wAdd+6, y+1, wCancel, 30), cancelL, ui.Flat, false) ||
		c.Focus == addCustomerID && rl.IsKeyPressed(rl.KeyEscape) {
		s.reset()
		a.ui.Focus = ""
		return
	}
	hint, col := i18n.Trans("A customer is a group of computers, for example one company you help."), ui.ColMuted
	if s.addErr != "" {
		hint, col = s.addErr, ui.ColError
	} else if name != "" && !validCustomerName(name) {
		hint, col = i18n.Trans("1 to {0} printable characters.", control.MaxCustomerName), ui.ColError
	}
	c.Text(ui.Regular, ui.SizeSmall, c.Ellipsize(ui.Regular, ui.SizeSmall, hint, r.Width-32), x, y+36, col)
}

// validCustomerName reports whether the server accepts s as a customer name.
func validCustomerName(s string) bool {
	return validComputerName(s) && len([]rune(strings.TrimSpace(s))) <= control.MaxCustomerName
}

// openAddCustomer shows the Add customer field at the top of the list.
func (a *App) openAddCustomer() {
	s := &a.cust
	s.adding, s.addErr = true, ""
	s.addField.Edit.SetText("")
	a.ui.Focus = addCustomerID
}

func (a *App) addCustomer(name string) {
	s := &a.cust
	if a.backend == nil || s.addBusy {
		return
	}
	s.addBusy = true
	a.run(a.backend.AddCustomer(name), func(err error) {
		s.addBusy = false
		if err != nil {
			s.addErr = i18n.Trans("Not added: {0}", err)
			return
		}
		s.reset()
		if a.ui.Focus == addCustomerID {
			a.ui.Focus = ""
		}
	})
}

func (a *App) startCustomerRename(id string) {
	s := &a.cust
	s.renaming, s.renameBad, s.confirm = id, false, ""
	s.renameField.Edit.SetText(a.customerName(id))
	s.renameField.Edit.SelectAll()
	a.ui.Focus = renameCustomerID
}

// drawCustomerRename draws the inline name field of a customer being
// renamed: Enter saves, Esc or a click elsewhere cancels.
func (a *App) drawCustomerRename(id string, r rl.Rectangle) {
	c := a.ui
	s := &a.cust
	res := c.TextField(renameCustomerID, r, &s.renameField)
	if res.Changed {
		s.renameBad = false
	}
	end := func() {
		s.renaming = ""
		if c.Focus == renameCustomerID {
			c.Focus = ""
		}
	}
	switch {
	case res.Submitted:
		name := strings.TrimSpace(s.renameField.Edit.String())
		if name == a.customerName(id) {
			end()
			return
		}
		if !validCustomerName(name) {
			s.renameBad = true
			return
		}
		end()
		a.renameCustomer(id, name)
	case rl.IsKeyPressed(rl.KeyEscape), c.Focus != renameCustomerID:
		end()
	}
	if s.renameBad {
		hint := i18n.Trans("1 to {0} printable characters.", control.MaxCustomerName)
		c.Text(ui.Regular, ui.SizeSmall, hint, r.X+r.Width+8, r.Y+8, ui.ColError)
	}
}

// customerNote shows an error on a customer's header for a few seconds.
func (a *App) customerNote(id, text string) {
	a.notes["customer/"+id] = note{text: text, until: time.Now().Add(6 * time.Second)}
}

func (a *App) renameCustomer(id, name string) {
	if a.backend == nil {
		return
	}
	a.cust.busy[id] = true
	a.run(a.backend.RenameCustomer(id, name), func(err error) {
		delete(a.cust.busy, id)
		if err != nil {
			a.customerNote(id, i18n.Trans("Could not rename: {0}", err))
		}
	})
}

func (a *App) removeCustomer(id string) {
	a.cust.confirm = ""
	if a.backend == nil {
		return
	}
	a.cust.busy[id] = true
	a.run(a.backend.RemoveCustomer(id), func(err error) {
		delete(a.cust.busy, id)
		if err != nil {
			a.customerNote(id, i18n.Trans("Could not remove: {0}", err))
		}
	})
}

// moveChips are the customers a computer can move to: all of them and No
// customer.
func (a *App) moveChips(ci control.ComputerInfo) []chip {
	chips := a.customerChips(func(id string) bool { return id == ci.Customer })
	return append(chips, chip{key: "cancel", label: i18n.Trans("Cancel")})
}

// drawMove draws the Move choice of a computer below its row, from y in
// width w.
func (a *App) drawMove(ci control.ComputerInfo, x, y, w float32) bool {
	c := a.ui
	c.Text(ui.Regular, ui.SizeSmall, i18n.Trans("Move {0} to:", ci.Name), x, y+4, ui.ColMuted)
	key, ok := a.drawChips("move/"+ci.Fingerprint, a.moveChips(ci), x, y+26, w, a.cust.moveBusy[ci.Fingerprint] || !a.online())
	if !ok {
		return false
	}
	a.cust.moving = ""
	if key != "cancel" && chipCustomer(key) != ci.Customer {
		a.moveComputer(ci.Fingerprint, chipCustomer(key))
	}
	return true
}

func (a *App) moveComputer(fp, customer string) {
	if a.backend == nil {
		return
	}
	a.cust.moveBusy[fp] = true
	delete(a.notes, fp)
	a.run(a.backend.MoveComputer(fp, customer), func(err error) {
		delete(a.cust.moveBusy, fp)
		if err != nil {
			a.notes[fp] = note{text: i18n.Trans("Could not move: {0}", err), until: time.Now().Add(6 * time.Second)}
		}
	})
}
