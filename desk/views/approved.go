package views

import (
	"context"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/helpbuoy/helpbuoy/desk/ui"
	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/proto/hostprobe"
	"github.com/helpbuoy/helpbuoy/proto/i18n"
	"github.com/helpbuoy/helpbuoy/proto/serveraddr"
	"github.com/helpbuoy/helpbuoy/proto/sysproxy"
	"github.com/helpbuoy/helpbuoy/proto/transport"
)

// approvedForm adds and removes the other servers that the computers of
// this server may use (PLAN.md 12.3), on the Server tab. A server is added
// by its address: the desk app looks up the host key the server shows at
// that moment and the computers only accept that key from then on, trust on
// first use.
type approvedForm struct {
	name    ui.Field
	address ui.Field
	busy    bool
	result  string
	failed  bool
	confirm string // address of the server awaiting Remove confirmation

	// The inline rename of a server in the list.
	renaming   string // address of the server being renamed, or ""
	renameName ui.Field
	renameBad  bool // the typed name was rejected
	renameBlur bool // drop the field's focus at the start of the next frame
}

// approvedRenameID is the ui id of the inline rename field.
const approvedRenameID = "approved/rename"

// beginFrame drops the focus of a closed rename field, a frame later so the
// Enter or Esc that closed it does not reach anything else in that frame.
func (f *approvedForm) beginFrame(c *ui.Context) {
	if f.renameBlur {
		if c.Focus == approvedRenameID {
			c.Focus = ""
		}
		f.renameBlur = false
	}
}

func (f *approvedForm) endRename() {
	f.renaming, f.renameBlur = "", true
}

func (f *approvedForm) init() {
	f.name.Placeholder = i18n.Trans("For example {0}", "Vendor Inc")
	f.name.Edit.Max = control.MaxApprovedName
	f.address.Placeholder = i18n.Trans("For example {0}", "help.example.com")
	f.address.Edit.Max = 255
}

// reset empties the form and closes the Remove confirmation.
func (f *approvedForm) reset() {
	f.name.Edit.SetText("")
	f.address.Edit.SetText("")
	f.result, f.failed, f.confirm = "", false, ""
	if f.renaming != "" {
		f.endRename()
	}
}

// checkApproved returns the name and normalized address the techie typed,
// or why the server would refuse them; empty is true when the address is
// still empty. The name is optional.
func checkApproved(name, address string) (s control.ApprovedServer, problem string, empty bool) {
	name, address = strings.TrimSpace(name), strings.TrimSpace(address)
	if !validLine(name, control.MaxApprovedName) {
		return s, i18n.Trans("The name can have up to {0} printable characters.", control.MaxApprovedName), false
	}
	if address == "" {
		return s, "", true
	}
	addr, err := serveraddr.Parse(address)
	if err != nil {
		return s, i18n.Trans("This is not a server address."), false
	}
	return control.ApprovedServer{Address: addr.String(), Name: name}, "", false
}

// approvedRowH is the height of one server in the list of other servers.
const approvedRowH = 44

