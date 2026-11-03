// Package app is the computer's logic between the window and the server
// connection: which screen to show (the server address with Connect, the key
// to send, waiting for a techie with Disconnect), the stored configuration,
// Remove key and the update offer. It has
// no GUI dependencies; the window renders the View it is given and forwards
// button presses.
package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/helpbuoy/client-windows/config"
	"github.com/mevdschee/helpbuoy/client-windows/session"
	"github.com/mevdschee/helpbuoy/proto/appkey"
	"github.com/mevdschee/helpbuoy/proto/control"
	"github.com/mevdschee/helpbuoy/proto/serveraddr"
	"github.com/mevdschee/helpbuoy/proto/update"
)

// Screen is one of the window's layouts.
type Screen int

// Screens.
const (
	ScreenAddress  Screen = iota // server address and Connect; always the first screen
	ScreenKey                    // connected, key not registered: send this key to a techie
	ScreenMain                   // registered: waiting for a techie to connect, Disconnect
	ScreenKeyError               // the app key exists but can't be used
)

func (s Screen) String() string {
	return [...]string{"address", "key", "main", "key-error"}[s]
}

// View is everything the window shows.
type View struct {
	Screen Screen

	// ScreenAddress
	Address    string // prefilled or current address
	Connecting bool   // a Connect attempt is running
	// CheckingUpdate is set while the start looks for a newer release
	// before connecting to Address (Connecting is set too).
	CheckingUpdate bool
	// Error is an inline error (ScreenAddress, ScreenKeyError).
	Error string

	// ScreenKey
	ServerFingerprint string
	KeyLine           string
	KeyFingerprint    string
	// Notice is a short, calm line for a temporary condition, like a lost
	// connection being retried (ScreenKey, ScreenMain). Empty most of the time.
	Notice string

	// ScreenMain
	KeyComment string // comment of this PC's key, user@machine
	Ready      bool   // connected and registered: techies can connect
	Asking     bool   // a techie wants to connect; the user is being asked
	InSession  bool   // the window is minimized while true
	// ServerKeyChange is set when the server showed another key than the
	// pinned one; the computer stopped connecting (ScreenAddress). The window
	// warns, and TrustServerKey pins the new key if the user accepts it.
	ServerKeyChange *session.HostKeyChangedError

	// ScreenKeyError
	KeyDir string

	// Title and Instruction are the server's title and instruction line
	// (e.g. "Example Inc", "Call +1 234 567 890 for support"), or the
	// generic defaults when the server has none or is not known.
	Title       string
	Instruction string

	// Update is a newer release to offer. It is only set while the computer
	// is idle (address screen, or waiting for a techie), so the offer never
	// interrupts the user. At the start it is offered before connecting:
	// the window calls DismissUpdate when the user declines, which connects.
	Update *update.Release
}

// UI is the window, as the controller sees it.
type UI interface {
	// Render shows v. It is called from any goroutine, one call at a time,
	// and must not wait for the controller.
	Render(v View)
	// ConfirmTechie asks whether techie h may take control. oldFingerprint
	// is set when that techie showed a different key last time. It may
	// block; when ctx ends the question is moot and it should return false.
	ConfirmTechie(ctx context.Context, h session.Techie, oldFingerprint string) bool
}

// Deps are the controller's collaborators.
type Deps struct {
	UI    UI
	Store config.Store
	// LoadKey loads the app key, creating it if there is none.
	LoadKey func() (*appkey.Key, error)
	// RemoveKey deletes the key files and config.json.
	RemoveKey func() error
	// NewRunner returns the session runner for a key; nil disables sessions.
	NewRunner func(key *appkey.Key) session.Runner
	// EscHook is passed to session.Options.
	EscHook     func(onEsc func()) (stop func())
	AppVersion  string
	UserMachine string
	// CheckUpdate looks for a newer release; nil never checks. It is called
	// once in the background by Start, which waits up to UpdateWait for it
	// before connecting to the stored address.
	// Errors (offline, the API refusing) are ignored: next start tries again.
	CheckUpdate func(ctx context.Context) (*update.Release, error)
	// UpdateWait is how long Start waits for CheckUpdate before it connects
	// anyway; 0 means DefaultUpdateWait. A later result is offered when the
	// computer is idle.
	UpdateWait time.Duration
	// Tune adjusts the session options, for tests.
	Tune   func(*session.Options)
	Logger *slog.Logger
}

