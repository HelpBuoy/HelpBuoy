// Package views holds the desk app's screens: a tab bar that is always
// there, with the fixed Clients, Agents and Relay tabs (and an open Register client or Register agent tab before Relay) followed by
// one tab per open session (PLAN.md section 7).
//
// App owns all UI state and runs on the main OS thread. Network goroutines
// (relayconn, session, localrelay) hand results over through channels and
// snapshots that Frame drains once per frame.
package views

import (
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"
	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/helpbuoy/desk/config"
	"github.com/mevdschee/helpbuoy/desk/identity"
	"github.com/mevdschee/helpbuoy/desk/localrelay"
	"github.com/mevdschee/helpbuoy/desk/relayconn"
	"github.com/mevdschee/helpbuoy/desk/session"
	"github.com/mevdschee/helpbuoy/desk/ui"
	"github.com/mevdschee/helpbuoy/proto/appkey"
	"github.com/mevdschee/helpbuoy/proto/control"
	"github.com/mevdschee/helpbuoy/proto/relayaddr"
)

// Options configure the app.
type Options struct {
	DataDir string // "" means the per-user data directory
	Version string
	Demo    bool   // fake relay data, no network, nothing written to disk
	Page    string // start page: "register", "relay" or "session" (demo)
	Icon    []byte // the app icon (PNG), shown on session tabs
}

// Layout sizes in logical pixels.
const (
	tabBarH      = 40
	fixedTabW    = 144 // Clients, Agents, Relay
	registerTabW = 180 // Register client or Register agent, with a close button
	sessionTabW  = 216
	toolbarH     = 44
	idleFPS      = 10
	activeHold   = 1500 * time.Millisecond // stay fast this long after input
	// openTimeout is how long a Connect waits for the user to allow it and
	// the session channel to arrive. The relay gives up after 1 minute.
	openTimeout = 75 * time.Second
)

// backend is what the views need from the relay connection; the demo mode
// has its own.
type backend interface {
	RegisterClient(line, name string) <-chan error
	RegisterAgent(line, name string) <-chan error
	RenameClient(fingerprint, name string) <-chan error
	UnregisterClient(fingerprint string) <-chan error
	Connect(fingerprint, requestID string) <-chan error
	SetRelayInfo(title, instruction string) <-chan error
}

type tabKind int

// Clients, Agents and Relay are always there, in this order. At most one
// Register client or Register agent tab sits at registerSlot, between Agents
// and Relay, while it is open. Session tabs follow Relay.
const (
	tabClients tabKind = iota
	tabAgents
	tabRegister
	tabRegisterAgent
	tabRelay
	tabSession
)

// registerSlot is the tab bar position of the Register client or Register
// agent tab.
const registerSlot = 2

type tab struct {
	kind tabKind
	sess *sessionTab
}

// pending is a command waiting for its result.
type pending struct {
	res    <-chan error
	onDone func(error)
}

// note is a brief message on a client row.
type note struct {
	text  string
	until time.Time
}

// App is the whole agent UI.
type App struct {
	opts Options
	ui   *ui.Context
	dir  string

	cfg    *config.Config
	cfgErr error

	key        *appkey.Key
	keyErr     error
	keyRemoved bool
	quit       bool

	conn     *relayconn.Conn
	backend  backend
	status   relayconn.StatusEvent
	hostKey  ssh.PublicKey
	presence *control.Presence
	me       string             // our agent name from Hello
	info     *control.RelayInfo // the relay's title and instruction line, nil until received

	local      *localrelay.Relay
	localAdded int

	cs        connectScreen
	addr      relayaddr.Addr // relay address of the current Connect
	localWait bool           // the local relay is starting; the connection follows
	reached   bool           // the current connection has been online
	notAgent  bool           // the relay refused this key before it was ever online

	cmds          []pending
	opened        map[string]time.Time // request ids this agent sent Connect for, and when
	opening       map[string]bool      // client fingerprints with a Connect in flight
	waiting       map[string]*waitInfo // by client fingerprint: accepted Connects, until the session starts
	notes         map[string]note      // by client fingerprint
	confirmRemove string
	offlineOpen   bool
	rename        renameState

	tabs       []*tab
	active     int          // index into tabs
	reg        registerPage // Register client
	regAgent   registerPage // Register agent
	infoForm   infoForm     // title and instruction on the Relay tab
	confirmKey bool         // the Remove key confirmation on the damaged key screen
	pageHeight map[tabKind]float32

	lastInput  time.Time
	fps        int32
	fullscreen bool
	windowed   windowGeom // window position and size before fullscreen
	keysTo     *sessionTab
	demo       *demo
}