// drawApproved draws the other servers the computers of this server may
// use, each with Rename and Remove, and the fields to add one. It returns
// the y below.
func (a *App) drawApproved(x, y, w float32) float32 {
	c := a.ui
	f := &a.approvedForm
	c.Text(ui.Bold, ui.SizeBody, i18n.Trans("Other servers"), x, y, ui.ColText)
	y += 26
	y += c.Paragraph(ui.Regular, ui.SizeSmall, i18n.Trans("Computers that are locked to this server can also get help through these servers, for example the server of a software vendor. This holds for all computers of this server. A server is remembered by the key it shows when you add it."), x, y, w, ui.ColMuted)
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
	f.beginFrame(c)
	removeL, cancelL, renameL := i18n.Trans("Remove"), i18n.Trans("Cancel"), i18n.Trans("Rename")
	wRemove, wCancel, wRename := a.btnW(removeL, 66), a.btnW(cancelL, 68), a.btnW(renameL, 66)
	for _, s := range servers {
		id := "approved/" + s.Address
		if f.renaming == s.Address {
			a.drawApprovedRename(s, rl.NewRectangle(x, y-3, fw, 28))
			y += approvedRowH
			continue
		}
		confirming := f.confirm == s.Address
		actionW := wRename + 4 + wRemove
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
		} else {
			if c.Button(id+"/rename", rl.NewRectangle(ax, ay, wRename, 30), renameL, ui.Flat, !a.online()) {
				f.renaming, f.renameBad, f.confirm = s.Address, false, ""
				f.renameName.Edit.Max = control.MaxApprovedName
				f.renameName.Placeholder = s.Address
				f.renameName.Edit.SetText(s.Name)
				f.renameName.Edit.SelectAll()
				c.Focus = approvedRenameID
			}
			if c.Button(id+"/remove", rl.NewRectangle(ax+wRename+4, ay, wRemove, 30), removeL, ui.Flat, !a.online()) {
				f.confirm = s.Address
			}
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
	s, problem, empty := checkApproved(f.name.Edit.String(), f.address.Edit.String())
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

// drawApprovedRename draws the inline name field of the server being
// renamed: Enter saves, Esc or a click elsewhere cancels. An empty name takes
// the name away, after which users see the address.
func (a *App) drawApprovedRename(s control.ApprovedServer, r rl.Rectangle) {
	c := a.ui
	f := &a.approvedForm
	res := c.TextField(approvedRenameID, r, &f.renameName)
	hint, col := i18n.Trans("Enter to save, Esc to cancel"), ui.ColMuted
	if f.renameBad {
		hint, col = i18n.Trans("The name can have up to {0} printable characters.", control.MaxApprovedName), ui.ColError
	}
	c.Text(ui.Regular, ui.SizeSmall, c.Ellipsize(ui.Regular, ui.SizeSmall, hint, r.Width), r.X, r.Y+r.Height+2, col)
	if res.Changed {
		f.renameBad = false
	}
	switch {
	case res.Submitted:
		name := strings.TrimSpace(f.renameName.Edit.String())
		if !validLine(name, control.MaxApprovedName) {
			f.renameBad = true
			return
		}
		f.endRename()
		if name == s.Name || a.backend == nil {
			return
		}
		addr := s.Address
		a.run(a.backend.RenameApproved(addr, name), func(err error) {
			if err != nil {
				a.notes["approved/"+addr] = note{text: i18n.Trans("Could not rename: {0}", err), until: a.ui.Now.Add(6 * time.Second)}
			}
		})
	case rl.IsKeyPressed(rl.KeyEscape), c.Focus != approvedRenameID:
		f.endRename()
	}
}

// addApproved looks up the host key of the server at s.Address and sends
// AddApproved with it, then empties the fields when it worked. Adding a
// server that is on the list with another name renames it, and one that
// shows another key now than when it was added gets the new key.
func (a *App) addApproved(s control.ApprovedServer) {
	f := &a.approvedForm
	if f.busy || a.backend == nil {
		return
	}
	f.busy, f.result, f.confirm = true, "", ""
	backend := a.backend
	probe := a.probeServer
	if probe == nil {
		proxy := sysproxy.Func()
		if a.cfg.NoSystemProxy {
			proxy = sysproxy.Off
		}
		probe = func(addr string) (string, error) {
			key, err := hostprobe.Probe(context.Background(), addr, 0, transport.DialOptions{Proxy: proxy})
			if err != nil {
				return "", err
			}
			return ssh.FingerprintSHA256(key), nil
		}
	}
	var fp string
	found := make(chan error, 1)
	go func() {
		var err error
		fp, err = probe(s.Address)
		found <- err
	}()
	a.run(found, func(err error) {
		if err != nil || a.backend != backend {
			f.busy = false
			f.result, f.failed = i18n.Trans("Not added: no HelpBuoy server answers at {0}.", s.Address), true
			return
		}
		a.run(backend.AddApproved(s.Address, fp, s.Name), func(err error) {
			f.busy = false
			if err != nil {
				f.result, f.failed = i18n.Trans("Not added: {0}", err), true
				return
			}
			f.name.Edit.SetText("")
			f.address.Edit.SetText("")
			f.result, f.failed = i18n.Trans("Added {0}.", s.Address), false
		})
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
