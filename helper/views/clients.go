package views

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/mevdschee/help-me-win/helper/relayconn"
	"github.com/mevdschee/help-me-win/helper/ui"
	"github.com/mevdschee/help-me-win/proto/control"
)

// drawSidebar draws the left column: relay status, the clients (online
// first, offline collapsed) and the buttons for the Register and Settings
// pages.
func (a *App) drawSidebar() {
	c := a.ui
	_, h := ui.WindowSize()
	rl.DrawRectangleRec(rl.NewRectangle(0, 0, sidebarW, h), ui.ColPanel)
	defer rl.DrawRectangleRec(rl.NewRectangle(sidebarW, 0, 1, h), ui.ColBorder)

	c.Text(ui.Bold, ui.SizeHeading, "Clients", 16, 14, ui.ColText)
	y := a.drawStatus(16, 44, sidebarW-32)

	list := rl.NewRectangle(0, y+8, sidebarW, h-(y+8)-64)
	rl.DrawLineV(rl.NewVector2(0, list.Y), rl.NewVector2(sidebarW, list.Y), ui.ColBorder)
	a.drawClientList(list)

	by := h - 52
	rl.DrawLineV(rl.NewVector2(0, by-12), rl.NewVector2(sidebarW, by-12), ui.ColBorder)
	bw := float32(sidebarW-32-8) / 2
	if c.Button("side/register", rl.NewRectangle(16, by, bw, 36), "Register client", ui.Normal, false) {
		a.openPage(tabRegister)
	}
	if c.Button("side/settings", rl.NewRectangle(16+bw+8, by, bw, 36), "Settings", ui.Normal, false) {
		a.openPage(tabSettings)
	}
}

// statusText describes the relay connection for the UI.
func (a *App) statusText() (text, detail string, col rl.Color) {
	if a.demo != nil {
		return "Online", "demo mode, no relay", ui.ColOK
	}
	if a.key == nil {
		return "No key", "", ui.ColError
	}
	if a.conn == nil {
		return "No relay set up", "Enter the relay address in Settings.", ui.ColMuted
	}
	addr := a.cfg.RelayAddr
	if a.cfg.LocalRelay.Enabled {
		addr = a.cfg.ConnectAddr()
	}
	errText := ""
	if a.status.Err != nil {
		errText = a.status.Err.Error()
	}
	switch a.status.Status {
	case relayconn.StatusOnline:
		return "Online", addr, ui.ColOK
	case relayconn.StatusAuthFailed:
		return "This key is not a helper on the relay", "Send your public key (Settings) to the relay admin.", ui.ColError
	case relayconn.StatusUnreachable:
		return "Relay unreachable", errText, ui.ColError
	case relayconn.StatusIdentityChanged:
		return "Relay identity changed", "The relay's key differs from the pinned one. See Settings.", ui.ColError
	}
	if errText != "" {
		return "Reconnecting to " + addr, errText, ui.ColWarn
	}
	return "Connecting to " + addr, "", ui.ColWarn
}

// drawStatus draws the status line and returns the y below it.
func (a *App) drawStatus(x, y, w float32) float32 {
	c := a.ui
	text, detail, col := a.statusText()
	rl.DrawCircleV(rl.NewVector2(x+5, y+9), 4.5, col)
	c.Text(ui.Regular, ui.SizeSmall, c.Ellipsize(ui.Regular, ui.SizeSmall, text, w-16), x+16, y, ui.ColText)
	y += 20
	if detail != "" {
		for i, l := range c.Wrap(ui.Regular, ui.SizeSmall, detail, w-16) {
			if i == 2 {
				break
			}
			c.Text(ui.Regular, ui.SizeSmall, l, x+16, y, ui.ColMuted)
			y += 18
		}
	}
	return y
}

// sortedClients splits the presence into online and offline clients, each
// sorted by name, with open requests first among the online ones.
func sortedClients(p *control.Presence) (online, offline []control.ClientInfo) {
	if p == nil {
		return nil, nil
	}
	for _, c := range p.Clients {
		if c.Online {
			online = append(online, c)
		} else {
			offline = append(offline, c)
		}
	}
	isOpen := func(c control.ClientInfo) bool { return c.Request != nil && c.Request.State == control.RequestOpen }
	sort.SliceStable(online, func(i, j int) bool {
		if isOpen(online[i]) != isOpen(online[j]) {
			return isOpen(online[i])
		}
		return strings.ToLower(online[i].Name) < strings.ToLower(online[j].Name)
	})
	sort.SliceStable(offline, func(i, j int) bool {
		return strings.ToLower(offline[i].Name) < strings.ToLower(offline[j].Name)
	})
	return online, offline
}

const (
	rowH        = 52
	requestRowH = 64
)

