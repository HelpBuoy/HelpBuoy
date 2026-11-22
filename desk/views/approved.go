package views

import (
	"strings"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/helpbuoy/helpbuoy/desk/ui"
	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/proto/i18n"
	"github.com/helpbuoy/helpbuoy/proto/serveraddr"
)

// approvedForm adds and removes the other servers that the computers of
// this server may use (PLAN.md 12.3), on the Server tab.
type approvedForm struct {
	name        ui.Field
	address     ui.Field
	fingerprint ui.Field
	busy        bool
	result      string
	failed      bool
	confirm     string // address of the server awaiting Remove confirmation
}

func (f *approvedForm) init() {
	f.name.Placeholder = i18n.Trans("For example {0}", "Vendor Inc")
	f.name.Edit.Max = control.MaxApprovedName
	f.address.Placeholder = i18n.Trans("For example {0}", "help.example.com")
	f.address.Edit.Max = 255
	f.fingerprint.Placeholder = "SHA256:…"
	f.fingerprint.Mono = true
	f.fingerprint.Edit.Max = 64
}

// reset empties the form and closes the Remove confirmation.
func (f *approvedForm) reset() {
	f.name.Edit.SetText("")
	f.address.Edit.SetText("")
	f.fingerprint.Edit.SetText("")
	f.result, f.failed, f.confirm = "", false, ""
}

// checkApproved returns the server the techie typed, or why the server
// would refuse it; empty is true when the address or the fingerprint is
// still empty. The name is optional.
func checkApproved(name, address, fingerprint string) (s control.ApprovedServer, problem string, empty bool) {
	name, address, fingerprint = strings.TrimSpace(name), strings.TrimSpace(address), strings.TrimSpace(fingerprint)
	if !validLine(name, control.MaxApprovedName) {
		return s, i18n.Trans("The name can have up to {0} printable characters.", control.MaxApprovedName), false
	}
	if address == "" || fingerprint == "" {
		return s, "", true
	}
	addr, err := serveraddr.Parse(address)
	if err != nil {
		return s, i18n.Trans("This is not a server address."), false
	}
	if !control.ValidFingerprint(fingerprint) {
		return s, i18n.Trans("A fingerprint is SHA256: followed by 43 characters."), false
	}
	return control.ApprovedServer{Address: addr.String(), Fingerprint: fingerprint, Name: name}, "", false
}

// approvedRowH is the height of one server in the list of other servers.
const approvedRowH = 44

