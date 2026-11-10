package views

import (
	"strings"
	"unicode"
	"unicode/utf8"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/helpbuoy/helpbuoy/desk/serverconn"
	"github.com/helpbuoy/helpbuoy/desk/ui"
	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/proto/i18n"
	"github.com/helpbuoy/helpbuoy/proto/serveraddr"
)

// infoForm edits who runs the server on the Server tab.
type infoForm struct {
	company     ui.Field
	phone       ui.Field
	address     ui.Field
	description ui.Field
	busy        bool
	result      string
	failed      bool
}

func (f *infoForm) init() {
	f.company.Placeholder = i18n.Trans("For example {0}", "Example Inc")
	f.company.Edit.Max = control.MaxCompany
	f.phone.Placeholder = i18n.Trans("For example {0}", "+1 234 567 890")
	f.phone.Edit.Max = control.MaxPhone
	f.address.Placeholder = i18n.Trans("For example {0}", "helpme.example.com")
	f.address.Edit.Max = control.MaxAddress
	f.description.Placeholder = i18n.Trans("For example {0}", i18n.Trans("Monday to Friday 9:00 to 17:00"))
	f.description.Edit.Max = control.MaxDescription
}

// fields returns the form's fields with the ServerInfo field each edits.
func (f *infoForm) fields(info *control.ServerInfo) []struct {
	field *ui.Field
	value *string
} {
	return []struct {
		field *ui.Field
		value *string
	}{
		{&f.company, &info.Company},
		{&f.phone, &info.Phone},
		{&f.address, &info.Address},
		{&f.description, &info.Description},
	}
}

// typed returns what the techie typed, trimmed.
func (f *infoForm) typed() control.ServerInfo {
	return control.ServerInfo{
		Company:     strings.TrimSpace(f.company.Edit.String()),
		Phone:       strings.TrimSpace(f.phone.Edit.String()),
		Address:     strings.TrimSpace(f.address.Edit.String()),
		Description: strings.TrimSpace(f.description.Edit.String()),
	}
}