// MinWidth and MinHeight are the smallest window size, in window units.
const (
	MinWidth  = 760
	MinHeight = 480
)

// windowGeom is a window position and size, in window units.
type windowGeom struct {
	x, y, w, h int32
}

// New creates the app. Call it after rl.InitWindow.
func New(opts Options) *App {
	a := &App{
		opts:       opts,
		ui:         ui.New(),
		tabs:       []*tab{{kind: tabClients}, {kind: tabAgents}, {kind: tabRelay}},
		opened:     map[string]time.Time{},
		opening:    map[string]bool{},
		waiting:    map[string]*waitInfo{},
		notes:      map[string]note{},
		pageHeight: map[tabKind]float32{},
		cfg:        &config.Config{},
	}
	a.ui.SetIcon(opts.Icon)
	a.reg.init(false)
	a.regAgent.init(true)
	a.infoForm.init()
	a.cs.init()
	if opts.Demo {
		a.demo = newDemo(a)
	} else {
		a.load()
	}
	if ad, err := relayaddr.Parse(a.cfg.RelayAddr); err == nil {
		a.cs.addr.Edit.SetText(fieldText(ad))
	}
	switch opts.Page {
	case "register":
		a.openRegister(tabRegister)
	case "relay":
		a.openPage(tabRelay)
	case "session":
		if a.demo != nil {
			a.demo.connectFirst()
		}
	}
	return a
}

// load reads the data directory: config and key. Nothing connects until
// the user presses Connect.
func (a *App) load() {
	dir := a.opts.DataDir
	if dir == "" {
		var err error
		if dir, err = identity.Dir(); err != nil {
			a.keyErr = err
			return
		}
	}
	a.dir = dir
	a.cfg, a.cfgErr = config.Load(dir)
	a.key, _, a.keyErr = identity.Load(dir)
	if a.keyErr != nil {
		a.key = nil
	}
}

// saveConfig writes the config, except in demo mode.
func (a *App) saveConfig() {
	if a.demo != nil || a.dir == "" {
		return
	}
	if err := a.cfg.Save(a.dir); err != nil {
		a.cfgErr = err
	} else {
		a.cfgErr = nil
	}
}

// Quit reports whether the app wants to exit.
func (a *App) Quit() bool { return a.quit }

// Shutdown ends all sessions, the relay connection and the local relay.
func (a *App) Shutdown() {
	for _, t := range a.tabs {
		if t.sess != nil {
			t.sess.close()
		}
	}
	if a.conn != nil {
		a.conn.Close()
	}
	if a.local != nil {
		a.local.Stop()
	}
	if a.demo != nil {
		a.demo.stop()
	}
	a.ui.Unload()
}