// DefaultUpdateWait is how long the start waits for the update check.
const DefaultUpdateWait = 5 * time.Second

// Controller connects the window, the configuration and the server computer.
// Its methods are meant to be called from the UI thread; they never block on
// the network.
type Controller struct {
	d   Deps
	log *slog.Logger

	mu       sync.Mutex
	cfg      *config.Config
	key      *appkey.Key
	keyErr   error
	addr     string // address of the running computer, or the last one tried
	computer *session.Computer
	gen      int // identifies the running computer; events of older ones are dropped
	st       session.State
	reached  bool       // the running computer got through to the server at least once
	known    bool       // the running computer is registered on its server
	info     *[2]string // title and instruction the running computer's server sent
	lastErr  error      // why the last computer stopped, shown on the address screen
	inputErr string
	closed   bool
	closing  <-chan struct{}

	newRelease    *update.Release // newer release, until offered
	updateStarted bool
	updateCancel  context.CancelFunc
	// pendingAddr is the stored address Start connects to once the update
	// check is done and any offer was declined.
	pendingAddr string
	checking    bool        // Start waits for the update check
	waitTimer   *time.Timer // ends the wait for the update check

	saveMu sync.Mutex

	renderMu   sync.Mutex
	renderNext *View
	renderSig  chan struct{}
}

// New returns a controller. Call Start to show the first screen.
func New(d Deps) *Controller {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	c := &Controller{d: d, log: log, renderSig: make(chan struct{}, 1)}
	go c.renderLoop()
	return c
}

// renderLoop delivers the newest View to the UI, one at a time, so callers
// never block on the UI thread.
func (c *Controller) renderLoop() {
	for range c.renderSig {
		c.renderMu.Lock()
		v := c.renderNext
		c.renderNext = nil
		c.renderMu.Unlock()
		if v != nil {
			c.d.UI.Render(*v)
		}
	}
}

// update computes the View and queues it. Call without c.mu held.
func (c *Controller) update() {
	c.mu.Lock()
	v := c.viewLocked()
	c.mu.Unlock()
	c.renderMu.Lock()
	c.renderNext = &v
	c.renderMu.Unlock()
	select {
	case c.renderSig <- struct{}{}:
	default:
	}
}

// View returns the current View.
func (c *Controller) View() View {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.viewLocked()
}

func (c *Controller) viewLocked() View {
	v := c.screenLocked()
	if v.Screen == ScreenMain {
		v.Title, v.Instruction = c.serverInfoLocked()
		if v.Instruction == "" {
			v.Instruction = control.DefaultInstruction
		}
	}
	idle := v.Screen == ScreenAddress && !v.Connecting || v.Screen == ScreenMain && v.Ready
	if idle {
		v.Update = c.newRelease
	}
	return v
}

// serverInfoLocked returns the title and instruction the server sent on this
// connection; they are never stored.
func (c *Controller) serverInfoLocked() (string, string) {
	if c.info != nil {
		return c.info[0], c.info[1]
	}
	return "", ""
}

// screenLocked computes the View without the update offer.
func (c *Controller) screenLocked() View {
	var v View
	if c.keyErr != nil || c.key == nil {
		v.Screen = ScreenKeyError
		v.Error = describeKeyError(c.keyErr)
		v.KeyDir = c.d.Store.Dir
		return v
	}
	v.KeyLine = c.key.Line
	v.KeyFingerprint = c.key.Fingerprint
	v.KeyComment = c.key.Comment
	v.ServerFingerprint = c.st.ServerFingerprint

	lost := "The connection to the server was lost. Trying again..."
	switch p := c.st.Phase; {
	case c.computer == nil && c.checking:
		v.Screen, v.Address, v.Connecting, v.CheckingUpdate = ScreenAddress, c.pendingAddr, true, true
	case c.computer == nil:
		v.Screen = ScreenAddress
		v.Address = c.addr
		if v.Address == "" {
			v.Address = c.storedAddrLocked()
		}
		switch {
		case c.inputErr != "":
			v.Error = c.inputErr
		case c.lastErr != nil:
			v.Error = session.Describe(c.lastErr)
			var hk *session.HostKeyChangedError
			if errors.As(c.lastErr, &hk) {
				v.ServerKeyChange = hk
			}
		}
	case p == session.PhasePending:
		v.Screen = ScreenKey
	case p == session.PhaseReady:
		v.Screen, v.Ready = ScreenMain, true
	case p == session.PhaseAsking:
		v.Screen, v.Asking, v.Notice = ScreenMain, true, "A techie wants to connect."
	case p == session.PhaseInSession:
		v.Screen, v.InSession = ScreenMain, true
	case !c.reached:
		v.Screen, v.Address, v.Connecting = ScreenAddress, c.addr, true
	case c.known:
		v.Screen, v.Notice = ScreenMain, lost
	default:
		v.Screen, v.Notice = ScreenKey, lost
	}
	return v
}

