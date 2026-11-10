package views

import (
	"errors"
	"net/netip"
	"strconv"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/helpbuoy/helpbuoy/desk/config"
	"github.com/helpbuoy/helpbuoy/desk/reach"
	"github.com/helpbuoy/helpbuoy/desk/ui"
	"github.com/helpbuoy/helpbuoy/proto/i18n"
)

// alwaysOnURL is the guide page for a server that does not run on the
// techie's own PC.
const alwaysOnURL = "https://helpbuoy.com/docs/always-on-server/"

// Why a port forward on the router can't work.
const (
	noProblem = iota
	problemNoIPv4
	problemCGNAT
)

type lanResult struct {
	addr  netip.AddrPort
	err   error
	noIP4 bool // no IPv4 route to the internet
}

// giveOut is the address in the local network on the Server tab while the
// server runs on this computer (issue #17). Finding it contacts nobody.
type giveOut struct {
	lan     netip.AddrPort
	lanErr  string
	lanWait <-chan lanResult
	problem int // noProblem, problemNoIPv4 or problemCGNAT

	copiedAt time.Time
}

// localShown reports whether the Server tab shows the address to give out:
// for the server inside this app, and in the demo.
func (a *App) localShown() bool { return a.demo != nil || config.IsLocal(a.addr) }

// startLAN looks for the address in the local network once the local server
// listens.
func (a *App) startLAN(port int) {
	ch := make(chan lanResult, 1)
	a.give = giveOut{lanWait: ch}
	go func() {
		ap, err := reach.LANAddr(port)
		ch <- lanResult{addr: ap, err: err, noIP4: !reach.DefaultRouteIP().IsValid()}
	}()
}

// pollGiveOut takes the result of the lookup, once per frame.
func (a *App) pollGiveOut() {
	g := &a.give
	select {
	case res := <-g.lanWait:
		g.lanWait = nil
		g.lan = res.addr
		switch {
		case errors.Is(res.err, reach.ErrNoLAN):
			g.lanErr = i18n.Trans("This computer is not connected to a local network.")
		case res.err != nil:
			g.lanErr = capitalize(res.err.Error()) + "."
		}
		switch {
		case res.noIP4:
			g.problem = problemNoIPv4
		case g.lan.IsValid() && reach.IsCGNAT(g.lan.Addr()):
			g.problem = problemCGNAT
		}
	default:
	}
}

// drawGiveOut draws the address in the local network with Copy, and what to
// give out. It returns the y below.
func (a *App) drawGiveOut(x, y, w float32) float32 {
	c := a.ui
	g := &a.give
	port := strconv.Itoa(a.addr.Port)
	y = label(c, i18n.Trans("Address in your network"), x, y)
	switch {
	case g.lan.IsValid():
		addr := g.lan.String()
		c.Text(ui.Mono, ui.SizeBody, addr, x, y+6, ui.ColText)
		bx := x + c.Measure(ui.Mono, ui.SizeBody, addr).X + 14
		copyL := i18n.Trans("Copy")
		bw := a.btnW(copyL, 70)
		if c.Button("server/copylan", rl.NewRectangle(bx, y, bw, 30), copyL, ui.Normal, false) {
			rl.SetClipboardText(addr)
			g.copiedAt = c.Now
		}
		if c.Now.Sub(g.copiedAt) < 2*time.Second {
			c.TextIn(ui.Regular, ui.SizeSmall, i18n.Trans("Copied"), rl.NewRectangle(bx+bw+12, y, 100, 30), ui.ColOK)
		}
		y += 38
	case g.lanWait != nil:
		c.Text(ui.Regular, ui.SizeBody, i18n.Trans("Looking for it…"), x, y+6, ui.ColMuted)
		y += 34
	default:
		y += c.Paragraph(ui.Regular, ui.SizeSmall, g.lanErr, x, y, w, ui.ColWarn)
		y += 10
	}
	y += 4
	y += c.Paragraph(ui.Regular, ui.SizeSmall, i18n.Trans("Forward TCP port {0} on your router to this address. Then give the people you help and the techies you add your public address, or a dynamic DNS name that points to it, with :{0} at the end, for example helpme.example.com:{0}. On the same network this address works too.", port), x, y, w, ui.ColMuted)
	y += 10

	if g.problem != noProblem {
		text := i18n.Trans("This internet connection shares its public address with other customers of your provider (carrier-grade NAT), so a port forward on your router can't work.")
		if g.problem == problemNoIPv4 {
			text = i18n.Trans("This computer has no IPv4 internet connection, so a port forward on your router can't work.")
		}
		y += c.Paragraph(ui.Regular, ui.SizeSmall, text, x, y, w, ui.ColWarn)
		y += 4
		if c.Link("server/alwayson", x, y, i18n.Trans("Run the server on an always-on machine instead"), false) {
			rl.OpenURL(alwaysOnURL)
		}
		y += 28
	}
	return y + 12
}