// Frame drains the background channels, then draws one frame.
func (a *App) Frame() {
	a.ui.Begin()
	a.rename.beginFrame(a.ui)
	if a.ui.Interacted {
		a.lastInput = a.ui.Now
	}
	a.drain()
	a.updateSessions()

	rl.BeginDrawing()
	rl.ClearBackground(ui.ColBackground)
	switch {
	case a.keyErr != nil || a.keyRemoved:
		a.drawKeyProblem()
	case a.demo == nil && !a.reached:
		a.drawStart()
	case a.fullscreen && a.activeSession() != nil:
		w, h := ui.WindowSize()
		a.drawSession(a.activeSession(), rl.NewRectangle(0, 0, w, h))
	default:
		if a.fullscreen {
			a.setFullscreen(false)
		}
		a.drawWorkArea()
	}
	if a.keysTo != nil && a.keysTo != a.activeSession() {
		a.keysTo.release() // the session lost the keyboard: lift held keys
		a.keysTo = nil
	}
	a.ui.End()
	rl.EndDrawing()
	a.adjustFPS()
}

// adjustFPS runs at the monitor's refresh rate while a session is open, the
// user is interacting, or the mouse is over the window, and at idleFPS
// otherwise. raylib sees button state only once per frame, so a click whose
// press and release both fall between two idle frames would be lost; keeping
// the full rate while the cursor is inside the window prevents that.
func (a *App) adjustFPS() {
	want := int32(idleFPS)
	if a.sessionCount() > 0 || a.ui.Now.Sub(a.lastInput) < activeHold || rl.IsCursorOnScreen() {
		want = int32(rl.GetMonitorRefreshRate(rl.GetCurrentMonitor()))
		if want <= 0 {
			want = 60
		}
	}
	if want != a.fps {
		rl.SetTargetFPS(want)
		a.fps = want
	}
}

// drain handles everything that arrived from background goroutines.
func (a *App) drain() {
	// handleEvent may disconnect, so the connection is checked every time.
	for a.conn != nil {
		select {
		case ev := <-a.conn.Events():
			a.handleEvent(ev)
			continue
		default:
		}
		break
	}
	a.pollLocal()
	if a.demo != nil {
		a.demo.frame()
	}
	kept := a.cmds[:0]
	for _, p := range a.cmds {
		select {
		case err := <-p.res:
			p.onDone(err)
		default:
			kept = append(kept, p)
		}
	}
	a.cmds = kept
	if a.local != nil {
		if n := a.local.Status().AgentAdded; n != a.localAdded {
			a.localAdded = n
			if a.conn != nil {
				a.conn.Reconnect()
			}
		}
	}
	for id, at := range a.opened {
		if a.ui.Now.Sub(at) > openTimeout {
			delete(a.opened, id) // no session channel came; a late one is refused
		}
	}
	for fp, w := range a.waiting {
		if _, ok := a.opened[w.id]; !ok {
			delete(a.waiting, fp)
		}
	}
	for fp, n := range a.notes {
		if a.ui.Now.After(n.until) {
			delete(a.notes, fp)
		}
	}
}

func (a *App) handleEvent(ev relayconn.Event) {
	switch ev := ev.(type) {
	case relayconn.StatusEvent:
		a.status = ev
		if ev.HostKey != nil {
			a.hostKey = ev.HostKey
		}
		if ev.Hello != nil {
			a.me = ev.Hello.Name
		}
		if ev.Status == relayconn.StatusOnline {
			a.reached, a.notAgent = true, false
		} else {
			a.opening = map[string]bool{}
			a.waiting = map[string]*waitInfo{}
			if !a.reached {
				a.statusBeforeOnline(ev)
			}
		}
	case relayconn.HostKeyPinned:
		a.hostKey = ev.Key
		if _, err := a.cfg.Pin(ev.Key); err == nil {
			a.saveConfig()
		}
	case relayconn.PresenceEvent:
		a.setPresence(ev.Presence)
	case relayconn.RelayInfoEvent:
		a.setRelayInfo(ev.Info)
	case relayconn.SessionEvent:
		a.incomingSession(ev.Data, ev.Channel)
	}
}

// waitInfo is a Connect the relay accepted, waiting for the user's answer.
type waitInfo struct {
	id   string
	seen bool // the request showed up in Presence
}