func describeKeyError(err error) string {
	switch {
	case err == nil:
		return "The key of this PC could not be loaded."
	case errors.Is(err, appkey.ErrDamaged):
		return "The key of this PC can't be used. " + capitalize(err.Error()) + "."
	}
	return "The key of this PC could not be loaded or created: " + err.Error()
}

// Start loads the key and the configuration, checks for a newer release and
// then connects to the last server address. A newer release is offered
// first; it connects when the user declines it (DismissUpdate) or when the
// check takes longer than UpdateWait. Without a stored address it shows the
// empty address screen; the address can only be changed once connecting
// failed or after Disconnect.
func (c *Controller) Start() {
	c.mu.Lock()
	c.loadLocked()
	addr := c.storedAddrLocked()
	if addr == "" || c.key == nil || c.keyErr != nil {
		addr = ""
	}
	if c.startUpdateCheckLocked() && addr != "" {
		c.pendingAddr, c.checking = addr, true
		wait := c.d.UpdateWait
		if wait <= 0 {
			wait = DefaultUpdateWait
		}
		c.waitTimer = time.AfterFunc(wait, func() {
			c.mu.Lock()
			if c.checking {
				c.log.Info("update check is slow; connecting")
				c.checking = false
				c.connectPendingLocked()
			}
			c.mu.Unlock()
			c.update()
		})
	} else if addr != "" {
		c.startComputerLocked(addr)
	}
	c.mu.Unlock()
	c.update()
}

// connectPendingLocked connects to the address Start held back, unless the
// user connected or closed the app meanwhile.
func (c *Controller) connectPendingLocked() {
	addr := c.pendingAddr
	c.pendingAddr = ""
	if addr != "" && !c.closed && c.computer == nil && c.key != nil {
		c.startComputerLocked(addr)
	}
}

// storedAddrLocked is the address to connect to or prefill: the last one
// that reached a server, else the server this PC is registered on.
func (c *Controller) storedAddrLocked() string {
	if c.cfg.LastAddress != "" {
		return c.cfg.LastAddress
	}
	return c.cfg.ServerAddress
}

// startUpdateCheckLocked runs Deps.CheckUpdate once in the background and
// reports whether it started. When the check ends while Start waits for it,
// it connects, unless there is a release to offer first.
func (c *Controller) startUpdateCheckLocked() bool {
	if c.d.CheckUpdate == nil || c.updateStarted {
		return false
	}
	c.updateStarted = true
	ctx, cancel := context.WithCancel(context.Background())
	c.updateCancel = cancel
	go func() {
		r, err := c.d.CheckUpdate(ctx)
		if err != nil {
			c.log.Info("update check failed", "err", err)
		} else if r != nil {
			c.log.Info("update available", "version", r.Version)
		}
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return
		}
		waited := c.checking
		c.checking = false
		if c.waitTimer != nil {
			c.waitTimer.Stop()
		}
		if err == nil && r != nil {
			c.newRelease = r // offered now; DismissUpdate connects
		} else if waited {
			c.connectPendingLocked()
		}
		c.mu.Unlock()
		c.update()
	}()
	return true
}

func (c *Controller) loadLocked() {
	c.key, c.keyErr = c.d.LoadKey()
	if c.keyErr != nil {
		c.log.Error("app key", "err", c.keyErr)
	}
	cfg, err := c.d.Store.Load()
	if err != nil {
		c.log.Error("configuration is unreadable; starting over", "err", err)
		cfg = &config.Config{}
	}
	c.cfg = cfg
}

