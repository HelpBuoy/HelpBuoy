// Package app is the client's logic between the window and the relay
// connection: which screen to show (the relay address with Connect, the key
// to send, waiting for a helper with Disconnect), the stored configuration,
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

	"github.com/mevdschee/help-me-win/client-windows/config"
	"github.com/mevdschee/help-me-win/client-windows/session"
	"github.com/mevdschee/help-me-win/client-windows/update"
	"github.com/mevdschee/help-me-win/proto/appkey"
	"github.com/mevdschee/help-me-win/proto/relayaddr"
)

// Screen is one of the window's layouts.
type Screen int

// Screens.
const (
	ScreenAddress  Screen = iota // relay address and Connect; always the first screen
	ScreenKey                    // connected, key not registered: send this key to a helper
	ScreenMain                   // registered: waiting for a helper to connect, Disconnect
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
	// Error is an inline error (ScreenAddress, ScreenKeyError).
	Error string

	// ScreenKey
	RelayFingerprint string
	KeyLine          string
	KeyFingerprint   string
	// Notice is a short, calm line for a temporary condition, like a lost
	// connection being retried (ScreenKey, ScreenMain). Empty most of the time.
	Notice string

	// ScreenMain
	KeyComment string // comment of this PC's key, user@machine
	Ready      bool   // connected and registered: helpers can connect
	Asking     bool   // a helper wants to connect; the user is being asked
	InSession  bool   // the window is minimized while true
	// RelayKeyChange is set when the relay showed another key than the
	// pinned one; the client stopped connecting (ScreenAddress). The window
	// warns, and TrustRelayKey pins the new key if the user accepts it.
	RelayKeyChange *session.HostKeyChangedError

	// ScreenKeyError
	KeyDir string

	// Update is a newer release to offer. It is only set while the client
	// is idle (address screen, or waiting for a helper), so the offer never
	// interrupts the user.
	Update *update.Release
}

// UI is the window, as the controller sees it.
type UI interface {
	// Render shows v. It is called from any goroutine, one call at a time,
	// and must not wait for the controller.
	Render(v View)
	// ConfirmHelper asks whether helper h may take control. oldFingerprint
	// is set when that helper showed a different key last time. It may
	// block; when ctx ends the question is moot and it should return false.
	ConfirmHelper(ctx context.Context, h session.Helper, oldFingerprint string) bool
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
	// once in the background by Start.
	// Errors (offline, the API refusing) are ignored: next start tries again.
	CheckUpdate func(ctx context.Context) (*update.Release, error)
	// Tune adjusts the session options, for tests.
	Tune   func(*session.Options)
	Logger *slog.Logger
}