// drawApproved draws the other servers the computers of this server may
// use, each with Remove, and the fields to add one. It returns the y below.
func (a *App) drawApproved(x, y, w float32) float32 {
	c := a.ui
	f := &a.approvedForm
	c.Text(ui.Bold, ui.SizeBody, i18n.Trans("Other servers"), x, y, ui.ColText)
	y += 26
	y += c.Paragraph(ui.Regular, ui.SizeSmall, i18n.Trans("Computers that are locked to this server can also get help through these servers, for example the server of a software vendor. This holds for all computers of this server."), x, y, w, ui.ColMuted)
	y += 10
	fw := min(w, 560)
	var servers []control.ApprovedServer
	if a.presence != nil {
		servers = a.presence.Approved
	}
	if len(servers) == 0 {
		c.Text(ui.Regular, ui.SizeSmall, i18n.Trans("No other servers are allowed."), x, y, ui.ColMuted)
		y += 26
	}
	removeL, cancelL := i18n.Trans("Remove"), i18n.Trans("Cancel")
	wRemove, wCancel := a.btnW(removeL, 66), a.btnW(cancelL, 68)
	for _, s := range servers {
		id := "approved/" + s.Address
		confirming := f.confirm == s.Address
		actionW := wRemove
		if confirming {
			actionW = wRemove + 6 + wCancel
		}
		tw := fw - actionW - 12
		sub, font, col := s.Fingerprint, ui.Mono, ui.ColMuted
		if n, ok := a.notes[id]; ok {
			sub, font, col = n.text, ui.Regular, ui.ColError
		}
		if confirming {
			sub, font, col = i18n.Trans("Remove this server? Locked computers can then no longer connect to it."), ui.Regular, ui.ColError
		}
		title := s.Address
		if s.Name != "" {
			title = s.Name + " · " + s.Address
		}
		c.Text(ui.Regular, ui.SizeBody, c.Ellipsize(ui.Regular, ui.SizeBody, title, tw), x, y, ui.ColText)
		c.Text(font, ui.SizeSmall, c.Ellipsize(font, ui.SizeSmall, sub, tw), x, y+21, col)
		ax, ay := x+fw-actionW, y+4
		if confirming {
			if c.Button(id+"/confirm", rl.NewRectangle(ax, ay, wRemove, 30), removeL, ui.Danger, !a.online()) {
				a.removeApproved(s.Address)
			}
			if c.Button(id+"/cancel", rl.NewRectangle(ax+wRemove+6, ay, wCancel, 30), cancelL, ui.Flat, false) {
				f.confirm = ""
			}
		} else if c.Button(id+"/remove", rl.NewRectangle(ax, ay, wRemove, 30), removeL, ui.Flat, !a.online()) {
			f.confirm = s.Address
		}
		y += approvedRowH
	}
	y += 6

	var changed, submitted bool
	for _, fl := range []struct {
		id, label string
		field     *ui.Field
	}{
		{"approved/name", i18n.Trans("Name users pick it by (optional)"), &f.name},
		{"approved/address", i18n.Trans("Address of the other server"), &f.address},
		{"approved/fingerprint", i18n.Trans("Its server fingerprint (the other helpdesk finds it on the Server tab)"), &f.fingerprint},
	} {
		y = label(c, fl.label, x, y)
		r := c.TextField(fl.id, rl.NewRectangle(x, y, fw, 40), fl.field)
		changed = changed || r.Changed
		submitted = submitted || r.Submitted
		y += 48
	}
	if changed {
		f.result = ""
	}
	s, problem, empty := checkApproved(f.name.Edit.String(), f.address.Edit.String(), f.fingerprint.Edit.String())
	if problem != "" {
		y += c.Paragraph(ui.Regular, ui.SizeSmall, problem, x, y, w, ui.ColError)
		y += 3
	}
	ready := problem == "" && !empty && !f.busy && a.online()
	addL, addingL := i18n.Trans("Add server"), i18n.Trans("Adding…")
	text := addL
	if f.busy {
		text = addingL
	}
	if c.Button("approved/add", rl.NewRectangle(x, y, max(a.btnW(addL, 130), a.btnW(addingL, 130)), 34), text, ui.Normal, !ready) || (submitted && ready) {
		a.addApproved(s)
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

// addApproved sends AddApproved and empties the fields when it worked.
// Adding a server that is on the list with another name renames it.
func (a *App) addApproved(s control.ApprovedServer) {
	f := &a.approvedForm
	if f.busy || a.backend == nil {
		return
	}
	f.busy, f.result, f.confirm = true, "", ""
	a.run(a.backend.AddApproved(s.Address, s.Fingerprint, s.Name), func(err error) {
		f.busy = false
		if err != nil {
			f.result, f.failed = i18n.Trans("Not added: {0}", err), true
			return
		}
		f.name.Edit.SetText("")
		f.address.Edit.SetText("")
		f.fingerprint.Edit.SetText("")
		f.result, f.failed = i18n.Trans("Added {0}.", s.Address), false
	})
}

// removeApproved sends RemoveApproved for the server at address.
func (a *App) removeApproved(address string) {
	f := &a.approvedForm
	f.confirm = ""
	if a.backend == nil {
		return
	}
	a.run(a.backend.RemoveApproved(address), func(err error) {
		if err != nil {
			a.notes["approved/"+address] = note{text: i18n.Trans("Could not remove: {0}", err), until: a.ui.Now.Add(6 * time.Second)}
		}
	})
}