// setPresence stores a snapshot. A Connect whose request went away before
// its session started was refused by the user, or not answered in time.
func (a *App) setPresence(p *control.Presence) {
	a.presence = p
	for _, c := range p.Clients {
		w := a.waiting[c.Fingerprint]
		if w == nil {
			continue
		}
		if c.Request != nil && c.Request.ID == w.id {
			w.seen = true
			continue
		}
		if w.seen {
			delete(a.waiting, c.Fingerprint)
			delete(a.opened, w.id)
			a.notes[c.Fingerprint] = note{text: "Not allowed: the user refused or did not answer", until: time.Now().Add(10 * time.Second)}
		}
	}
}

// incomingSession verifies a session channel from the relay and opens a tab.
func (a *App) incomingSession(d control.AgentSessionData, ch ssh.Channel) {
	_, opened := a.opened[d.RequestID]
	info, err := session.Verify(d, a.presence, opened)
	if err != nil || a.key == nil {
		ch.Close()
		if err != nil {
			a.noteFor(d.ClientName, "Session refused: "+err.Error())
		}
		return
	}
	delete(a.opened, d.RequestID)
	for fp, w := range a.waiting {
		if w.id == d.RequestID {
			delete(a.waiting, fp)
		}
	}
	a.addSession(session.Dial(ch, a.key.Signer, info))
}

// addSession opens a tab for a new session and switches to it.
func (a *App) addSession(s *session.Session) {
	a.tabs = append(a.tabs, &tab{kind: tabSession, sess: newSessionTab(s)})
	a.activate(len(a.tabs) - 1)
}

// updateSessions closes tabs of ended sessions and uploads new pixels.
func (a *App) updateSessions() {
	for i := 0; i < len(a.tabs); i++ {
		st := a.tabs[i].sess
		if st == nil {
			continue
		}
		select {
		case <-st.s.Done():
			if err := st.s.Err(); err != nil && !isEnd(err) {
				a.notes[st.s.Info.Fingerprint] = note{text: "Session failed: " + err.Error(), until: time.Now().Add(8 * time.Second)}
			}
			a.closeTab(i)
			i--
			continue
		default:
		}
		st.upload()
	}
}

// isEnd reports errors that are just the other side going away.
func isEnd(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed)
}

// noteFor shows a brief message on the row of the client with this name.
func (a *App) noteFor(name, text string) {
	if a.presence == nil {
		return
	}
	for _, c := range a.presence.Clients {
		if c.Name == name {
			a.notes[c.Fingerprint] = note{text: text, until: time.Now().Add(8 * time.Second)}
		}
	}
}

func (a *App) sessionCount() int {
	n := 0
	for _, t := range a.tabs {
		if t.sess != nil {
			n++
		}
	}
	return n
}

func (a *App) activeSession() *sessionTab {
	if a.active < 0 || a.active >= len(a.tabs) {
		return nil
	}
	return a.tabs[a.active].sess
}

// sessionFor returns the open session tab for a client fingerprint, or -1.
func (a *App) sessionFor(fp string) int {
	for i, t := range a.tabs {
		if t.sess != nil && t.sess.s.Info.Fingerprint == fp {
			return i
		}
	}
	return -1
}

// sessionName is the client's current name for a session: the name in the
// latest presence, which follows renames, or the name at session start.
func (a *App) sessionName(st *sessionTab) string {
	if a.presence != nil {
		for _, c := range a.presence.Clients {
			if c.Fingerprint == st.s.Info.Fingerprint && c.Name != "" {
				return c.Name
			}
		}
	}
	return st.s.Info.ClientName
}