// Controller connects the window, the configuration and the relay client.
// Its methods are meant to be called from the UI thread; they never block on
// the network.
type Controller struct {
	d   Deps
	log *slog.Logger

	mu       sync.Mutex
	cfg      *config.Config
	key      *appkey.Key
	keyErr   error
	addr     string // address of the running client, or the last one tried
	client   *session.Client
	gen      int // identifies the running client; events of older ones are dropped
	st       session.State
	reached  bool  // the running client got through to the relay at least once
	known    bool  // the running client is registered on its relay
	lastErr  error // why the last client stopped, shown on the address screen
	inputErr string
	closed   bool
	closing  <-chan struct{}

	newRelease    *update.Release // newer release, until offered
	updateStarted bool
	updateCancel  context.CancelFunc

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
	idle := v.Screen == ScreenAddress && !v.Connecting || v.Screen == ScreenMain && v.Ready
	if idle {
		v.Update = c.newRelease
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
	v.RelayFingerprint = c.st.RelayFingerprint

	lost := "The connection to the relay was lost. Trying again..."
	switch p := c.st.Phase; {
	case c.client == nil:
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
				v.RelayKeyChange = hk
			}
		}
	case p == session.PhasePending:
		v.Screen = ScreenKey
	case p == session.PhaseReady:
		v.Screen, v.Ready = ScreenMain, true
	case p == session.PhaseAsking:
		v.Screen, v.Asking, v.Notice = ScreenMain, true, "A helper wants to connect."
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

// Start loads the key and the configuration and connects to the last relay
// address right away. Without one it shows the empty address screen; the
// address can only be changed once connecting failed or after Disconnect.
func (c *Controller) Start() {
	c.mu.Lock()
	c.loadLocked()
	c.startUpdateCheckLocked()
	if addr := c.storedAddrLocked(); addr != "" && c.key != nil && c.keyErr == nil {
		c.startClientLocked(addr)
	}
	c.mu.Unlock()
	c.update()
}

// storedAddrLocked is the address to connect to or prefill: the last one
// that reached a relay, else the relay this PC is registered on.
func (c *Controller) storedAddrLocked() string {
	if c.cfg.LastAddress != "" {
		return c.cfg.LastAddress
	}
	return c.cfg.RelayAddress
}

// startUpdateCheckLocked runs Deps.CheckUpdate once in the background.
func (c *Controller) startUpdateCheckLocked() {
	if c.d.CheckUpdate == nil || c.updateStarted {
		return
	}
	c.updateStarted = true
	ctx, cancel := context.WithCancel(context.Background())
	c.updateCancel = cancel
	go func() {
		r, err := c.d.CheckUpdate(ctx)
		if err != nil {
			c.log.Info("update check failed", "err", err)
			return
		}
		if r == nil {
			return
		}
		c.log.Info("update available", "version", r.Version)
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return
		}
		c.newRelease = r
		c.mu.Unlock()
		c.update()
	}()
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

// startClientLocked replaces the running client. The relay's host key and
// the helper pins are the stored ones when addr is the stored relay;
// otherwise the host key is pinned on first use.
func (c *Controller) startClientLocked(addr string) {
	c.stopClientLocked()
	c.gen++
	c.addr = addr
	c.reached, c.known, c.lastErr = false, false, nil
	c.st = session.State{Phase: session.PhaseConnecting}
	var hostKey string
	var helpers map[string]string
	if addr == c.cfg.RelayAddress {
		hostKey, helpers = c.cfg.RelayHostKey, c.cfg.Clone().KnownHelpers
	}
	c.client = c.newClientLocked(addr, hostKey, helpers, c.gen)
	c.client.Start()
}

// newClientLocked returns a relay client whose events carry gen.
func (c *Controller) newClientLocked(addr, hostKey string, helpers map[string]string, gen int) *session.Client {
	var runner session.Runner
	if c.d.NewRunner != nil {
		runner = c.d.NewRunner(c.key)
	}
	opts := session.Options{
		Addr:         addr,
		Signer:       c.key.Signer,
		AppVersion:   c.d.AppVersion,
		UserMachine:  c.d.UserMachine,
		RelayHostKey: hostKey,
		KnownHelpers: helpers,
		Handler:      handler{c, gen},
		Runner:       runner,
		EscHook:      c.d.EscHook,
		Logger:       c.log,
	}
	if c.d.Tune != nil {
		c.d.Tune(&opts)
	}
	return session.New(opts)
}

// stopClientLocked stops the running client.
func (c *Controller) stopClientLocked() {
	if c.client != nil {
		c.client.Close()
		c.client = nil
	}
	c.st = session.State{}
}

// Connect is the Connect button on the address screen.
func (c *Controller) Connect(text string) {
	c.mu.Lock()
	if c.closed || c.key == nil || c.client != nil {
		c.mu.Unlock()
		return
	}
	a, err := relayaddr.Parse(text)
	if err != nil {
		c.addr = text
		c.lastErr = nil
		c.inputErr = capitalize(err.Error()) + "."
	} else {
		c.inputErr = ""
		c.startClientLocked(a.String())
	}
	c.mu.Unlock()
	c.update()
}

// Disconnect is the Disconnect button (and the "use a different relay
// address" link): it leaves the relay and goes back to the address screen.
// It does nothing during a session; the user ends that with Esc.
func (c *Controller) Disconnect() {
	c.mu.Lock()
	if c.client == nil || c.st.Phase == session.PhaseInSession {
		c.mu.Unlock()
		return
	}
	c.log.Info("disconnected by the user", "relay", c.addr)
	c.stopClientLocked()
	c.lastErr = nil
	c.mu.Unlock()
	c.update()
}

// TrustRelayKey pins the relay's new host key after the user accepted the
// change warning for newFingerprint, and connects again.
func (c *Controller) TrustRelayKey(newFingerprint string) {
	c.mu.Lock()
	var hk *session.HostKeyChangedError
	if c.client != nil || !errors.As(c.lastErr, &hk) || hk.NewFingerprint != newFingerprint || c.addr != c.cfg.RelayAddress {
		c.mu.Unlock()
		return
	}
	c.log.Warn("relay host key change accepted by the user", "old", hk.OldFingerprint, "new", hk.NewFingerprint)
	c.cfg.RelayHostKey = hk.NewKeyLine
	c.startClientLocked(c.addr)
	c.mu.Unlock()
	c.save()
	c.update()
}

// RemoveKey deletes the key and the configuration, then creates a new key
// and goes back to the address screen. Call only after the user confirmed.
func (c *Controller) RemoveKey() error {
	c.mu.Lock()
	c.stopClientLocked()
	err := c.d.RemoveKey()
	if err == nil {
		c.addr, c.inputErr, c.lastErr = "", "", nil
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
	if c.client != nil {
		c.closing = c.client.Done()
	}
	c.stopClientLocked()
	c.mu.Unlock()
}

// WaitClosed waits up to d for the client stopped by Close to finish, so
// the relay sees a clean disconnect before the process exits.
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

// DismissUpdate is called once View.Update was offered, whatever the user
// answered, so it is not offered again in this run.
func (c *Controller) DismissUpdate() {
	c.mu.Lock()
	c.newRelease = nil
	c.mu.Unlock()
	c.update()
}

// handler receives the events of one client generation.
type handler struct {
	c   *Controller
	gen int
}

func (h handler) StateChanged(s session.State) {
	c := h.c
	c.mu.Lock()
	if c.gen != h.gen || c.client == nil {
		c.mu.Unlock()
		return
	}
	c.st = s
	var save bool
	switch s.Phase {
	case session.PhaseStopped:
		// The client gave up (the relay could not be reached at first, or
		// refused this PC): back to the address screen with the reason.
		c.client = nil
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

// Registered saves the relay this PC is registered on. A different relay
// than the stored one replaces it, and the helper pins of the old relay are
// forgotten: helper names are only unique within one relay.
func (h handler) Registered(relayHostKey string) {
	c := h.c
	c.mu.Lock()
	if c.gen != h.gen || c.client == nil {
		c.mu.Unlock()
		return
	}
	c.known = true
	if c.cfg.RelayAddress != c.addr {
		c.cfg.KnownHelpers = nil
	}
	changed := c.cfg.RelayAddress != c.addr || c.cfg.RelayHostKey != relayHostKey
	c.cfg.RelayAddress = c.addr
	c.cfg.RelayHostKey = relayHostKey
	c.mu.Unlock()
	if changed {
		c.log.Info("registered on the relay", "relay", c.addr)
		c.save()
	}
	c.update()
}

func (h handler) HelperPinned(name, line string) {
	c := h.c
	c.mu.Lock()
	if c.gen != h.gen {
		c.mu.Unlock()
		return
	}
	if c.cfg.KnownHelpers == nil {
		c.cfg.KnownHelpers = make(map[string]string)
	}
	c.cfg.KnownHelpers[name] = line
	c.mu.Unlock()
	c.save()
}

func (h handler) ConfirmHelper(ctx context.Context, helper session.Helper, oldFP string) bool {
	return h.c.d.UI.ConfirmHelper(ctx, helper, oldFP)
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
