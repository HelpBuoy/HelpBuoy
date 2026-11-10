package views

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/helpbuoy/helpbuoy/desk/config"
	"github.com/helpbuoy/helpbuoy/desk/serverconn"
	"github.com/helpbuoy/helpbuoy/desk/ui"
	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/proto/i18n"
)

// drawComputersPage draws the Computers tab: the computers, online first,
// offline collapsed. The server status is on the Server tab.
func (a *App) drawComputersPage(r rl.Rectangle) {
	c := a.ui
	rl.DrawRectangleRec(r, ui.ColPanel)
	c.Text(ui.Bold, ui.SizeHeading, i18n.Trans("Computers"), r.X+16, r.Y+14, ui.ColText)
	a.addButton("computers/add", i18n.Trans("Add computer"), tabRegister, r)
	top := r.Y + 56
	rl.DrawLineV(rl.NewVector2(r.X, top), rl.NewVector2(r.X+r.Width, top), ui.ColBorder)
	if a.notSetUp() {
		top = a.drawNotSetUp(r.X+16, top+12, r.Width-32) + 12
	}
	a.drawComputerList(rl.NewRectangle(r.X, top, r.Width, r.Y+r.Height-top))
}

// addButton draws the Add computer or Add techie button at the top right of
// page r; it opens the Register tab k.
func (a *App) addButton(id, label string, k tabKind, r rl.Rectangle) {
	bw := a.ui.ButtonWidth(label)
	if a.ui.Button(id, rl.NewRectangle(r.X+r.Width-16-bw, r.Y+11, bw, 34), label, ui.Normal, a.backend == nil) {
		a.openRegister(k)
	}
}

// statusText describes the server connection for the UI.
func (a *App) statusText() (text, detail string, col rl.Color) {
	if a.demo != nil {
		return i18n.Trans("Online"), i18n.Trans("demo mode, no server"), ui.ColOK
	}
	if a.key == nil {
		return i18n.Trans("No key"), "", ui.ColError
	}
	if a.conn == nil {
		return i18n.Trans("Not connected"), "", ui.ColMuted
	}
	addr := a.addr.String()
	if config.IsLocal(a.addr) {
		addr = i18n.Trans("{0} (on this computer)", addr)
	}
	errText := ""
	if a.status.Err != nil {
		errText = a.status.Err.Error()
	}
	switch a.status.Status {
	case serverconn.StatusOnline:
		return i18n.Trans("Online"), addr, ui.ColOK
	case serverconn.StatusAuthFailed:
		return i18n.Trans("This key is not a techie on the server anymore"), i18n.Trans("Disconnect and connect again to see the key to send to the server admin."), ui.ColError
	case serverconn.StatusUnreachable:
		return i18n.Trans("Server unreachable"), errText, ui.ColError
	case serverconn.StatusIdentityChanged:
		return i18n.Trans("Server identity changed"), i18n.Trans("The server's key differs from the pinned one. Disconnect and connect again to decide."), ui.ColError
	case serverconn.StatusReplaced:
		return i18n.Trans("Connected from somewhere else"), i18n.Trans("Another desk app with this key connected to the server, so this one was disconnected. Reconnect takes the connection back."), ui.ColError
	}
	if errText != "" {
		return i18n.Trans("Reconnecting to {0}", addr), errText, ui.ColWarn
	}
	return i18n.Trans("Connecting to {0}", addr), "", ui.ColWarn
}

// sortedComputers splits the presence into online and offline computers, each
// sorted by name.
func sortedComputers(p *control.Presence) (online, offline []control.ComputerInfo) {
	if p == nil {
		return nil, nil
	}
	for _, c := range p.Computers {
		if c.Online {
			online = append(online, c)
		} else {
			offline = append(offline, c)
		}
	}
	sort.SliceStable(online, func(i, j int) bool {
		return strings.ToLower(online[i].Name) < strings.ToLower(online[j].Name)
	})
	sort.SliceStable(offline, func(i, j int) bool {
		return strings.ToLower(offline[i].Name) < strings.ToLower(offline[j].Name)
	})
	return online, offline
}

const rowH = 52

// listingH is the height of one open listing request below a computer row.
const listingH = 46

// computerRowH is the height of a computer row with its listing requests.
func computerRowH(ci control.ComputerInfo) float32 {
	return rowH + float32(len(ci.Listings))*listingH
}

