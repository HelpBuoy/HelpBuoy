package views

import (
	"errors"
	"fmt"
	"strings"

	rl "github.com/gen2brain/raylib-go/raylib"
	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/help-me-win/helper/ui"
	"github.com/mevdschee/help-me-win/proto/identity"
)

// registerPage is the state of the Register client or Register helper page.
type registerPage struct {
	helper bool // Register helper; otherwise Register client

	key  ui.Field
	name ui.Field

	parsedFor string // text the fields below were parsed from
	pubKey    ssh.PublicKey
	comment   string
	line      string
	parseErr  error
	nameFor   string // key line the name field was filled for

	busy   bool
	result string // last outcome, shown under the button
	failed bool
}

func (p *registerPage) init(helper bool) {
	p.helper = helper
	p.key.Mono = true
	if helper {
		p.key.Placeholder = "Paste the key the helper sent you (ssh-ed25519 AAAA…)"
		p.name.Placeholder = "Name, e.g. bob"
		p.name.Edit.Max = maxHelperName
		return
	}
	p.key.Placeholder = "Paste the key the user sent you (ssh-ed25519 AAAA…)"
	p.name.Placeholder = "Name, e.g. Anna laptop"
	p.name.Edit.Max = maxClientName
}

// maxHelperName is the relay's limit for helper names.
const maxHelperName = 64

// validHelperName reports whether the relay accepts s as a helper name:
// 1 to 64 printable characters without spaces.
func validHelperName(s string) bool {
	return validClientName(s) && len(s) <= maxHelperName && !strings.ContainsAny(s, " \t")
}

// helperNameFrom suggests a helper name from a key comment: the user part
// of user@machine, lower case.
func helperNameFrom(comment string) string {
	name := strings.TrimSpace(comment)
	if i := strings.IndexByte(name, '@'); i >= 0 {
		name = name[:i]
	}
	name = strings.ToLower(name)
	if !validHelperName(name) {
		return ""
	}
	return name
}

// reset empties the form, unless a registration is still running.
func (p *registerPage) reset() {
	if p.busy {
		return
	}
	p.key.Edit.SetText("")
	p.name.Edit.SetText("")
	p.parsedFor, p.nameFor = "", ""
	p.pubKey, p.comment, p.line, p.parseErr = nil, "", "", nil
	p.result, p.failed = "", false
}

// parse re-parses the pasted text when it changed. A newly found key fills
// the name field with its comment (user@machine), which stays editable.
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
		return
	}
	if p.line != p.nameFor {
		p.nameFor = p.line
		name := strings.TrimSpace(p.comment)
		if p.helper {
			name = helperNameFrom(p.comment)
		} else if !validClientName(name) {
			name = ""
		}
		p.name.Edit.SetText(name)
	}
}

// nameOK reports whether the name field holds a name the relay accepts.
func (p *registerPage) nameOK() bool {
	if p.helper {
		return validHelperName(strings.TrimSpace(p.name.Edit.String()))
	}
	return validClientName(p.name.Edit.String())
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

// registerPageFor returns the draw function of the Register client or
// Register helper page for a.page.
func (a *App) registerPageFor(p *registerPage) func(x, y, w float32) float32 {
	return func(x, y, w float32) float32 { return a.drawRegister(p, x, y, w) }
}

func (a *App) drawRegister(p *registerPage, x, y, w float32) float32 {
	c := a.ui
	id := "reg/"
	title, intro := "Register client", "Paste the key line the user sent you in the chat. Extra words, quotes and line breaks are fine."
	nameLabel, fpLabel := "NAME (as helpers see it in the client list)", "FINGERPRINT (compare with the user on the phone)"
	maxName, nameRule := maxClientName, "printable characters"
	if p.helper {
		id = "reghelper/"
		title, intro = "Register helper", "Paste the key line the other helper sent you. Their app shows it when they connect to this relay before being added. They can then help all its clients."
		nameLabel, fpLabel = "NAME (one word, as clients and helpers see it)", "FINGERPRINT (compare with the helper on the phone)"
		maxName, nameRule = maxHelperName, "characters without spaces"
	}
	c.Text(ui.Bold, ui.SizeHeading, title, x, y, ui.ColText)
	y += 34
	y += c.Paragraph(ui.Regular, ui.SizeBody, intro, x, y, w, ui.ColMuted)
	y += 12

	res := c.TextField(id+"key", rl.NewRectangle(x, y, w, 40), &p.key)
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
		c.Text(ui.Regular, ui.SizeSmall, nameLabel, x, y, ui.ColMuted)
		y += 20
		if p.name.Edit.String() == "" && strings.TrimSpace(p.comment) == "" {
			y += c.Paragraph(ui.Regular, ui.SizeSmall, "The key has no name. Type one to register it as:", x, y, w, ui.ColWarn)
			y += 4
		}
		if c.TextField(id+"name", rl.NewRectangle(x, y, min(w, 420), 40), &p.name).Submitted {
			a.register(p)
		}
		y += 48
		if n := p.name.Edit.String(); n != "" && !p.nameOK() {
			c.Text(ui.Regular, ui.SizeSmall, fmt.Sprintf("1 to %d %s.", maxName, nameRule), x, y, ui.ColError)
			y += 22
		}
		c.Text(ui.Regular, ui.SizeSmall, fpLabel, x, y, ui.ColMuted)
		y += 22
		y = a.bigFingerprint(fp, x, y, w)
		y += 10
		if known := a.registeredAs(fp); known != "" {
			c.Text(ui.Regular, ui.SizeBody, "Already registered as "+known+".", x, y, ui.ColWarn)
			y += 28
		}
	}

	if rfp := a.relayFingerprint(); rfp != "" && !p.helper {
		y += 8
		c.Text(ui.Regular, ui.SizeSmall, "RELAY FINGERPRINT the user's app should show", x, y, ui.ColMuted)
		y += 22
		c.Text(ui.Mono, ui.SizeBody, rfp, x, y, ui.ColText)
		y += 32
	}

	y += 8
	ready := p.pubKey != nil && p.nameOK()
	label := "Register"
	if p.busy {
		label = "Registering…"
	}
	if c.Button(id+"submit", rl.NewRectangle(x, y, 140, 38), label, ui.Primary, !ready || p.busy || !a.online()) || (res.Submitted && ready) {
		a.register(p)
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

// registeredAs describes what a fingerprint is registered as on the
// relay ("client anna@PC", "helper bob"), or "".
func (a *App) registeredAs(fp string) string {
	if a.presence == nil {
		return ""
	}
	for _, c := range a.presence.Clients {
		if c.Fingerprint == fp {
			return "client " + c.Name
		}
	}
	for _, h := range a.presence.Helpers {
		if h.Fingerprint == fp {
			return "helper " + h.Name
		}
	}
	return ""
}

// register sends RegisterClient or RegisterHelper for the parsed key.
func (a *App) register(p *registerPage) {
	if p.pubKey == nil || p.busy || a.backend == nil || !p.nameOK() {
		return
	}
	name := strings.TrimSpace(p.name.Edit.String())
	shown := name
	p.busy, p.result = true, ""
	send := a.backend.RegisterClient
	if p.helper {
		send = a.backend.RegisterHelper
	}
	a.run(send(p.line, name), func(err error) {
		p.busy = false
		if err != nil {
			p.result, p.failed = "Not registered: "+err.Error(), true
			return
		}
		p.result, p.failed = "Registered "+shown+".", false
		p.key.Edit.SetText("")
		p.name.Edit.SetText("")
		p.nameFor = ""
	})
}