func (a *App) drawClientList(r rl.Rectangle) {
	c := a.ui
	online, offline := sortedClients(a.presence)
	if a.presence != nil && len(online)+len(offline) == 0 {
		c.Paragraph(ui.Regular, ui.SizeSmall, "No registered clients yet. Use Register client below to add one.", 16, r.Y+16, r.Width-32, ui.ColMuted)
		return
	}
	if a.presence == nil {
		return
	}
	contentH := float32(0)
	for _, ci := range online {
		contentH += a.rowHeight(ci)
	}
	contentH += 40
	if a.offlineOpen {
		contentH += float32(len(offline)) * rowH
	}
	off := c.BeginScroll("clients", r, contentH)
	y := r.Y + off
	if len(online) == 0 {
		c.Text(ui.Regular, ui.SizeSmall, "No clients online.", 16, y+14, ui.ColMuted)
		y += 44
		contentH += 44
	}
	for _, ci := range online {
		h := a.rowHeight(ci)
		a.drawClientRow(ci, rl.NewRectangle(0, y, r.Width, h))
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
		c.TextIn(ui.Bold, ui.SizeSmall, fmt.Sprintf("Offline (%d)", len(offline)), rl.NewRectangle(34, y, r.Width, 40), ui.ColMuted)
		if hover {
			c.SetCursor(rl.MouseCursorPointingHand)
		}
		if clicked {
			a.offlineOpen = !a.offlineOpen
		}
		y += 40
		if a.offlineOpen {
			for _, ci := range offline {
				a.drawClientRow(ci, rl.NewRectangle(0, y, r.Width, rowH))
				y += rowH
			}
		}
	}
	c.EndScroll()
}

func (a *App) rowHeight(ci control.ClientInfo) float32 {
	if ci.Request != nil && ci.Request.State == control.RequestOpen {
		return requestRowH
	}
	return rowH
}

