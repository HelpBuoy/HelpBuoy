// Package app is the computer's logic between the window and the server
// connection: which screen to show (the server address with Connect, the key
// to send, waiting for a techie with Disconnect), the stored configuration,
// the lock to a primary server (PLAN.md 12), Remove key and the update check. It has
// no GUI dependencies; the window renders the View it is given and forwards
// button presses.
package app

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/client-windows/config"
	"github.com/helpbuoy/helpbuoy/client-windows/session"
	"github.com/helpbuoy/helpbuoy/proto/appkey"
	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/proto/i18n"
	"github.com/helpbuoy/helpbuoy/proto/serveraddr"
	"github.com/helpbuoy/helpbuoy/proto/servertext"
	"github.com/helpbuoy/helpbuoy/proto/sysproxy"
	"github.com/helpbuoy/helpbuoy/proto/transport"
	"github.com/helpbuoy/helpbuoy/proto/update"
)

// Screen is one of the window's layouts.
type Screen int

// Screens.
const (
	ScreenAddress  Screen = iota // server address and Connect; always the first screen
	ScreenKey                    // connected, key not registered: send this key to a techie
	ScreenMain                   // registered: waiting for a techie to connect, Disconnect
	ScreenKeyError               // the app key exists but can't be used
	// ScreenWarning is the warning before the key screen of a server this PC
	// never warned about (PLAN.md 6.2); Continue goes on to ScreenKey.
	ScreenWarning
)

func (s Screen) String() string {
	return [...]string{"address", "key", "main", "key-error", "warning"}[s]
}

// warnDelay is how long the first connection warning shows before Continue
// works. Tests shorten it.
var warnDelay = 5 * time.Second

// View is everything the window shows.
type View struct {
	Screen Screen

	// ScreenAddress
	Address    string // prefilled or current address
	Connecting bool   // a Connect attempt is running
	// Error is an inline error (ScreenAddress, ScreenKeyError).
	Error string
	// LockedTo is the company (or address) of the primary server this PC is
	// locked to, "" while unlocked. The address screen says that other
	// servers need its approval.
	LockedTo string
	// UseSystemProxy is the "Use system proxy settings" box under the
	// address field.
	UseSystemProxy bool

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

	// ScreenWarning: WarnWait is the number of seconds before Continue
	// works; 0 means it works now.
	WarnWait int

	// Who the user is waiting for (ScreenMain, PLAN.md 12.4): the address
	// this PC is connected to, and the company name, phone number and
	// description the server gave, cleaned for display. Company, Phone and
	// Description are empty until the server sent them.
	ServerAddress string
	Company       string
	Phone         string
	Description   string
	// Instruction is the line below them (ScreenMain).
	Instruction string
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
	// ConfirmLock asks whether to lock this PC to the server of company
	// (PLAN.md 12.1), with Lock and Not now. It may block; when ctx ends the
	// question is moot and it should return false.
	ConfirmLock(ctx context.Context, company string) bool
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
	// CheckUpdate looks for a newer release, or returns nil when there is
	// none; nil never finds one. It only runs when the user clicks the
	// update link on the waiting screen (LatestRelease).
	CheckUpdate func(ctx context.Context) (*update.Release, error)
	// Probe looks up the host key of a server; nil uses
	// session.ProbeHostKey. Tests replace it.
	Probe func(ctx context.Context, addr string) (ssh.PublicKey, error)
	// Tune adjusts the session options, for tests.
	Tune   func(*session.Options)
	Logger *slog.Logger
}

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
	reached  bool                // the running computer got through to the server at least once
	known    bool                // the running computer is registered on its server
	info     *control.ServerInfo // who runs the running computer's server, cleaned; nil until sent
	// approved is the approved list the running computer's server sent
	// (PLAN.md 12), kept for when the user locks to it.
	approved []control.ApprovedServer
	// listing is the server the running computer, connected to the primary
	// server, asks it to approve; nil if none.
	listing *control.ApprovedServer
	// probing is set while a locked PC looks up the host key of a server
	// that is not approved; stopProbe cancels it.
	probing   bool
	stopProbe context.CancelFunc
	// lockAsked is set once the lock question was asked on the running
	// computer's connection; stopLock ends the question. lockOpen is closed
	// once it is answered, nil while it is not open.
	lockAsked bool
	stopLock  context.CancelFunc
	lockOpen  chan struct{}
	// techieAsking is set while the user is asked about a techie. The lock
	// question and the techie question are never open at the same time:
	// whichever comes second waits for the first.
	techieAsking bool
	lastErr      error // why the last computer stopped, shown on the address screen
	inputErr     string
	warnFrom     time.Time // when the first connection warning was first shown; zero if not shown
	closed       bool
	closing      <-chan struct{}

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
		v.ServerAddress = c.addr
		if c.info != nil {
			v.Company, v.Phone, v.Description = c.info.Company, c.info.Phone, c.info.Description
		}
		v.Instruction = i18n.Trans("A techie can now ask to connect. You will be asked first, " +
			"and pressing Esc always takes back control.")
	}
	v.UseSystemProxy = !c.cfg.NoSystemProxy
	if l := c.cfg.Lock; l != nil {
		v.LockedTo = l.Company
		if v.LockedTo == "" {
			v.LockedTo = l.Address
		}
	}
	return v
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

	lost := i18n.Trans("The connection to the server was lost. Trying again...")
	switch p := c.st.Phase; {
	case c.computer == nil:
		v.Screen = ScreenAddress
		v.Address = c.addr
		v.Connecting = c.probing
		if v.Address == "" {
			v.Address = c.storedAddrLocked()
		}
		switch {
		case c.probing:
		case c.inputErr != "":
			v.Error = c.inputErr
		case c.lastErr != nil:
			v.Error = session.Describe(c.lastErr)
			var hk *session.HostKeyChangedError
			if errors.As(c.lastErr, &hk) && c.mayTrustLocked() {
				v.ServerKeyChange = hk
			}
		}
	case p == session.PhasePending && !c.warnedLocked(c.st.ServerFingerprint):
		v.Screen = ScreenWarning
		v.Address = c.addr
		if c.warnFrom.IsZero() {
			c.warnFrom = time.Now()
			go c.countDown(c.warnFrom)
		}
		if left := warnDelay - time.Since(c.warnFrom); left > 0 {
			v.WarnWait = int((left + time.Second - 1) / time.Second)
		}
	case p == session.PhasePending:
		v.Screen = ScreenKey
	case p == session.PhaseReady && c.listing != nil:
		v.Screen, v.Ready = ScreenMain, true
		v.Notice = i18n.Trans("Waiting until {0} allows {1}.", c.companyLocked(), c.listing.Address)
	case p == session.PhaseReady:
		v.Screen, v.Ready = ScreenMain, true
	case p == session.PhaseAsking:
		v.Screen, v.Asking, v.Notice = ScreenMain, true, i18n.Trans("A techie wants to connect.")
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
		return i18n.Trans("The key of this PC could not be loaded.")
	case errors.Is(err, appkey.ErrDamaged):
		return i18n.Trans("The key of this PC can't be used.") + " " + capitalize(err.Error()) + "."
	}
	return i18n.Trans("The key of this PC could not be loaded or created: {0}", err.Error())
}