// resizeToClient resizes the window so the session view shows the remote
// screen at 1:1, clamped to the current monitor, and switches the tab to
// 1:1 (scrolling if the screen did not fit). It does nothing in fullscreen.
func (a *App) resizeToClient(st *sessionTab) {
	w, h := st.screen.Size()
	if a.fullscreen || !st.screen.Ready() || w <= 0 || h <= 0 {
		return
	}
	lw, _ := ui.WindowSize()
	units := float32(1)
	if lw > 0 && rl.GetScreenWidth() > 0 {
		units = float32(rl.GetScreenWidth()) / lw
	}
	mon := rl.GetCurrentMonitor()
	mw, mh := rl.GetMonitorWidth(mon), rl.GetMonitorHeight(mon)
	t := resizeToClient(w, h, dpi(), units, 0, tabBarH+1+toolbarH, mw, mh, MinWidth, MinHeight)
	rl.SetWindowSize(t.w, t.h)
	// Keep the window on its monitor.
	if mw > 0 && mh > 0 {
		mp, wp := rl.GetMonitorPosition(mon), rl.GetWindowPosition()
		x := clampf(wp.X, mp.X+decorW/2, max(mp.X+float32(mw-t.w-decorW/2), mp.X))
		y := clampf(wp.Y, mp.Y+decorH/2, max(mp.Y+float32(mh-t.h-decorH/2), mp.Y))
		if x != wp.X || y != wp.Y {
			rl.SetWindowPosition(int(x), int(y))
		}
	}
	st.mode = modeOne
}

// activate switches to tab i. Leaving a page resets its form.
func (a *App) activate(i int) {
	if i != a.active {
		a.leaveTab(a.active)
		a.ui.Focus = ""
	}
	a.active = i
}

// leaveTab resets the transient state of the page in tab i: half-filled
// fields, results, errors and open confirmations.
func (a *App) leaveTab(i int) {
	if i < 0 || i >= len(a.tabs) {
		return
	}
	switch a.tabs[i].kind {
	case tabClients:
		if a.rename.fp != "" {
			a.endRename()
		}
		a.confirmRemove = ""
	}
}

// resetRegister empties the form of Register tab k; it is only reset when
// its tab closes or is replaced, not when another tab is shown.
func (a *App) resetRegister(k tabKind) {
	if k == tabRegisterAgent {
		a.regAgent.reset()
	} else {
		a.reg.reset()
	}
}

// isRegister reports whether k is the Register client or Register agent tab.
func isRegister(k tabKind) bool { return k == tabRegister || k == tabRegisterAgent }

// registerOpen reports whether the Register tab is there, and which one.
func (a *App) registerOpen() (tabKind, bool) {
	if len(a.tabs) > registerSlot && isRegister(a.tabs[registerSlot].kind) {
		return a.tabs[registerSlot].kind, true
	}
	return 0, false
}

// openRegister shows the Register client or Register agent tab (k) at
// registerSlot. If it is already open it just switches to it; an open
// Register tab of the other kind is replaced.
func (a *App) openRegister(k tabKind) {
	if cur, ok := a.registerOpen(); ok {
		if cur != k {
			a.resetRegister(cur)
			a.tabs[registerSlot] = &tab{kind: k}
		}
	} else {
		a.tabs = append(a.tabs[:registerSlot], append([]*tab{{kind: k}}, a.tabs[registerSlot:]...)...)
		if a.active >= registerSlot {
			a.active++
		}
	}
	a.activate(registerSlot)
}

// closeRegister closes the Register tab and resets its form. When it was
// active, the list it was opened from is shown.
func (a *App) closeRegister() {
	k, ok := a.registerOpen()
	if !ok {
		return
	}
	a.resetRegister(k)
	a.tabs = append(a.tabs[:registerSlot], a.tabs[registerSlot+1:]...)
	switch {
	case a.active == registerSlot:
		a.active = 0
		if k == tabRegisterAgent {
			a.active = 1
		}
		a.ui.Focus = ""
	case a.active > registerSlot:
		a.active--
	}
}

// openPage shows one of the fixed tabs.
func (a *App) openPage(k tabKind) {
	for i, t := range a.tabs {
		if t.kind == k {
			a.activate(i)
			return
		}
	}
}