// startComputerLocked replaces the running computer. The server's host key and
// the techie pins are the stored ones when addr is the stored server;
// otherwise the host key is pinned on first use.
func (c *Controller) startComputerLocked(addr string) {
	c.stopComputerLocked()
	c.gen++
	c.addr = addr
	c.reached, c.known, c.lastErr = false, false, nil
	c.info = nil
	c.st = session.State{Phase: session.PhaseConnecting}
	var hostKey string
	var techies map[string]string
	if addr == c.cfg.ServerAddress {
		hostKey, techies = c.cfg.ServerHostKey, c.cfg.Clone().KnownTechies
	}
	c.computer = c.newComputerLocked(addr, hostKey, techies, c.gen)
	c.computer.Start()
}

// newComputerLocked returns a server computer whose events carry gen.
func (c *Controller) newComputerLocked(addr, hostKey string, techies map[string]string, gen int) *session.Computer {
	var runner session.Runner
	if c.d.NewRunner != nil {
		runner = c.d.NewRunner(c.key)
	}
	opts := session.Options{
		Addr:          addr,
		Signer:        c.key.Signer,
		AppVersion:    c.d.AppVersion,
		UserMachine:   c.d.UserMachine,
		ServerHostKey: hostKey,
		KnownTechies:  techies,
		Handler:       handler{c, gen},
		Runner:        runner,
		EscHook:       c.d.EscHook,
		Logger:        c.log,
	}
	if c.d.Tune != nil {
		c.d.Tune(&opts)
	}
	return session.New(opts)
}

// stopComputerLocked stops the running computer.
func (c *Controller) stopComputerLocked() {
	if c.computer != nil {
		c.computer.Close()
		c.computer = nil
	}
	c.st = session.State{}
}

// Connect is the Connect button on the address screen.
func (c *Controller) Connect(text string) {
	c.mu.Lock()
	if c.closed || c.key == nil || c.computer != nil || c.checking {
		c.mu.Unlock()
		return
	}
	a, err := serveraddr.Parse(text)
	if err != nil {
		c.addr = text
		c.lastErr = nil
		c.inputErr = capitalize(err.Error()) + "."
	} else {
		c.inputErr = ""
		c.pendingAddr, c.checking = "", false
		c.startComputerLocked(a.String())
	}
	c.mu.Unlock()
	c.update()
}

// Disconnect is the Disconnect button (and the "use a different server
// address" link): it leaves the server and goes back to the address screen.
// A running session ends with it.
func (c *Controller) Disconnect() {
	c.mu.Lock()
	if c.computer == nil {
		c.mu.Unlock()
		return
	}
	c.log.Info("disconnected by the user", "server", c.addr)
	c.stopComputerLocked()
	c.lastErr = nil
	c.mu.Unlock()
	c.update()
}

// EndSession is the End session button shown during a session: it ends
// the session and the computer keeps waiting for a techie.
func (c *Controller) EndSession() {
	c.mu.Lock()
	cl := c.computer
	c.mu.Unlock()
	if cl != nil {
		cl.EndSession()
	}
}

// TrustServerKey pins the server's new host key after the user accepted the
// change warning for newFingerprint, and connects again.
func (c *Controller) TrustServerKey(newFingerprint string) {
	c.mu.Lock()
	var hk *session.HostKeyChangedError
	if c.computer != nil || !errors.As(c.lastErr, &hk) || hk.NewFingerprint != newFingerprint || c.addr != c.cfg.ServerAddress {
		c.mu.Unlock()
		return
	}
	c.log.Warn("server host key change accepted by the user", "old", hk.OldFingerprint, "new", hk.NewFingerprint)
	c.cfg.ServerHostKey = hk.NewKeyLine
	c.startComputerLocked(c.addr)
	c.mu.Unlock()
	c.save()
	c.update()
}

// RemoveKey deletes the key and the configuration, then creates a new key
// and goes back to the address screen. Call only after the user confirmed.
func (c *Controller) RemoveKey() error {
	c.mu.Lock()
	c.stopComputerLocked()
	err := c.d.RemoveKey()
	if err == nil {
		c.addr, c.inputErr, c.lastErr = "", "", nil
		c.pendingAddr, c.checking = "", false
		c.loadLocked()
	}
	c.mu.Unlock()
	c.update()
	return err
}