// Start loads the key and the configuration and connects to the last server
// address. Without a stored address it shows the empty address screen;
// the address can only be changed once connecting failed or after
// Disconnect.
func (c *Controller) Start() {
	c.mu.Lock()
	c.loadLocked()
	if addr := c.storedAddrLocked(); addr != "" && c.key != nil && c.keyErr == nil {
		c.startComputerLocked(addr, nil)
	}
	c.mu.Unlock()
	c.update()
}

// storedAddrLocked is the address to connect to or prefill: the last one
// that reached a server, else the server this PC is registered on. A locked
// PC only goes back to an address it may still use, else to its primary
// server.
func (c *Controller) storedAddrLocked() string {
	addr := c.cfg.LastAddress
	if addr == "" {
		addr = c.cfg.ServerAddress
	}
	if l := c.cfg.Lock; l != nil && !c.allowedLocked(addr) {
		return l.Address
	}
	return addr
}

// allowedLocked reports whether this PC may connect to addr: always while
// unlocked, else only to the primary server and the approved servers.
func (c *Controller) allowedLocked(addr string) bool {
	l := c.cfg.Lock
	if l == nil || addr == l.Address {
		return true
	}
	_, ok := l.ApprovedAt(addr)
	return ok
}

// companyLocked names the server of the running computer: its company
// name, else its address.
func (c *Controller) companyLocked() string {
	if c.info != nil && c.info.Company != "" {
		return c.info.Company
	}
	return c.addr
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

// startComputerLocked replaces the running computer. The server's host key
// and the techie pins are the stored ones when addr is the stored server;
// otherwise the host key is pinned on first use. A locked PC pins its
// primary server's host key, and an approved server's by the fingerprint
// the primary server approved. listing, if set, is the server to ask the
// primary server (addr) to approve.
func (c *Controller) startComputerLocked(addr string, listing *control.ApprovedServer) {
	c.stopComputerLocked()
	c.gen++
	c.addr = addr
	c.reached, c.known, c.lastErr = false, false, nil
	c.info, c.approved, c.listing, c.lockAsked = nil, nil, listing, false
	c.warnFrom = time.Time{}
	c.st = session.State{Phase: session.PhaseConnecting}
	var hostKey, fp string
	var techies map[string]string
	if addr == c.cfg.ServerAddress {
		hostKey, techies = c.cfg.ServerHostKey, c.cfg.Clone().KnownTechies
	}
	if l := c.cfg.Lock; l != nil {
		if addr == l.Address {
			hostKey = l.HostKey
		} else if a, ok := l.ApprovedAt(addr); ok {
			hostKey, fp = "", a.Fingerprint
		}
	}
	c.computer = c.newComputerLocked(addr, hostKey, fp, techies, listing, c.gen)
	c.computer.Start()
}

// newComputerLocked returns a server computer whose events carry gen.
func (c *Controller) newComputerLocked(addr, hostKey, fp string, techies map[string]string, listing *control.ApprovedServer, gen int) *session.Computer {
	var runner session.Runner
	if c.d.NewRunner != nil {
		runner = c.d.NewRunner(c.key)
	}
	opts := session.Options{
		Addr:              addr,
		Signer:            c.key.Signer,
		AppVersion:        c.d.AppVersion,
		UserMachine:       c.d.UserMachine,
		ServerHostKey:     hostKey,
		KnownTechies:      techies,
		ServerFingerprint: fp,
		Handler:           handler{c, gen},
		Runner:            runner,
		EscHook:           c.d.EscHook,
		Logger:            c.log,
		Transport:         c.transportLocked(),
	}
	if listing != nil {
		opts.Listing = &control.RequestListing{Address: listing.Address, Fingerprint: listing.Fingerprint}
	}
	if c.d.Tune != nil {
		c.d.Tune(&opts)
	}
	return session.New(opts)
}

// stopComputerLocked stops the running computer, or the host key lookup
// before it.
func (c *Controller) stopComputerLocked() {
	if c.computer != nil {
		c.computer.Close()
		c.computer = nil
	}
	if c.stopProbe != nil {
		c.stopProbe()
		c.stopProbe = nil
	}
	if c.stopLock != nil {
		c.stopLock()
		c.stopLock = nil
	}
	c.probing = false
	c.listing = nil
	c.st = session.State{}
}

// SetUseSystemProxy is the "Use system proxy settings" box on the address
// screen. It applies from the next connection on.
func (c *Controller) SetUseSystemProxy(on bool) {
	c.mu.Lock()
	changed := c.cfg.NoSystemProxy == on
	c.cfg.NoSystemProxy = !on
	c.mu.Unlock()
	if changed {
		c.save()
	}
	c.update()
}

// transportLocked returns how to reach servers: through the system's proxy
// unless the user unticked "Use system proxy settings".
func (c *Controller) transportLocked() transport.DialOptions {
	if c.cfg.NoSystemProxy {
		return transport.DialOptions{Proxy: sysproxy.Off}
	}
	return transport.DialOptions{Proxy: sysproxy.Func()}
}

// Connect is the Connect button on the address screen.
func (c *Controller) Connect(text string) {
	c.mu.Lock()
	if c.closed || c.key == nil || c.computer != nil || c.probing {
		c.mu.Unlock()
		return
	}
	a, err := serveraddr.Parse(text)
	switch {
	case err != nil:
		c.addr = text
		c.lastErr = nil
		c.inputErr = capitalize(err.Error()) + "."
	case c.allowedLocked(a.String()):
		c.inputErr = ""
		c.startComputerLocked(a.String(), nil)
	default:
		c.inputErr = ""
		c.probeLocked(a.String())
	}
	c.mu.Unlock()
	c.update()
}

// probeLocked is Connect on a locked PC to a server that is not approved
// (PLAN.md 12.3): it only looks up that server's host key, then connects
// to the primary server and asks it to approve that server with that key.
// Once it does, the PC connects to the server; until then the user sees
// the primary server's waiting screen.
func (c *Controller) probeLocked(addr string) {
	c.stopComputerLocked()
	c.gen++
	gen := c.gen
	c.addr, c.lastErr, c.probing = addr, nil, true
	ctx, cancel := context.WithCancel(context.Background())
	c.stopProbe = cancel
	probe := c.d.Probe
	if probe == nil {
		topts := c.transportLocked()
		probe = func(ctx context.Context, addr string) (ssh.PublicKey, error) {
			return session.ProbeHostKey(ctx, addr, 0, topts)
		}
	}
	c.log.Info("looking up the host key of a server that is not approved", "server", addr)
	go func() {
		key, err := probe(ctx, addr)
		c.mu.Lock()
		if c.gen != gen || !c.probing {
			c.mu.Unlock()
			return
		}
		c.probing, c.stopProbe = false, nil
		cancel()
		switch {
		case err != nil:
			c.lastErr = err
		default:
			s := control.ApprovedServer{Address: addr, Fingerprint: ssh.FingerprintSHA256(key)}
			if slices.Contains(c.cfg.Lock.Approved, s) {
				c.startComputerLocked(addr, nil)
			} else {
				c.startComputerLocked(c.cfg.Lock.Address, &s)
			}
		}
		c.mu.Unlock()
		c.update()
	}()
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
	if c.computer != nil || !errors.As(c.lastErr, &hk) || hk.NewFingerprint != newFingerprint || !c.mayTrustLocked() {
		c.mu.Unlock()
		return
	}
	c.log.Warn("server host key change accepted by the user", "old", hk.OldFingerprint, "new", hk.NewFingerprint)
	if c.addr == c.cfg.ServerAddress {
		c.cfg.ServerHostKey = hk.NewKeyLine
	}
	if l := c.cfg.Lock; l != nil {
		l.HostKey = hk.NewKeyLine
	}
	c.startComputerLocked(c.addr, nil)
	c.mu.Unlock()
	c.save()
	c.update()
}

// mayTrustLocked reports whether the user may accept a changed host key of
// the server at c.addr: the server this PC is registered on, or for a locked
// PC only its primary server. The key of an approved server is the one the
// primary server approved; only the primary server can approve another.
func (c *Controller) mayTrustLocked() bool {
	if l := c.cfg.Lock; l != nil {
		return c.addr == l.Address
	}
	return c.addr == c.cfg.ServerAddress
}

// warnedLocked reports whether the first connection warning was accepted for
// the server key with fingerprint fp.
func (c *Controller) warnedLocked(fp string) bool {
	return fp == "" || slices.Contains(c.cfg.WarnedServers, fp)
}

// countDown renders once a second while the first connection warning that
// started at from counts down.
func (c *Controller) countDown(from time.Time) {
	step := min(time.Second, warnDelay)
	for d := step; d <= warnDelay; d += step {
		time.Sleep(time.Until(from.Add(d)))
		c.update()
	}
}

// ContinueWarning is Continue on the first connection warning: it works
// once the warning showed for five seconds, and the warning is not shown
// again for this server key.
func (c *Controller) ContinueWarning() {
	c.mu.Lock()
	fp := c.st.ServerFingerprint
	if c.computer == nil || c.st.Phase != session.PhasePending || c.warnedLocked(fp) ||
		c.warnFrom.IsZero() || time.Since(c.warnFrom) < warnDelay {
		c.mu.Unlock()
		return
	}
	c.cfg.WarnedServers = append(c.cfg.WarnedServers, fp)
	c.mu.Unlock()
	c.save()
	c.update()
}

// RemoveKey deletes the key and the configuration, then creates a new key
// and goes back to the address screen. Call only after the user confirmed.
// A lock stays: this PC then starts over as a new computer of its primary
// server (PLAN.md 12.2).
func (c *Controller) RemoveKey() error {
	c.mu.Lock()
	c.stopComputerLocked()
	lock := c.cfg.Clone().Lock
	err := c.d.RemoveKey()
	if err == nil {
		c.addr, c.inputErr, c.lastErr = "", "", nil
		c.loadLocked()
		c.cfg.Lock = lock
	}
	c.mu.Unlock()
	if err == nil && lock != nil {
		c.save()
	}
	c.update()
	return err
}

// Close ends any session and disconnects. It does not wait; see WaitClosed.
func (c *Controller) Close() {
	c.mu.Lock()
	c.closed = true
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

// Version is the app version, shown in the window title.
func (c *Controller) Version() string { return c.d.AppVersion }

// LatestRelease asks for a release newer than Version and returns nil when
// there is none (or for a development build). It blocks, so the window
// calls it outside the UI thread, when the user clicks Check for updates.
func (c *Controller) LatestRelease(ctx context.Context) (*update.Release, error) {
	if c.d.CheckUpdate == nil {
		return nil, nil
	}
	r, err := c.d.CheckUpdate(ctx)
	switch {
	case err != nil:
		c.log.Info("update check failed", "err", err)
	case r != nil:
		c.log.Info("update available", "version", r.Version)
	default:
		c.log.Info("no update available", "version", c.d.AppVersion)
	}
	return r, err
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
	c.askLockLocked()
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

// ServerInfo shows who runs the server. Text from a server can say
// anything, so it is shown on one line without control characters.
func (h handler) ServerInfo(info control.ServerInfo) {
	c := h.c
	c.mu.Lock()
	if c.gen != h.gen || c.computer == nil {
		c.mu.Unlock()
		return
	}
	c.info = &control.ServerInfo{
		Company:     servertext.Clean(info.Company),
		Phone:       servertext.Clean(info.Phone),
		Description: servertext.Clean(info.Description),
	}
	save := false
	if l := c.cfg.Lock; l != nil && c.addr == l.Address && c.info.Company != "" && l.Company != c.info.Company {
		l.Company = c.info.Company
		save = true
	}
	c.askLockLocked()
	c.mu.Unlock()
	if save {
		c.save()
	}
	c.update()
}

// ApprovedServers keeps the approved list the server sent. From the primary
// server of a locked PC it replaces the stored list, and when it now has
// the server this PC asked for, the PC connects there.
func (h handler) ApprovedServers(servers []control.ApprovedServer) {
	c := h.c
	c.mu.Lock()
	if c.gen != h.gen || c.computer == nil {
		c.mu.Unlock()
		return
	}
	c.approved = slices.Clone(servers)
	l := c.cfg.Lock
	if l == nil || c.addr != l.Address {
		c.mu.Unlock()
		return
	}
	save := !slices.Equal(l.Approved, servers)
	l.Approved = slices.Clone(servers)
	if q := c.listing; q != nil && slices.Contains(servers, *q) {
		c.log.Info("the primary server approved the server", "server", q.Address, "fingerprint", q.Fingerprint)
		c.startComputerLocked(q.Address, nil)
	}
	c.mu.Unlock()
	if save {
		c.save()
	}
	c.update()
}

// ListingRefused goes back to the address screen when a techie of the
// primary server refused the server this PC asked for.
func (h handler) ListingRefused(s control.ApprovedServer) {
	c := h.c
	c.mu.Lock()
	if c.gen != h.gen || c.listing == nil || *c.listing != s {
		c.mu.Unlock()
		return
	}
	company := c.companyLocked()
	c.log.Info("the primary server refused the server", "server", s.Address, "fingerprint", s.Fingerprint)
	c.stopComputerLocked()
	c.addr, c.lastErr = s.Address, nil
	c.inputErr = i18n.Trans("{0} did not allow this PC to connect to {1}.", company, s.Address)
	c.mu.Unlock()
	c.update()
}

// askLockLocked asks the lock question (PLAN.md 12.1) once per connection
// of an unlocked PC, as soon as the server accepted this PC as registered
// and said who runs it.
func (c *Controller) askLockLocked() {
	if c.cfg.Lock != nil || c.lockAsked || c.computer == nil || c.listing != nil || c.techieAsking ||
		c.st.Phase != session.PhaseReady || c.info == nil || c.info.Company == "" {
		return
	}
	c.lockAsked = true
	ctx, cancel := context.WithCancel(context.Background())
	c.stopLock = cancel
	open := make(chan struct{})
	c.lockOpen = open
	gen, company := c.gen, c.info.Company
	go func() {
		ok := c.d.UI.ConfirmLock(ctx, company)
		c.mu.Lock()
		close(open)
		if c.lockOpen == open {
			c.lockOpen = nil
		}
		if !ok || ctx.Err() != nil || c.gen != gen || c.computer == nil || !c.known ||
			c.cfg.Lock != nil || c.st.ServerHostKey == "" {
			c.mu.Unlock()
			return
		}
		c.cfg.Lock = &config.Lock{
			Address:  c.addr,
			HostKey:  c.st.ServerHostKey,
			Company:  company,
			Approved: slices.Clone(c.approved),
		}
		c.log.Info("locked to the primary server", "server", c.addr, "fingerprint", c.st.ServerFingerprint)
		c.mu.Unlock()
		c.save()
		c.update()
	}()
}

// ConfirmTechie asks the user about a techie, once the lock question, if it
// is open, is answered. Afterwards the lock question can be asked again if
// it is still due.
func (h handler) ConfirmTechie(ctx context.Context, techie session.Techie, oldFP string) bool {
	c := h.c
	c.mu.Lock()
	for c.lockOpen != nil {
		open := c.lockOpen
		c.mu.Unlock()
		select {
		case <-open:
		case <-ctx.Done():
			return false
		}
		c.mu.Lock()
	}
	c.techieAsking = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.techieAsking = false
		if c.gen == h.gen {
			c.askLockLocked()
		}
		c.mu.Unlock()
	}()
	techie.Name = servertext.Clean(techie.Name)
	return c.d.UI.ConfirmTechie(ctx, techie, oldFP)
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