func (a *App) drawComputerList(r rl.Rectangle) {
	c := a.ui
	online, offline := sortedComputers(a.presence)
	if a.presence != nil && len(online)+len(offline) == 0 {
		c.Paragraph(ui.Regular, ui.SizeSmall, i18n.Trans("No registered computers yet. Use Add computer to add one."), 16, r.Y+16, r.Width-32, ui.ColMuted)
		return
	}
	if a.presence == nil {
		return
	}
	contentH := float32(40)
	for _, ci := range online {
		contentH += computerRowH(ci)
	}
	if a.offlineOpen {
		for _, ci := range offline {
			contentH += computerRowH(ci)
		}
	}
	off := c.BeginScroll("computers", r, contentH)
	y := r.Y + off
	if len(online) == 0 {
		c.Text(ui.Regular, ui.SizeSmall, i18n.Trans("No computers online."), 16, y+14, ui.ColMuted)
		y += 44
		contentH += 44
	}
	for _, ci := range online {
		h := computerRowH(ci)
		a.drawComputerRow(ci, rl.NewRectangle(0, y, r.Width, h))
		y += h
	}
	if len(offline) > 0 {
		hr := rl.NewRectangle(0, y, r.Width, 40)
		hover, clicked := c.Row("offline/header", hr, ui.ColPanel, false)
		arrow := "►"
		if a.offlineOpen {
			arrow = "▼"
		}
		c.TextIn(ui.Regular, ui.SizeSmall, arrow, rl.NewRectangle(16, y, 20, 40), ui.ColMuted)
		c.TextIn(ui.Bold, ui.SizeSmall, i18n.Trans("Offline ({0})", len(offline)), rl.NewRectangle(34, y, r.Width, 40), ui.ColMuted)
		if hover {
			c.SetCursor(rl.MouseCursorPointingHand)
		}
		if clicked {
			a.offlineOpen = !a.offlineOpen
		}
		y += 40
		if a.offlineOpen {
			for _, ci := range offline {
				h := computerRowH(ci)
				a.drawComputerRow(ci, rl.NewRectangle(0, y, r.Width, h))
				y += h
			}
		}
	}
	c.EndScroll()
}