// validLine reports whether the server accepts s as a server info field:
// empty, or at most max printable characters.
func validLine(s string, max int) bool {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// normAddress returns the address as the server stores it (host:port), or s
// itself when it is empty or not a valid address.
func normAddress(s string) string {
	if a, err := serveraddr.Parse(s); err == nil {
		return a.String()
	}
	return s
}

// checkInfo returns why the server would refuse info, or "" when it is fine.
// missing is true when only a required field is empty.
func checkInfo(info control.ServerInfo) (problem string, missing bool) {
	if !validLine(info.Company, control.MaxCompany) || !validLine(info.Phone, control.MaxPhone) ||
		!validLine(info.Address, control.MaxAddress) || !validLine(info.Description, control.MaxDescription) {
		return i18n.Trans("Company name up to {0}, phone number up to {1}, description up to {2} printable characters.",
			control.MaxCompany, control.MaxPhone, control.MaxDescription), false
	}
	if info.Address != "" {
		if _, err := serveraddr.Parse(info.Address); err != nil {
			return i18n.Trans("The address is not valid: {0}", err), false
		}
	}
	if info.Company == "" || info.Phone == "" {
		return i18n.Trans("Enter the company name and phone number."), true
	}
	return "", false
}

// setServerInfo stores who runs the server (nil when not connected). Fields
// the techie did not change follow the server's values.
func (a *App) setServerInfo(info *control.ServerInfo) {
	f := &a.infoForm
	var old, now control.ServerInfo
	if a.info != nil {
		old = *a.info
	}
	if info != nil {
		now = *info
	}
	olds, nows := f.fields(&old), f.fields(&now)
	for i, o := range olds {
		if o.field.Edit.String() == *o.value {
			o.field.Edit.SetText(*nows[i].value)
		}
	}
	if info == nil {
		f.result, f.failed = "", false
	}
	a.info = info
}

// notSetUp reports whether the connected server has no company name and
// phone number yet, so it refuses computers.
func (a *App) notSetUp() bool {
	return a.info != nil && !a.info.Configured()
}

// drawNotSetUp draws the notice that computers cannot connect until the
// server is set up, with a button to the Server tab. It returns the y below.
func (a *App) drawNotSetUp(x, y, w float32) float32 {
	c := a.ui
	text := i18n.Trans("Computers cannot connect to this server until you enter the company name and phone number on the Server tab.")
	btnL := i18n.Trans("Open the Server tab")
	th := float32(len(c.Wrap(ui.Regular, ui.SizeBody, text, w-24))) * ui.SizeBody * 1.35
	h := 12 + th + 10 + 30 + 12
	box := rl.NewRectangle(x, y, w, h)
	rl.DrawRectangleRec(box, ui.ColErrorSoft)
	rl.DrawRectangleLinesEx(box, 1, ui.ColError)
	c.Paragraph(ui.Regular, ui.SizeBody, text, x+12, y+12, w-24, ui.ColText)
	if c.Button("setup/server", rl.NewRectangle(x+12, y+12+th+10, a.btnW(btnL, 100), 30), btnL, ui.Primary, false) {
		a.openPage(tabServer)
	}
	return y + h
}

// drawServerInfo draws the company name, phone number, address and
// description fields and Save.
func (a *App) drawServerInfo(x, y, w float32) float32 {
	c := a.ui
	f := &a.infoForm
	y += c.Paragraph(ui.Regular, ui.SizeSmall, i18n.Trans("Users waiting for a techie see this, so they know who is helping them and who to call. The company name and phone number are required before computers can connect."), x, y, w, ui.ColMuted)
	y += 10
	fw := min(w, 560)
	var changedAny, submitted bool
	for _, fl := range []struct {
		id, label string
		field     *ui.Field
	}{
		{"server/company", i18n.Trans("Company name"), &f.company},
		{"server/phone", i18n.Trans("Phone number"), &f.phone},
		{"server/address", i18n.Trans("Address computers use (optional)"), &f.address},
		{"server/description", i18n.Trans("Description, for example opening hours (optional)"), &f.description},
	} {
		y = label(c, fl.label, x, y)
		r := c.TextField(fl.id, rl.NewRectangle(x, y, fw, 40), fl.field)
		changedAny = changedAny || r.Changed
		submitted = submitted || r.Submitted
		y += 48
	}
	if changedAny {
		f.result = ""
	}
	info := f.typed()
	problem, missing := checkInfo(info)
	if problem != "" {
		col := ui.ColError
		if missing {
			col = ui.ColWarn
		}
		y += c.Paragraph(ui.Regular, ui.SizeSmall, problem, x, y, w, col)
		y += 3
	}
	changed := a.info != nil && (info.Company != a.info.Company || info.Phone != a.info.Phone ||
		normAddress(info.Address) != a.info.Address || info.Description != a.info.Description)
	ready := problem == "" && changed && !f.busy && a.online()
	saveL, savingL := i18n.Trans("Save"), i18n.Trans("Saving…")
	text := saveL
	if f.busy {
		text = savingL
	}
	if c.Button("server/saveinfo", rl.NewRectangle(x, y, max(a.btnW(saveL, 130), a.btnW(savingL, 130)), 34), text, ui.Primary, !ready) || (submitted && ready) {
		a.saveServerInfo(info)
	}
	y += 44
	if f.result != "" {
		col := ui.ColOK
		if f.failed {
			col = ui.ColError
		}
		y += c.Paragraph(ui.Regular, ui.SizeBody, f.result, x, y, w, col)
	}
	return y + 12
}

// drawServerInfoReadOnly shows who runs the server to a techie limited to
// some customers, who may not change it.
func (a *App) drawServerInfoReadOnly(x, y, w float32) float32 {
	c := a.ui
	for _, f := range []struct{ label, value string }{
		{i18n.Trans("Company name"), a.info.Company},
		{i18n.Trans("Phone number"), a.info.Phone},
		{i18n.Trans("Address computers use (optional)"), a.info.Address},
		{i18n.Trans("Description, for example opening hours (optional)"), a.info.Description},
	} {
		if f.value == "" {
			continue
		}
		y = label(c, f.label, x, y)
		c.Text(ui.Regular, ui.SizeBody, c.Ellipsize(ui.Regular, ui.SizeBody, f.value, w), x, y, ui.ColText)
		y += 30
	}
	y += c.Paragraph(ui.Regular, ui.SizeSmall, i18n.Trans("You help only some customers of this server, so you can't change this."), x, y, w, ui.ColMuted)
	return y + 16
}

// saveServerInfo sends SetServerInfo.
func (a *App) saveServerInfo(info control.ServerInfo) {
	f := &a.infoForm
	if f.busy || a.backend == nil {
		return
	}
	f.busy, f.result = true, ""
	a.run(a.backend.SetServerInfo(info), func(err error) {
		f.busy = false
		if err != nil {
			f.result, f.failed = i18n.Trans("Not saved: {0}", err), true
			return
		}
		f.result, f.failed = i18n.Trans("Saved."), false
		// The server stores the address as host:port; show it so.
		if t := strings.TrimSpace(f.address.Edit.String()); t != "" && normAddress(t) != t {
			f.address.Edit.SetText(normAddress(t))
		}
	})
}

func label(c *ui.Context, text string, x, y float32) float32 {
	c.Text(ui.Regular, ui.SizeSmall, text, x, y, ui.ColMuted)
	return y + 20
}

// drawServer is the Server tab: the connection status, the address (for the
// server inside this app, the addresses to give out) and the fingerprint to read out to users, and Disconnect.
func (a *App) drawServer(x, y, w float32) float32 {
	c := a.ui
	c.Text(ui.Bold, ui.SizeHeading, i18n.Trans("Server"), x, y, ui.ColText)
	y += 40
	text, detail, col := a.statusText()
	rl.DrawCircleV(rl.NewVector2(x+5, y+9), 4.5, col)
	c.Text(ui.Regular, ui.SizeBody, text, x+16, y, ui.ColText)
	y += 24
	if detail != "" {
		y += c.Paragraph(ui.Regular, ui.SizeSmall, detail, x+16, y, w-16, ui.ColMuted)
	}
	y += 12
	if a.localShown() {
		// Not localhost, which on the other computers points at themselves.
		y = a.drawGiveOut(x, y, w)
	} else {
		y = label(c, i18n.Trans("Address"), x, y)
		c.Text(ui.Regular, ui.SizeBody, c.Ellipsize(ui.Regular, ui.SizeBody, a.addr.String(), w), x, y, ui.ColText)
		y += 30
	}
	y = label(c, i18n.Trans("Server fingerprint (computers show this on first contact)"), x, y)
	if fp := a.serverFingerprint(); fp != "" {
		c.Text(ui.Mono, ui.SizeBody, fp, x, y, ui.ColText)
	} else {
		c.Text(ui.Regular, ui.SizeBody, i18n.Trans("Not known yet; pinned on the first connection."), x, y, ui.ColMuted)
	}
	y += 34
	switch {
	case a.info != nil && a.limited():
		y = a.drawServerInfoReadOnly(x, y, w)
	case a.info != nil:
		y = a.drawServerInfo(x, y, w)
	}
	if a.demo == nil {
		busy := a.sessionCount() > 0
		discL := i18n.Trans("Disconnect")
		r := rl.NewRectangle(x, y, a.btnW(discL, 130), 34)
		if a.status.Status == serverconn.StatusReplaced {
			recL := i18n.Trans("Reconnect")
			rr := rl.NewRectangle(x, y, a.btnW(recL, 130), 34)
			if c.Button("server/reconnect", rr, recL, ui.Primary, false) && a.conn != nil {
				a.conn.Reconnect()
			}
			r.X = rr.X + rr.Width + 12
		}
		if c.Button("server/disconnect", r, discL, ui.Normal, busy) {
			a.disconnect()
		}
		if busy {
			c.Text(ui.Regular, ui.SizeSmall, i18n.Trans("End the open sessions first."), r.X+r.Width+12, y+9, ui.ColMuted)
		}
		y += 46
	}
	if a.cfgErr != nil {
		y += c.Paragraph(ui.Regular, ui.SizeSmall, i18n.Trans("Settings file: {0}", a.cfgErr), x, y, w, ui.ColError)
		y += 8
	}

	y += 12
	return y
}