// closeTab closes a session tab and ends its session. When it was the
// active tab, the client list is shown.
func (a *App) closeTab(i int) {
	if i < 0 || i >= len(a.tabs) || a.tabs[i].kind != tabSession {
		return
	}
	t := a.tabs[i]
	if a.keysTo == t.sess {
		a.keysTo = nil
	}
	t.sess.close()
	a.tabs = append(a.tabs[:i], a.tabs[i+1:]...)
	switch {
	case a.active == i:
		a.active = 0
		a.ui.Focus = ""
	case a.active > i:
		a.active--
	}
}

func (a *App) run(res <-chan error, onDone func(error)) {
	a.cmds = append(a.cmds, pending{res: res, onDone: onDone})
}

func (a *App) online() bool {
	return a.backend != nil && (a.demo != nil || a.status.Status == relayconn.StatusOnline)
}

// relayFingerprint is the fingerprint clients should be shown, or "".
func (a *App) relayFingerprint() string {
	if a.hostKey == nil {
		return ""
	}
	return ssh.FingerprintSHA256(a.hostKey)
}

// setFullscreen switches the window between fullscreen (borderless, the
// active session only) and windowed. Leaving fullscreen restores the window
// position and size from before.
func (a *App) setFullscreen(on bool) {
	if on == a.fullscreen {
		return
	}
	a.fullscreen = on
	if on {
		p := rl.GetWindowPosition()
		a.windowed = windowGeom{int32(p.X), int32(p.Y), int32(rl.GetScreenWidth()), int32(rl.GetScreenHeight())}
		rl.ToggleBorderlessWindowed()
		return
	}
	rl.ToggleBorderlessWindowed()
	if g := a.windowed; g.w > 0 && g.h > 0 {
		rl.SetWindowSize(int(g.w), int(g.h))
		rl.SetWindowPosition(int(g.x), int(g.y))
	}
}

// drawWorkArea draws the tab bar, which is always there, and the active
// tab below it. A session tab shows only the session.
func (a *App) drawWorkArea() {
	w, h := ui.WindowSize()
	bar := rl.NewRectangle(0, 0, w, tabBarH)
	rl.DrawRectangleRec(bar, ui.ColBackground)
	rl.DrawLineV(rl.NewVector2(0, bar.Height), rl.NewVector2(w, bar.Height), ui.ColBorder)
	// Fixed tabs are narrower than session tabs; all shrink together when
	// they do not fit.
	widthOf := func(t *tab) float32 {
		switch {
		case t.kind == tabSession:
			return sessionTabW
		case isRegister(t.kind):
			return registerTabW
		}
		return fixedTabW
	}
	total := float32(0)
	for _, t := range a.tabs {
		total += widthOf(t)
	}
	shrink := float32(1)
	if total > w {
		shrink = w / total
	}
	x := float32(0)
	closeAt := -1
	for i, t := range a.tabs {
		tw := max(widthOf(t)*shrink, 72)
		clicked, closed := a.ui.Tab(fmt.Sprintf("tab/%p", t), rl.NewRectangle(x, 0, tw, tabBarH), a.tabTitle(t), i == a.active, t.kind == tabSession, isRegister(t.kind))
		if clicked {
			a.activate(i)
		}
		if closed {
			closeAt = i
		}
		x += tw
	}
	if closeAt >= 0 {
		if isRegister(a.tabs[closeAt].kind) {
			a.closeRegister()
		} else {
			a.closeTab(closeAt) // ends the session
		}
	}
	content := rl.NewRectangle(0, tabBarH+1, w, h-tabBarH-1)
	switch t := a.tabs[a.active]; t.kind {
	case tabSession:
		a.drawSession(t.sess, content)
	case tabClients:
		a.drawClientsPage(content)
	case tabAgents:
		a.drawAgentsPage(content)
	case tabRegister:
		a.page(tabRegister, content, a.registerPageFor(&a.reg))
	case tabRegisterAgent:
		a.page(tabRegisterAgent, content, a.registerPageFor(&a.regAgent))
	case tabRelay:
		a.page(tabRelay, content, a.drawRelay)
	}
}