// drawComputerRow draws one computer: name, a status line, a Connect button for
// online computers nobody is helping, and Rename and Remove on hover. Its open
// listing requests follow below, each with Allow and Refuse.
func (a *App) drawComputerRow(ci control.ComputerInfo, r rl.Rectangle) {
	c := a.ui
	id := "computer/" + ci.Fingerprint
	tabIdx := a.sessionFor(ci.Fingerprint)
	wait := a.waiting[ci.Fingerprint]
	canConnect := ci.Online && ci.Request == nil && ci.InSessionWith == "" && tabIdx < 0 && wait == nil
	canCancel := wait != nil && tabIdx < 0
	bg := ui.ColPanel
	editing := a.rename.fp == ci.Fingerprint
	selected := tabIdx >= 0 && tabIdx == a.active
	hover, clicked := c.Row(id, r, bg, selected)
	rl.DrawLineV(rl.NewVector2(r.X+16, r.Y+r.Height), rl.NewVector2(r.X+r.Width, r.Y+r.Height), ui.ColHover)

	x := r.X + 16
	nameCol := ui.ColText
	if ci.Online {
		rl.DrawCircleV(rl.NewVector2(x+4, r.Y+17), 4, ui.ColOK)
	} else {
		nameCol = ui.ColMuted
		rl.DrawCircleLines(int32(x+4), int32(r.Y+17), 4, ui.ColBorder)
	}
	x += 16
	listingClicked := a.drawListings(ci, x, r.Y+rowH-6, r.X+r.Width-x-8)

	// Right side action area. Buttons grow with longer (translated) labels.
	removeL, cancelL, renameL := i18n.Trans("Remove"), i18n.Trans("Cancel"), i18n.Trans("Rename")
	connectL, askingL := i18n.Trans("Connect"), i18n.Trans("Asking…")
	wConfirm, wCancel := a.btnW(removeL, 74), a.btnW(cancelL, 68)
	wRename, wRemove := a.btnW(renameL, 66), a.btnW(removeL, 66)
	wConnect := max(a.btnW(connectL, 86), a.btnW(askingL, 86), a.btnW(cancelL, 86))
	actionW := float32(0)
	confirming := a.confirmRemove == ci.Fingerprint
	canRename := a.online() && a.rename.busy[ci.Fingerprint] == ""
	switch {
	case editing:
	case confirming:
		actionW = wConfirm + 6 + wCancel + 2
	case hover:
		actionW = wRename + 4
		if tabIdx < 0 {
			actionW += wRemove + 4
		}
		if canConnect || canCancel {
			actionW += wConnect + 6
		}
	case canConnect || canCancel:
		actionW = wConnect + 6
	}
	if editing {
		a.drawRenameField(ci, rl.NewRectangle(x, r.Y+5, r.X+r.Width-x-12, 28))
		return
	}
	nameW := r.Width - (x - r.X) - actionW - 12
	font := ui.Regular
	c.Text(font, ui.SizeBody, c.Ellipsize(font, ui.SizeBody, ci.Name, nameW), x, r.Y+8, nameCol)

	sub, subCol := a.rowSubline(ci, tabIdx)
	if n, ok := a.notes[ci.Fingerprint]; ok {
		sub, subCol = n.text, ui.ColError
		if hover {
			c.Tooltip(n.text)
		}
	}
	if n := a.rename.busy[ci.Fingerprint]; n != "" {
		sub, subCol = i18n.Trans("Renaming to {0}…", n), ui.ColMuted
	}
	if confirming {
		sub, subCol = i18n.Trans("Remove this computer?"), ui.ColError
	}
	c.Text(ui.Regular, ui.SizeSmall, c.Ellipsize(ui.Regular, ui.SizeSmall, sub, nameW), x, r.Y+30, subCol)

	ax := r.X + r.Width - actionW - 8
	ay := r.Y + (rowH-30)/2
	actionClicked := listingClicked
	switch {
	case confirming:
		if c.Button(id+"/confirm", rl.NewRectangle(ax, ay, wConfirm, 30), removeL, ui.Danger, false) {
			a.unregister(ci)
			actionClicked = true
		}
		if c.Button(id+"/cancel", rl.NewRectangle(ax+wConfirm+6, ay, wCancel, 30), cancelL, ui.Flat, false) {
			a.confirmRemove = ""
			actionClicked = true
		}
	case hover || canConnect || canCancel:
		bx := ax
		if hover {
			if c.Button(id+"/rename", rl.NewRectangle(bx, ay, wRename, 30), renameL, ui.Flat, !canRename) {
				a.startRename(ci)
				actionClicked = true
			}
			bx += wRename + 4
			if tabIdx < 0 {
				if c.Button(id+"/remove", rl.NewRectangle(bx, ay, wRemove, 30), removeL, ui.Flat, !a.online()) {
					a.confirmRemove = ci.Fingerprint
					actionClicked = true
				}
				bx += wRemove + 4
			}
		}
		if canConnect {
			busy := a.opening[ci.Fingerprint]
			label := connectL
			if busy {
				label = askingL
			}
			if c.Button(id+"/connect", rl.NewRectangle(bx, ay, wConnect, 30), label, ui.Primary, busy || !a.online()) {
				a.connectComputer(ci)
				actionClicked = true
			}
		}
		if canCancel {
			if c.Button(id+"/cancelconnect", rl.NewRectangle(bx, ay, wConnect, 30), cancelL, ui.Normal, false) {
				a.cancelConnect(ci.Fingerprint)
				actionClicked = true
			}
		}
	}
	onMain := c.Hover(rl.NewRectangle(r.X, r.Y, r.Width, rowH)) // not on the listing requests
	if hover && !actionClicked && onMain {
		tip := ci.Fingerprint
		if ci.UserMachine != "" {
			tip = ci.UserMachine + "  ·  " + tip
		}
		c.Tooltip(tip)
	}
	if clicked && !actionClicked && onMain {
		// A double click on the row renames, like a file manager.
		if a.rename.lastClick == ci.Fingerprint && c.Now.Sub(a.rename.lastAt) < 400*time.Millisecond && canRename {
			a.startRename(ci)
			a.rename.lastClick = ""
		} else {
			a.rename.lastClick, a.rename.lastAt = ci.Fingerprint, c.Now
		}
		if tabIdx >= 0 {
			a.activate(tabIdx)
		}
	}
}

// maxComputerName is the longest computer name the server accepts, in characters.
const maxComputerName = 128

