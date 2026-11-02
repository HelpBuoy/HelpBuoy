package views

import (
	"errors"
	"strings"

	rl "github.com/gen2brain/raylib-go/raylib"
	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/help-me-win/helper/ui"
	"github.com/mevdschee/help-me-win/proto/identity"
)

// registerPage is the state of the Register client page.
type registerPage struct {
	key  ui.Field
	name ui.Field

	parsedFor string // text the fields below were parsed from
	pubKey    ssh.PublicKey
	comment   string
	line      string
	parseErr  error

	busy   bool
	result string // last outcome, shown under the button
	failed bool
}

func (p *registerPage) init() {
	p.key.Placeholder = "Paste the key the user sent you (ssh-ed25519 AAAA…)"
	p.key.Mono = true
	p.name.Placeholder = "user@machine"
	p.name.Edit.Max = 64
}

// parse re-parses the pasted text when it changed.
func (p *registerPage) parse() {
	text := p.key.Edit.String()
	if text == p.parsedFor {
		return
	}
	p.parsedFor = text
	p.pubKey, p.comment, p.line, p.parseErr = nil, "", "", nil
	if strings.TrimSpace(text) == "" {
		return
	}
	p.pubKey, p.comment, p.line, p.parseErr = identity.ParsePasted(text)
	if p.parseErr != nil {
		p.pubKey = nil
	}
}

// needName reports whether the key's comment can't serve as the name.
func (p *registerPage) needName() bool {
	return p.comment == "" || !identity.ValidName(p.comment)
}

// parseMessage turns a parse error into advice for the helper.
func parseMessage(err error) string {
	switch {
	case errors.Is(err, identity.ErrNotEd25519):
		return "This is not a help-me-win key. Ask the user to copy the key shown in the app."
	case errors.Is(err, identity.ErrNoKey):
		return "No help-me-win key found in this text. Ask the user to copy the key shown in the app."
	}
	return err.Error()
}

func (a *App) drawRegister(x, y, w float32) float32 {
	c := a.ui
	p := &a.reg
	c.Text(ui.Bold, ui.SizeHeading, "Register client", x, y, ui.ColText)
	y += 34
	y += c.Paragraph(ui.Regular, ui.SizeBody, "Paste the key line the user sent you in the chat. Extra words, quotes and line breaks are fine.", x, y, w, ui.ColMuted)
	y += 12

	res := c.TextField("reg/key", rl.NewRectangle(x, y, w, 40), &p.key)
	if res.Changed {
		p.result = ""
	}
	y += 52
	p.parse()

	if p.parseErr != nil {
		y += c.Paragraph(ui.Regular, ui.SizeBody, parseMessage(p.parseErr), x, y, w, ui.ColError)
		y += 12
	}

	if p.pubKey != nil {
		fp := ssh.FingerprintSHA256(p.pubKey)
		c.Text(ui.Regular, ui.SizeSmall, "NAME", x, y, ui.ColMuted)
		y += 20
		if p.needName() {
			if p.comment != "" {
				y += c.Paragraph(ui.Regular, ui.SizeSmall, "The key's comment \""+p.comment+"\" can't be used as a name. Type one:", x, y, w, ui.ColWarn)
			} else {
				y += c.Paragraph(ui.Regular, ui.SizeSmall, "The key has no name. Type the user@machine to register it as:", x, y, w, ui.ColWarn)
			}
			y += 4
			if c.TextField("reg/name", rl.NewRectangle(x, y, min(w, 360), 40), &p.name).Submitted {
				a.register()
			}
			y += 48
			if n := p.name.Edit.String(); n != "" && !identity.ValidName(n) {
				c.Text(ui.Regular, ui.SizeSmall, "1 to 64 characters, no spaces.", x, y, ui.ColError)
				y += 22
			}
		} else {
			c.Text(ui.Bold, ui.SizeLarge, c.Ellipsize(ui.Bold, ui.SizeLarge, p.comment, w), x, y, ui.ColText)
			y += 40
		}
		c.Text(ui.Regular, ui.SizeSmall, "FINGERPRINT (compare with the user on the phone)", x, y, ui.ColMuted)
		y += 22
		y = a.bigFingerprint(fp, x, y, w)
		y += 10
		if name := a.registeredName(fp); name != "" {
			c.Text(ui.Regular, ui.SizeBody, "Already registered as "+name+".", x, y, ui.ColWarn)
			y += 28
		}
	}

	if rfp := a.relayFingerprint(); rfp != "" {
		y += 8
		c.Text(ui.Regular, ui.SizeSmall, "RELAY FINGERPRINT the user's app should show", x, y, ui.ColMuted)
		y += 22
		c.Text(ui.Mono, ui.SizeBody, rfp, x, y, ui.ColText)
		y += 32
	}

	y += 8
	ready := p.pubKey != nil && (!p.needName() || identity.ValidName(p.name.Edit.String()))
	label := "Register"
	if p.busy {
		label = "Registering…"
	}
	if c.Button("reg/submit", rl.NewRectangle(x, y, 140, 38), label, ui.Primary, !ready || p.busy || !a.online()) || (res.Submitted && ready) {
		a.register()
	}
	y += 48
	if !a.online() {
		c.Text(ui.Regular, ui.SizeSmall, "Registering needs a connection to the relay.", x, y, ui.ColMuted)
		y += 22
	}
	if p.result != "" {
		col := ui.ColOK
		if p.failed {
			col = ui.ColError
		}
		y += c.Paragraph(ui.Regular, ui.SizeBody, p.result, x, y, w, col)
	}
	return y
}

// bigFingerprint draws a fingerprint large, on two lines if it doesn't fit.
func (a *App) bigFingerprint(fp string, x, y, w float32) float32 {
	c := a.ui
	for _, size := range []float32{ui.SizeLarge, ui.SizeHeading} {
		if c.Measure(ui.Mono, size, fp).X <= w {
			c.Text(ui.Mono, size, fp, x, y, ui.ColText)
			return y + size*1.5
		}
	}
	half := len(fp) / 2
	c.Text(ui.Mono, ui.SizeHeading, fp[:half], x, y, ui.ColText)
	c.Text(ui.Mono, ui.SizeHeading, fp[half:], x, y+ui.SizeHeading*1.3, ui.ColText)
	return y + ui.SizeHeading*2.8
}

// registeredName returns the name a fingerprint is registered under, or "".
func (a *App) registeredName(fp string) string {
	if a.presence == nil {
		return ""
	}
	for _, c := range a.presence.Clients {
		if c.Fingerprint == fp {
			return c.Name
		}
	}
	return ""
}

// register sends RegisterClient for the parsed key.
func (a *App) register() {
	p := &a.reg
	if p.pubKey == nil || p.busy || a.backend == nil {
		return
	}
	name := ""
	if p.needName() {
		name = p.name.Edit.String()
		if !identity.ValidName(name) {
			return
		}
	}
	shown := name
	if shown == "" {
		shown = p.comment
	}
	p.busy, p.result = true, ""
	a.run(a.backend.RegisterClient(p.line, name), func(err error) {
		p.busy = false
		if err != nil {
			p.result, p.failed = "Not registered: "+err.Error(), true
			return
		}
		p.result, p.failed = "Registered "+shown+".", false
		p.key.Edit.SetText("")
		p.name.Edit.SetText("")
	})
}