func (a *App) tabTitle(t *tab) string {
	switch t.kind {
	case tabClients:
		return "Clients"
	case tabAgents:
		return "Agents"
	case tabRegister:
		return "Register client"
	case tabRegisterAgent:
		return "Register agent"
	case tabRelay:
		return "Relay"
	}
	return a.sessionName(t.sess)
}

// page draws a scrollable page; draw gets the content origin and width and
// returns the bottom y of what it drew.
func (a *App) page(k tabKind, r rl.Rectangle, draw func(x, y, w float32) float32) {
	rl.DrawRectangleRec(r, ui.ColPanel)
	off := a.ui.BeginScroll(fmt.Sprint("page/", k), r, a.pageHeight[k])
	const pad = 32
	w := min(r.Width-2*pad, 680)
	top := r.Y + pad + off
	bottom := draw(r.X+pad, top, w)
	a.ui.EndScroll()
	a.pageHeight[k] = bottom - top + 2*pad
}

// drawKeyProblem replaces the whole window when the key can't be used: a
// centered box with the problem, the key folder and Remove key, like the
// client's damaged key screen.
func (a *App) drawKeyProblem() {
	c := a.ui
	w, h := ui.WindowSize()
	bw := min(w-48, 600)
	bh := max(a.cs.boxH, 200)
	box := rl.NewRectangle((w-bw)/2, max((h-bh)/2, 24), bw, bh)
	rl.DrawRectangleRounded(box, 0.04, 8, ui.ColPanel)
	rl.DrawRectangleRoundedLinesEx(box, 0.04, 8, 1, ui.ColBorder)
	const pad = 32
	x, y, width := box.X+pad, box.Y+pad, box.Width-2*pad
	if a.keyRemoved {
		c.Text(ui.Bold, ui.SizeHeading, "The key was removed", x, y, ui.ColText)
		y += 36
		y += c.Paragraph(ui.Regular, ui.SizeBody, "Start the app again to create a new key. The relay admin, or another agent (Add agent on the Agents tab), then adds the new key.", x, y, width, ui.ColMuted)
	} else {
		c.Text(ui.Bold, ui.SizeHeading, "The agent key can't be used", x, y, ui.ColText)
		y += 36
		y += c.Paragraph(ui.Regular, ui.SizeBody, a.keyErr.Error(), x, y, width, ui.ColError)
		if a.dir != "" {
			y += 10
			y += c.Paragraph(ui.Regular, ui.SizeSmall, "Folder: "+a.dir, x, y, width, ui.ColMuted)
		}
		if errors.Is(a.keyErr, appkey.ErrDamaged) && a.dir != "" {
			y += 12
			y += c.Paragraph(ui.Regular, ui.SizeBody, "The key is never replaced on its own. Remove key deletes it and makes a new one on the next start; the relay admin or another agent must then add the new key.", x, y, width, ui.ColMuted)
			y += 16
			if a.confirmKey {
				if c.Button("keyproblem/confirm", rl.NewRectangle(x, y, 160, 36), "Remove key", ui.Danger, false) {
					if err := identity.Remove(a.dir); err != nil {
						a.keyErr = err
					} else {
						a.keyErr, a.keyRemoved = nil, true
					}
					a.confirmKey = false
				}
				if c.Button("keyproblem/cancel", rl.NewRectangle(x+172, y, 100, 36), "Cancel", ui.Normal, false) {
					a.confirmKey = false
				}
			} else if c.Button("keyproblem/remove", rl.NewRectangle(x, y, 160, 36), "Remove key…", ui.Normal, false) {
				a.confirmKey = true
			}
			y += 36
		}
	}
	y += 24
	if c.Button("keyproblem/quit", rl.NewRectangle(x, y, 100, 36), "Quit", ui.Primary, false) {
		a.quit = true
	}
	y += 36
	a.cs.boxH = y - box.Y + pad
}