// validComputerName reports whether the server accepts s as a computer name:
// 1 to 128 printable characters after trimming spaces at both ends. Spaces
// inside are fine ("Anna laptop").
func validComputerName(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxComputerName {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// renameFieldID is the ui id of the inline rename field.
const renameFieldID = "computers/rename"

// renameState is the inline rename of a computer row.
type renameState struct {
	fp    string // computer being renamed, or ""
	field ui.Field
	bad   bool // the typed name was rejected

	busy map[string]string // new names waiting for the server, by fingerprint

	blur      bool // drop the field's focus at the start of the next frame
	lastClick string
	lastAt    time.Time
}

// beginFrame drops the focus of a closed rename field. It is kept until the
// next frame so the Enter or Esc that closed the field does not also reach a
// session in the same frame.
func (r *renameState) beginFrame(c *ui.Context) {
	if r.blur {
		if c.Focus == renameFieldID {
			c.Focus = ""
		}
		r.blur = false
	}
}

func (a *App) startRename(ci control.ComputerInfo) {
	r := &a.rename
	r.fp, r.bad = ci.Fingerprint, false
	r.field.Edit.Max = maxComputerName
	r.field.Edit.SetText(ci.Name)
	r.field.Edit.SelectAll()
	a.confirmRemove = ""
	a.ui.Focus = renameFieldID
}

func (a *App) endRename() {
	a.rename.fp = ""
	a.rename.blur = true
}

// drawRenameField draws the inline name field of a row being renamed:
// Enter saves, Esc or a click elsewhere cancels.
func (a *App) drawRenameField(ci control.ComputerInfo, r rl.Rectangle) {
	c := a.ui
	res := c.TextField(renameFieldID, r, &a.rename.field)
	hint, col := i18n.Trans("Enter to save, Esc to cancel"), ui.ColMuted
	if a.rename.bad {
		hint, col = i18n.Trans("1 to {0} printable characters.", maxComputerName), ui.ColError
	}
	c.Text(ui.Regular, ui.SizeSmall, c.Ellipsize(ui.Regular, ui.SizeSmall, hint, r.Width), r.X, r.Y+r.Height+3, col)
	if res.Changed {
		a.rename.bad = false
	}
	switch {
	case res.Submitted:
		a.commitRename(ci)
	case rl.IsKeyPressed(rl.KeyEscape), c.Focus != renameFieldID:
		a.endRename()
	}
}

// commitRename sends the typed name to the server. An unchanged name just
// closes the field; an invalid one keeps it open with a hint.
func (a *App) commitRename(ci control.ComputerInfo) {
	name := strings.TrimSpace(a.rename.field.Edit.String())
	if name == ci.Name {
		a.endRename()
		return
	}
	if !validComputerName(name) {
		a.rename.bad = true
		return
	}
	a.endRename()
	if a.backend == nil {
		return
	}
	fp := ci.Fingerprint
	if a.rename.busy == nil {
		a.rename.busy = map[string]string{}
	}
	a.rename.busy[fp] = name
	a.run(a.backend.RenameComputer(fp, name), func(err error) {
		delete(a.rename.busy, fp)
		if err != nil {
			a.notes[fp] = note{text: i18n.Trans("Could not rename: {0}", err), until: time.Now().Add(6 * time.Second)}
		}
	})
}

// rowSubline is the small second line of a computer row.
func (a *App) rowSubline(ci control.ComputerInfo, tabIdx int) (string, rl.Color) {
	switch {
	case tabIdx >= 0:
		return i18n.Trans("In session"), ui.ColAccent
	case a.waiting[ci.Fingerprint] != nil:
		return i18n.Trans("Waiting for the user to allow the session…"), ui.ColWarn
	case ci.InSessionWith != "" && ci.InSessionWith != a.me:
		return i18n.Trans("In session with {0}", ci.InSessionWith), ui.ColMuted
	case ci.Online && ci.Request != nil && ci.Request.TakenBy != a.me:
		return i18n.Trans("{0} is connecting", ci.Request.TakenBy), ui.ColMuted
	case ci.Online:
		if ci.UserMachine != "" && !strings.EqualFold(ci.UserMachine, ci.Name) {
			return ci.UserMachine, ui.ColMuted
		}
		return i18n.Trans("Online"), ui.ColMuted
	case !ci.LastSeen.IsZero():
		return i18n.Trans("Last seen {0}", lastSeen(ci.LastSeen, time.Now())), ui.ColMuted
	}
	return i18n.Trans("Never connected"), ui.ColMuted
}

// lastSeen formats a time relative to now: a clock time today, otherwise a date.
func lastSeen(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	y1, m1, d1 := t.Date()
	y2, m2, d2 := now.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return i18n.Trans("today {0}", t.Format("15:04"))
	case y1 == y2:
		return fmt.Sprintf("%d %s", t.Day(), shortMonth(t.Month()))
	}
	return fmt.Sprintf("%d %s %d", t.Day(), shortMonth(t.Month()), t.Year())
}

// shortMonth is the abbreviated month name in the user's language.
func shortMonth(m time.Month) string {
	names := [...]string{
		i18n.Trans("Jan"), i18n.Trans("Feb"), i18n.Trans("Mar"), i18n.Trans("Apr"),
		i18n.Trans("May"), i18n.Trans("Jun"), i18n.Trans("Jul"), i18n.Trans("Aug"),
		i18n.Trans("Sep"), i18n.Trans("Oct"), i18n.Trans("Nov"), i18n.Trans("Dec"),
	}
	if m < time.January || m > time.December {
		return ""
	}
	return names[m-1]
}

// connectComputer asks a computer's user to allow a session with this techie.
func (a *App) connectComputer(ci control.ComputerInfo) {
	if a.backend == nil {
		return
	}
	reqID, fp := control.NewRequestID(), ci.Fingerprint
	a.opened[reqID] = time.Now()
	a.opening[fp] = true
	delete(a.notes, fp)
	a.run(a.backend.Connect(fp, reqID), func(err error) {
		delete(a.opening, fp)
		if err != nil {
			delete(a.opened, reqID)
			a.notes[fp] = note{text: i18n.Trans("Could not connect: {0}", err), until: time.Now().Add(6 * time.Second)}
			return
		}
		if _, ok := a.opened[reqID]; ok {
			a.waiting[fp] = &waitInfo{id: reqID}
		}
	})
}

// cancelConnect stops waiting for the user to allow a Connect. The server
// closes the question on the computer; a session channel that still comes
// is refused because the request is no longer in a.opened.
func (a *App) cancelConnect(fp string) {
	w := a.waiting[fp]
	if w == nil {
		return
	}
	delete(a.waiting, fp)
	delete(a.opened, w.id)
	if a.backend != nil {
		a.backend.CancelRequest(w.id)
	}
}

// unregister removes a computer from the server.
func (a *App) unregister(ci control.ComputerInfo) {
	a.confirmRemove = ""
	if a.backend == nil {
		return
	}
	fp := ci.Fingerprint
	a.run(a.backend.UnregisterComputer(fp), func(err error) {
		if err != nil {
			a.notes[fp] = note{text: i18n.Trans("Could not remove: {0}", err), until: time.Now().Add(6 * time.Second)}
		}
	})
}

// listingKey identifies one listing request of a computer, for a.deciding.
func listingKey(fp string, l control.ListingRequest) string {
	return fp + " " + l.Address + " " + l.Fingerprint
}

// drawListings draws the open listing requests of a computer, from y down,
// in width w: the other server it asks to use, with Allow and Refuse. It
// reports whether a button was clicked.
func (a *App) drawListings(ci control.ComputerInfo, x, y, w float32) bool {
	c := a.ui
	allowL, refuseL := i18n.Trans("Allow"), i18n.Trans("Refuse")
	wAllow, wRefuse := a.btnW(allowL, 70), a.btnW(refuseL, 70)
	clicked := false
	for i, l := range ci.Listings {
		ly := y + float32(i)*listingH
		key := listingKey(ci.Fingerprint, l)
		busy := a.deciding[key]
		tw := w - wAllow - 6 - wRefuse - 12
		text := i18n.Trans("Asks to also use the server {0}", l.Address)
		c.Text(ui.Regular, ui.SizeSmall, c.Ellipsize(ui.Regular, ui.SizeSmall, text, tw), x, ly+4, ui.ColWarn)
		c.Text(ui.Mono, ui.SizeSmall, c.Ellipsize(ui.Mono, ui.SizeSmall, l.Fingerprint, tw), x, ly+23, ui.ColMuted)
		bx, by := x+w-wAllow-6-wRefuse, ly+(listingH-30)/2
		id := "listing/" + key
		if c.Button(id+"/allow", rl.NewRectangle(bx, by, wAllow, 30), allowL, ui.Primary, busy || !a.online()) {
			a.decideListing(ci.Fingerprint, l, true)
			clicked = true
		}
		if c.Button(id+"/refuse", rl.NewRectangle(bx+wAllow+6, by, wRefuse, 30), refuseL, ui.Normal, busy || !a.online()) {
			a.decideListing(ci.Fingerprint, l, false)
			clicked = true
		}
	}
	return clicked
}

// decideListing allows or refuses a computer's listing request.
func (a *App) decideListing(fp string, l control.ListingRequest, allow bool) {
	if a.backend == nil {
		return
	}
	key := listingKey(fp, l)
	if a.deciding == nil {
		a.deciding = map[string]bool{}
	}
	a.deciding[key] = true
	delete(a.notes, fp)
	a.run(a.backend.DecideListing(fp, l, allow), func(err error) {
		delete(a.deciding, key)
		if err == nil {
			return
		}
		text := i18n.Trans("Could not allow: {0}", err)
		if !allow {
			text = i18n.Trans("Could not refuse: {0}", err)
		}
		a.notes[fp] = note{text: text, until: time.Now().Add(6 * time.Second)}
	})
}
