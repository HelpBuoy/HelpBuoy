package views

import (
	"errors"
	"strings"

	rl "github.com/gen2brain/raylib-go/raylib"
	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/desk/ui"
	"github.com/helpbuoy/helpbuoy/proto/i18n"
	"github.com/helpbuoy/helpbuoy/proto/identity"
)

// registerPage is the state of the Register computer or Register techie page.
type registerPage struct {
	techie bool // Register techie; otherwise Register computer

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

func (p *registerPage) init(techie bool) {
	p.techie = techie
	p.key.Mono = true
	if techie {
		p.key.Placeholder = i18n.Trans("Paste the key the techie sent you (ssh-ed25519 AAAA…)")
		p.name.Placeholder = i18n.Trans("Name, e.g. bob")
		p.name.Edit.Max = maxTechieName
		return
	}
	p.key.Placeholder = i18n.Trans("Paste the key the user sent you (ssh-ed25519 AAAA…)")
	p.name.Placeholder = i18n.Trans("Name, e.g. Anna laptop")
	p.name.Edit.Max = maxComputerName
}

// maxTechieName is the server's limit for techie names.
const maxTechieName = 64

// validTechieName reports whether the server accepts s as a techie name:
// 1 to 64 printable characters without spaces.
func validTechieName(s string) bool {
	return validComputerName(s) && len(s) <= maxTechieName && !strings.ContainsAny(s, " \t")
}

// techieNameFrom suggests a techie name from a key comment: the user part
// of user@machine, lower case.
func techieNameFrom(comment string) string {
	name := strings.TrimSpace(comment)
	if i := strings.IndexByte(name, '@'); i >= 0 {
		name = name[:i]
	}
	name = strings.ToLower(name)
	if !validTechieName(name) {
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
		if p.techie {
			name = techieNameFrom(p.comment)
		} else if !validComputerName(name) {
			name = ""
		}
		p.name.Edit.SetText(name)
	}
}

// nameOK reports whether the name field holds a name the server accepts.
func (p *registerPage) nameOK() bool {
	if p.techie {
		return validTechieName(strings.TrimSpace(p.name.Edit.String()))
	}
	return validComputerName(p.name.Edit.String())
}

// parseMessage turns a parse error into advice for the techie.
func parseMessage(err error) string {
	switch {
	case errors.Is(err, identity.ErrNotEd25519):
		return i18n.Trans("This is not a HelpBuoy key. Ask the user to copy the key shown in the app.")
	case errors.Is(err, identity.ErrNoKey):
		return i18n.Trans("No HelpBuoy key found in this text. Ask the user to copy the key shown in the app.")
	}
	return err.Error()
}

// registerPageFor returns the draw function of the Register computer or
// Register techie page for a.page.
func (a *App) registerPageFor(p *registerPage) func(x, y, w float32) float32 {
	return func(x, y, w float32) float32 { return a.drawRegister(p, x, y, w) }
}

func (a *App) drawRegister(p *registerPage, x, y, w float32) float32 {
	c := a.ui
	id := "reg/"
	title, intro := i18n.Trans("Register computer"), i18n.Trans("Paste the key line the user sent you in the chat. Extra words, quotes and line breaks are fine.")
	nameLabel, fpLabel := i18n.Trans("NAME (as techies see it in the computer list)"), i18n.Trans("FINGERPRINT (compare with the user on the phone)")
	nameRule := i18n.Trans("1 to {0} printable characters.", maxComputerName)
	if p.techie {
		id = "regtechie/"
		title, intro = i18n.Trans("Register techie"), i18n.Trans("Paste the key line the other techie sent you. Their app shows it when they connect to this server before being added. They can then help all its computers.")
		nameLabel, fpLabel = i18n.Trans("NAME (one word, as computers and techies see it)"), i18n.Trans("FINGERPRINT (compare with the techie on the phone)")
		nameRule = i18n.Trans("1 to {0} characters without spaces.", maxTechieName)
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
			y += c.Paragraph(ui.Regular, ui.SizeSmall, i18n.Trans("The key has no name. Type one to register it as:"), x, y, w, ui.ColWarn)
			y += 4
		}
		if c.TextField(id+"name", rl.NewRectangle(x, y, min(w, 420), 40), &p.name).Submitted {
			a.register(p)
		}
		y += 48
		if n := p.name.Edit.String(); n != "" && !p.nameOK() {
			c.Text(ui.Regular, ui.SizeSmall, nameRule, x, y, ui.ColError)
			y += 22
		}
		c.Text(ui.Regular, ui.SizeSmall, fpLabel, x, y, ui.ColMuted)
		y += 22
		y = a.bigFingerprint(fp, x, y, w)
		y += 10
		if known := a.registeredAs(fp); known != "" {
			y += c.Paragraph(ui.Regular, ui.SizeBody, known, x, y, w, ui.ColWarn)
			y += 6
		}
	}

	if rfp := a.serverFingerprint(); rfp != "" && !p.techie {
		y += 8
		c.Text(ui.Regular, ui.SizeSmall, i18n.Trans("SERVER FINGERPRINT the user's app should show"), x, y, ui.ColMuted)
		y += 22
		c.Text(ui.Mono, ui.SizeBody, rfp, x, y, ui.ColText)
		y += 32
	}

	y += 8
	ready := p.pubKey != nil && p.nameOK()
	regL, regingL := i18n.Trans("Register"), i18n.Trans("Registering…")
	label := regL
	if p.busy {
		label = regingL
	}
	if c.Button(id+"submit", rl.NewRectangle(x, y, max(a.btnW(regL, 140), a.btnW(regingL, 140)), 38), label, ui.Primary, !ready || p.busy || !a.online()) || (res.Submitted && ready) {
		a.register(p)
	}
	y += 48
	if !a.online() {
		c.Text(ui.Regular, ui.SizeSmall, i18n.Trans("Registering needs a connection to the server."), x, y, ui.ColMuted)
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
// server ("Already registered as computer anna@PC."), or "".
func (a *App) registeredAs(fp string) string {
	if a.presence == nil {
		return ""
	}
	for _, c := range a.presence.Computers {
		if c.Fingerprint == fp {
			return i18n.Trans("Already registered as computer {0}.", c.Name)
		}
	}
	for _, h := range a.presence.Techies {
		if h.Fingerprint == fp {
			return i18n.Trans("Already registered as techie {0}.", h.Name)
		}
	}
	return ""
}

// register sends RegisterComputer or RegisterTechie for the parsed key.
func (a *App) register(p *registerPage) {
	if p.pubKey == nil || p.busy || a.backend == nil || !p.nameOK() {
		return
	}
	name := strings.TrimSpace(p.name.Edit.String())
	p.busy, p.result = true, ""
	send := a.backend.RegisterComputer
	if p.techie {
		send = a.backend.RegisterTechie
	}
	a.run(send(p.line, name), func(err error) {
		p.busy = false
		if err != nil {
			p.result, p.failed = i18n.Trans("Not registered: {0}", err), true
			return
		}
		// Back to the list, where the new entry shows. A Register tab of
		// the other kind opened meanwhile stays.
		k := tabRegister
		if p.techie {
			k = tabRegisterTechie
		}
		if cur, ok := a.registerOpen(); ok && cur == k {
			a.closeRegister()
		} else {
			p.reset()
		}
	})
}
