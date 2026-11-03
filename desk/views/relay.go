package views

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/mevdschee/helpbuoy/desk/ui"
	"github.com/mevdschee/helpbuoy/proto/control"
)

// The relay's limits for the title and instruction line.
const (
	maxTitle       = 64
	maxInstruction = 160
)

// infoForm edits the relay's title and instruction line on the Relay tab.
type infoForm struct {
	title       ui.Field
	instruction ui.Field
	busy        bool
	result      string
	failed      bool
}

func (f *infoForm) init() {
	f.title.Placeholder = "Empty: no title"
	f.title.Edit.Max = maxTitle
	f.instruction.Placeholder = "Empty: " + control.DefaultInstruction
	f.instruction.Edit.Max = maxInstruction
}

// validLine reports whether the relay accepts s as a title or instruction:
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

// setRelayInfo stores the relay's title and instruction (nil when not
// connected). Fields the agent did not change follow the relay's values.
func (a *App) setRelayInfo(info *control.RelayInfo) {
	f := &a.infoForm
	var oldT, oldI, newT, newI string
	if a.info != nil {
		oldT, oldI = a.info.Title, a.info.Instruction
	}
	if info != nil {
		newT, newI = info.Title, info.Instruction
	}
	if f.title.Edit.String() == oldT {
		f.title.Edit.SetText(newT)
	}
	if f.instruction.Edit.String() == oldI {
		f.instruction.Edit.SetText(newI)
	}
	if info == nil {
		f.result, f.failed = "", false
	}
	a.info = info
}

// drawRelayInfo draws the title and instruction fields and Save.
func (a *App) drawRelayInfo(x, y, w float32) float32 {
	c := a.ui
	f := &a.infoForm
	y += c.Paragraph(ui.Regular, ui.SizeSmall, "Clients waiting for an agent show this title and instruction line, for example your company name and \"Call +1 234 567 890 for support\". The grey text shows what an empty field gives.", x, y, w, ui.ColMuted)
	y += 6
	fw := min(w, 560)
	y = label(c, "Title", x, y)
	r1 := c.TextField("relay/title", rl.NewRectangle(x, y, fw, 40), &f.title)
	y += 48
	y = label(c, "Instruction", x, y)
	r2 := c.TextField("relay/instruction", rl.NewRectangle(x, y, fw, 40), &f.instruction)
	y += 48
	if r1.Changed || r2.Changed {
		f.result = ""
	}
	title, instr := f.title.Edit.String(), f.instruction.Edit.String()
	valid := validLine(title, maxTitle) && validLine(instr, maxInstruction)
	if !valid {
		c.Text(ui.Regular, ui.SizeSmall, fmt.Sprintf("Title up to %d, instruction up to %d printable characters.", maxTitle, maxInstruction), x, y, ui.ColError)
		y += 22
	}
	changed := a.info != nil && (strings.TrimSpace(title) != a.info.Title || strings.TrimSpace(instr) != a.info.Instruction)
	ready := valid && changed && !f.busy && a.online()
	text := "Save"
	if f.busy {
		text = "Saving…"
	}
	if c.Button("relay/saveinfo", rl.NewRectangle(x, y, 130, 34), text, ui.Primary, !ready) || ((r1.Submitted || r2.Submitted) && ready) {
		a.saveRelayInfo(title, instr)
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

// saveRelayInfo sends SetRelayInfo.
func (a *App) saveRelayInfo(title, instruction string) {
	f := &a.infoForm
	if f.busy || a.backend == nil {
		return
	}
	f.busy, f.result = true, ""
	a.run(a.backend.SetRelayInfo(title, instruction), func(err error) {
		f.busy = false
		if err != nil {
			f.result, f.failed = "Not saved: "+err.Error(), true
			return
		}
		f.result, f.failed = "Saved.", false
	})
}

func label(c *ui.Context, text string, x, y float32) float32 {
	c.Text(ui.Regular, ui.SizeSmall, text, x, y, ui.ColMuted)
	return y + 20
}

// drawRelay is the Relay tab: the connection status, the address and
// fingerprint to read out to users, and Disconnect.
func (a *App) drawRelay(x, y, w float32) float32 {
	c := a.ui
	c.Text(ui.Bold, ui.SizeHeading, "Relay", x, y, ui.ColText)
	y += 40
	text, detail, col := a.statusText()
	rl.DrawCircleV(rl.NewVector2(x+5, y+9), 4.5, col)
	c.Text(ui.Regular, ui.SizeBody, text, x+16, y, ui.ColText)
	y += 24
	if detail != "" {
		y += c.Paragraph(ui.Regular, ui.SizeSmall, detail, x+16, y, w-16, ui.ColMuted)
	}
	y += 12
	if a.demo == nil {
		y = label(c, "Address", x, y)
		c.Text(ui.Regular, ui.SizeBody, c.Ellipsize(ui.Regular, ui.SizeBody, a.addr.String(), w), x, y, ui.ColText)
		y += 30
	}
	y = label(c, "Relay fingerprint (clients show this on first contact)", x, y)
	if fp := a.relayFingerprint(); fp != "" {
		c.Text(ui.Mono, ui.SizeBody, fp, x, y, ui.ColText)
	} else {
		c.Text(ui.Regular, ui.SizeBody, "Not known yet; pinned on the first connection.", x, y, ui.ColMuted)
	}
	y += 34
	if a.info != nil {
		y = a.drawRelayInfo(x, y, w)
	}
	if a.demo == nil {
		busy := a.sessionCount() > 0
		r := rl.NewRectangle(x, y, 130, 34)
		if c.Button("relay/disconnect", r, "Disconnect", ui.Normal, busy) {
			a.disconnect()
		}
		if busy {
			c.Text(ui.Regular, ui.SizeSmall, "End the open sessions first.", x+142, y+9, ui.ColMuted)
		}
		y += 46
	}
	if a.cfgErr != nil {
		y += c.Paragraph(ui.Regular, ui.SizeSmall, "Settings file: "+a.cfgErr.Error(), x, y, w, ui.ColError)
		y += 8
	}

	y += 12
	c.Text(ui.Regular, ui.SizeSmall, "HelpBuoy Desk "+a.opts.Version, x, y, ui.ColMuted)
	return y + 20
}