// Close ends any session and disconnects. It does not wait; see WaitClosed.
func (c *Controller) Close() {
	c.mu.Lock()
	c.closed = true
	if c.updateCancel != nil {
		c.updateCancel()
	}
	if c.waitTimer != nil {
		c.waitTimer.Stop()
	}
	c.pendingAddr, c.checking = "", false
	if c.computer != nil {
		c.closing = c.computer.Done()
	}
	c.stopComputerLocked()
	c.mu.Unlock()
}

// WaitClosed waits up to d for the computer stopped by Close to finish, so
// the server sees a clean disconnect before the process exits.
func (c *Controller) WaitClosed(d time.Duration) {
	c.mu.Lock()
	done := c.closing
	c.mu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(d):
	}
}

// Version is the app version, for the update offer.
func (c *Controller) Version() string { return c.d.AppVersion }

// DismissUpdate is called when the user declined View.Update, or its
// installation failed, so it is not offered again in this run. When the
// start held back connecting for the offer, it connects now.
func (c *Controller) DismissUpdate() {
	c.mu.Lock()
	c.newRelease = nil
	c.connectPendingLocked()
	c.mu.Unlock()
	c.update()
}

// handler receives the events of one computer generation.
type handler struct {
	c   *Controller
	gen int
}

func (h handler) StateChanged(s session.State) {
	c := h.c
	c.mu.Lock()
	if c.gen != h.gen || c.computer == nil {
		c.mu.Unlock()
		return
	}
	c.st = s
	var save bool
	switch s.Phase {
	case session.PhaseStopped:
		// The computer gave up (the server could not be reached at first, or
		// refused this PC): back to the address screen with the reason.
		c.computer = nil
		c.lastErr = s.Err
	case session.PhasePending, session.PhaseReady:
		c.known = s.Phase == session.PhaseReady
		if !c.reached {
			c.reached = true
			if c.cfg.LastAddress != c.addr {
				c.cfg.LastAddress = c.addr
				save = true
			}
		}
	}
	c.mu.Unlock()
	if save {
		c.save()
	}
	c.update()
}

// Registered saves the server this PC is registered on. A different server
// than the stored one replaces it, and the techie pins of the old server are
// forgotten: techie names are only unique within one server.
func (h handler) Registered(serverHostKey string) {
	c := h.c
	c.mu.Lock()
	if c.gen != h.gen || c.computer == nil {
		c.mu.Unlock()
		return
	}
	c.known = true
	if c.cfg.ServerAddress != c.addr {
		c.cfg.KnownTechies = nil
	}
	changed := c.cfg.ServerAddress != c.addr || c.cfg.ServerHostKey != serverHostKey
	c.cfg.ServerAddress = c.addr
	c.cfg.ServerHostKey = serverHostKey
	c.mu.Unlock()
	if changed {
		c.log.Info("registered on the server", "server", c.addr)
		c.save()
	}
	c.update()
}

func (h handler) TechiePinned(name, line string) {
	c := h.c
	c.mu.Lock()
	if c.gen != h.gen {
		c.mu.Unlock()
		return
	}
	if c.cfg.KnownTechies == nil {
		c.cfg.KnownTechies = make(map[string]string)
	}
	c.cfg.KnownTechies[name] = line
	c.mu.Unlock()
	c.save()
}

// ServerInfo shows the server's title and instruction.
func (h handler) ServerInfo(title, instruction string) {
	c := h.c
	c.mu.Lock()
	if c.gen != h.gen || c.computer == nil {
		c.mu.Unlock()
		return
	}
	c.info = &[2]string{title, instruction}
	c.mu.Unlock()
	c.update()
}

func (h handler) ConfirmTechie(ctx context.Context, techie session.Techie, oldFP string) bool {
	return h.c.d.UI.ConfirmTechie(ctx, techie, oldFP)
}

// save writes the current configuration. Saves are serialized and each
// writes the newest state, so an older snapshot never overwrites a newer one.
func (c *Controller) save() {
	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	c.mu.Lock()
	cfg := c.cfg.Clone()
	c.mu.Unlock()
	if err := c.d.Store.Save(cfg); err != nil {
		c.log.Error("saving the configuration", "err", err)
	}
}

// fingerprint returns the SHA256 fingerprint of an authorized_keys line, or
// "" if it can't be parsed.
func fingerprint(line string) string {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return ""
	}
	return ssh.FingerprintSHA256(k)
}

func capitalize(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}