// drawClientRow draws one client: name, a status line, and depending on the
// state an Open session button or a Remove action.
func (a *App) drawClientRow(ci control.ClientInfo, r rl.Rectangle) {
	c := a.ui
	id := "client/" + ci.Fingerprint
	open := ci.Online && ci.Request != nil && ci.Request.State == control.RequestOpen
	bg := ui.ColPanel
	if open {
		bg = ui.ColRequest
	}
	tabIdx := a.sessionFor(ci.Fingerprint)
	editing := a.rename.fp == ci.Fingerprint
	selected := tabIdx >= 0 && tabIdx == a.active
	hover, clicked := c.Row(id, r, bg, selected)
	if open {
		rl.DrawRectangleRec(rl.NewRectangle(r.X, r.Y, 4, r.Height), ui.ColRequestBar)
	}
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

	// Right side action area.
	actionW := float32(0)
	confirming := a.confirmRemove == ci.Fingerprint
	canRename := a.online() && a.rename.busy[ci.Fingerprint] == ""
	switch {
	case editing:
	case confirming:
		actionW = 150
	case open:
		actionW = 118
	case hover && tabIdx < 0:
		actionW = 140
	case hover:
		actionW = 70
	}
	if editing {
		a.drawRenameField(ci, rl.NewRectangle(x, r.Y+5, r.X+r.Width-x-12, 28))
		return
	}
	nameW := r.Width - (x - r.X) - actionW - 12
	font := ui.Regular
	if open {
		font = ui.Bold
	}
	c.Text(font, ui.SizeBody, c.Ellipsize(font, ui.SizeBody, ci.Name, nameW), x, r.Y+8, nameCol)

	sub, subCol := a.rowSubline(ci, tabIdx)
	if n, ok := a.notes[ci.Fingerprint]; ok {
		sub, subCol = n.text, ui.ColError
		if hover {
			c.Tooltip(n.text)
		}
	}
	if n := a.rename.busy[ci.Fingerprint]; n != "" {
		sub, subCol = "Renaming to "+n+"…", ui.ColMuted
	}
	if confirming {
		sub, subCol = "Remove this client?", ui.ColError
	}
	c.Text(ui.Regular, ui.SizeSmall, c.Ellipsize(ui.Regular, ui.SizeSmall, sub, nameW), x, r.Y+30, subCol)

	ax := r.X + r.Width - actionW - 8
	ay := r.Y + (r.Height-30)/2
	actionClicked := false
	switch {
	case confirming:
		if c.Button(id+"/confirm", rl.NewRectangle(ax, ay, 74, 30), "Remove", ui.Danger, false) {
			a.unregister(ci)
			actionClicked = true
		}
		if c.Button(id+"/cancel", rl.NewRectangle(ax+80, ay, 68, 30), "Cancel", ui.Flat, false) {
			a.confirmRemove = ""
			actionClicked = true
		}
	case open:
		busy := a.opening[ci.Fingerprint]
		label := "Open session"
		if busy {
			label = "Opening…"
		}
		if c.Button(id+"/open", rl.NewRectangle(ax, ay, 112, 30), label, ui.Primary, busy || !a.online()) {
			a.openRequest(ci)
			actionClicked = true
		}
	case hover:
		if c.Button(id+"/rename", rl.NewRectangle(ax, ay, 66, 30), "Rename", ui.Flat, !canRename) {
			a.startRename(ci)
			actionClicked = true
		}
		if tabIdx < 0 && c.Button(id+"/remove", rl.NewRectangle(ax+70, ay, 66, 30), "Remove", ui.Flat, !a.online()) {
			a.confirmRemove = ci.Fingerprint
			actionClicked = true
		}
	}
	if hover && !actionClicked {
		tip := ci.Fingerprint
		if ci.UserMachine != "" {
			tip = ci.UserMachine + "  ·  " + tip
		}
		c.Tooltip(tip)
	}
	if clicked && !actionClicked {
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

// maxClientName is the longest client name the relay accepts, in characters.
const maxClientName = 128

// validClientName reports whether the relay accepts s as a client name:
// 1 to 128 printable characters after trimming spaces at both ends. Spaces
// inside are fine ("Anna laptop").
func validClientName(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxClientName {
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
const renameFieldID = "clients/rename"

// renameState is the inline rename of a client row.
type renameState struct {
	fp    string // client being renamed, or ""
	field ui.Field
	bad   bool // the typed name was rejected

	busy map[string]string // new names waiting for the relay, by fingerprint

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

func (a *App) startRename(ci control.ClientInfo) {
	r := &a.rename
	r.fp, r.bad = ci.Fingerprint, false
	r.field.Edit.Max = maxClientName
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
func (a *App) drawRenameField(ci control.ClientInfo, r rl.Rectangle) {
	c := a.ui
	res := c.TextField(renameFieldID, r, &a.rename.field)
	hint, col := "Enter to save, Esc to cancel", ui.ColMuted
	if a.rename.bad {
		hint, col = fmt.Sprintf("1 to %d printable characters", maxClientName), ui.ColError
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

// commitRename sends the typed name to the relay. An unchanged name just
// closes the field; an invalid one keeps it open with a hint.
func (a *App) commitRename(ci control.ClientInfo) {
	name := strings.TrimSpace(a.rename.field.Edit.String())
	if name == ci.Name {
		a.endRename()
		return
	}
	if !validClientName(name) {
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
	a.run(a.backend.RenameClient(fp, name), func(err error) {
		delete(a.rename.busy, fp)
		if err != nil {
			a.notes[fp] = note{text: "Could not rename: " + err.Error(), until: time.Now().Add(6 * time.Second)}
		}
	})
}

// rowSubline is the small second line of a client row.
func (a *App) rowSubline(ci control.ClientInfo, tabIdx int) (string, rl.Color) {
	switch {
	case tabIdx >= 0:
		return "In session", ui.ColAccent
	case ci.Online && ci.Request != nil && ci.Request.State == control.RequestOpen:
		return "Asked for help at " + ci.Request.Created.Local().Format("15:04"), ui.ColWarn
	case ci.InSessionWith != "" && ci.InSessionWith != a.me:
		return "In session with " + ci.InSessionWith, ui.ColMuted
	case ci.Online:
		if ci.UserMachine != "" && !strings.EqualFold(ci.UserMachine, ci.Name) {
			return ci.UserMachine, ui.ColMuted
		}
		return "Online", ui.ColMuted
	case !ci.LastSeen.IsZero():
		return "Last seen " + lastSeen(ci.LastSeen, time.Now()), ui.ColMuted
	}
	return "Never connected", ui.ColMuted
}

// lastSeen formats a time relative to now: a clock time today, otherwise a date.
func lastSeen(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	y1, m1, d1 := t.Date()
	y2, m2, d2 := now.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return "today " + t.Format("15:04")
	case y1 == y2:
		return t.Format("2 Jan")
	}
	return t.Format("2 Jan 2006")
}

// openRequest takes a client's open help request.
func (a *App) openRequest(ci control.ClientInfo) {
	if a.backend == nil || ci.Request == nil {
		return
	}
	reqID, fp := ci.Request.ID, ci.Fingerprint
	a.opened[reqID] = time.Now()
	a.opening[fp] = true
	a.run(a.backend.OpenRequest(reqID), func(err error) {
		delete(a.opening, fp)
		if err != nil {
			delete(a.opened, reqID)
			a.notes[fp] = note{text: "Could not open: " + err.Error(), until: time.Now().Add(6 * time.Second)}
		}
	})
}

// unregister removes a client from the relay.
func (a *App) unregister(ci control.ClientInfo) {
	a.confirmRemove = ""
	if a.backend == nil {
		return
	}
	fp := ci.Fingerprint
	a.run(a.backend.UnregisterClient(fp), func(err error) {
		if err != nil {
			a.notes[fp] = note{text: "Could not remove: " + err.Error(), until: time.Now().Add(6 * time.Second)}
		}
	})
}
