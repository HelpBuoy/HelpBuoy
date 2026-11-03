package views

import (
	"sort"
	"strings"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/mevdschee/helpbuoy/desk/ui"
	"github.com/mevdschee/helpbuoy/proto/control"
)

// sortedAgents returns the agents of the presence, online first, each
// group sorted by name.
func sortedAgents(p *control.Presence) []control.AgentInfo {
	if p == nil {
		return nil
	}
	hs := append([]control.AgentInfo(nil), p.Agents...)
	sort.SliceStable(hs, func(i, j int) bool {
		if hs[i].Online != hs[j].Online {
			return hs[i].Online
		}
		return strings.ToLower(hs[i].Name) < strings.ToLower(hs[j].Name)
	})
	return hs
}

// helping returns the clients an agent is in session with, by name.
func helping(p *control.Presence, agent string) []string {
	var names []string
	if p == nil {
		return nil
	}
	for _, c := range p.Clients {
		if c.InSessionWith == agent {
			names = append(names, c.Name)
		}
	}
	sort.Strings(names)
	return names
}

// drawAgentsPage draws the Agents tab: every agent on the relay with an
// online dot, and whom they are helping right now.
func (a *App) drawAgentsPage(r rl.Rectangle) {
	c := a.ui
	rl.DrawRectangleRec(r, ui.ColPanel)
	c.Text(ui.Bold, ui.SizeHeading, "Agents", r.X+16, r.Y+14, ui.ColText)
	a.addButton("agents/add", "Add agent", tabRegisterAgent, r)
	list := rl.NewRectangle(r.X, r.Y+56, r.Width, r.Height-56)
	rl.DrawLineV(rl.NewVector2(r.X, list.Y), rl.NewVector2(r.X+r.Width, list.Y), ui.ColBorder)
	if a.presence == nil {
		return
	}
	hs := sortedAgents(a.presence)
	if len(hs) == 0 {
		c.Text(ui.Regular, ui.SizeSmall, "No agents on this relay.", list.X+16, list.Y+14, ui.ColMuted)
		return
	}
	off := c.BeginScroll("agents", list, float32(len(hs))*rowH)
	y := list.Y + off
	for _, h := range hs {
		a.drawAgentRow(h, rl.NewRectangle(list.X, y, list.Width, rowH))
		y += rowH
	}
	c.EndScroll()
}

func (a *App) drawAgentRow(h control.AgentInfo, r rl.Rectangle) {
	c := a.ui
	rl.DrawLineV(rl.NewVector2(r.X+16, r.Y+r.Height), rl.NewVector2(r.X+r.Width, r.Y+r.Height), ui.ColHover)
	x := r.X + 16
	nameCol := ui.ColText
	if h.Online {
		rl.DrawCircleV(rl.NewVector2(x+4, r.Y+17), 4, ui.ColOK)
	} else {
		nameCol = ui.ColMuted
		rl.DrawCircleLines(int32(x+4), int32(r.Y+17), 4, ui.ColBorder)
	}
	x += 16
	w := r.X + r.Width - x - 16
	name := h.Name
	if h.Name == a.me {
		name += " (you)"
	}
	c.Text(ui.Regular, ui.SizeBody, c.Ellipsize(ui.Regular, ui.SizeBody, name, w), x, r.Y+8, nameCol)
	sub, subCol := h.Fingerprint, ui.ColMuted
	switch names := helping(a.presence, h.Name); {
	case len(names) > 0:
		sub, subCol = "Helping "+strings.Join(names, ", "), ui.ColAccent
	case !h.Online:
		sub = "Offline · " + h.Fingerprint
	}
	c.Text(ui.Regular, ui.SizeSmall, c.Ellipsize(ui.Regular, ui.SizeSmall, sub, w), x, r.Y+30, subCol)
}
