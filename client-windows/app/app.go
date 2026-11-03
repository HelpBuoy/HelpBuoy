// Package app is the client's logic between the window and the relay
// connection: which screen to show (first-run address entry, the key to send,
// the main window), the stored configuration, changing the relay address,
// Reset, Remove key and the update offer. It has
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
	ScreenAddress  Screen = iota // first run, step 1: relay address
	ScreenKey                    // first run, step 2: send this key to your helper
	ScreenMain                   // name and the Request help button
	ScreenKeyError               // the app key exists but can't be used
)

func (s Screen) String() string {
	return [...]string{"address", "key", "main", "key-error"}[s]
}

// Button is the label of the main button.
type Button int

// Main button labels.
const (
	ButtonRequest Button = iota // "Request help"
	ButtonCancel                // "Cancel request"
)

// View is everything the window shows.
type View struct {
	Screen Screen

	// ScreenAddress
	Address    string // prefilled or current address
	Connecting bool   // a Connect attempt is running
	// Error is an inline error (ScreenAddress, ScreenKeyError) or a problem
	// that stops the main window from working (ScreenMain).
	Error string

	// ScreenKey
	RelayFingerprint string
	KeyLine          string
	KeyFingerprint   string
	// CanChangeAddress shows the link back to the address field.
	CanChangeAddress bool
	// Notice is a short, calm line for a temporary condition, like a lost
	// connection being retried. Empty most of the time.
	Notice string

	// ScreenMain
	Name          string
	Button        Button
	ButtonEnabled bool
	InSession     bool // the window is minimized while true

	// ScreenKeyError
	KeyDir string

	// Change relay address, in the settings window.
	Relay RelayChange

	// Update is a newer release to offer. It is only set while the client
	// is idle (no request, no session, not changing the relay), so the
	// offer never interrupts the user.
	Update *update.Release
}

// RelayChange is the state of "Change relay address" in the settings.
type RelayChange struct {
	// Blocked says why the relay address can't be changed now (a request is
	// open, a session runs, the first window is still open). Empty if it
	// can.
	Blocked string
	// Connecting is true while the new address is being tried.
	Connecting bool
	// Error is why the last attempt failed; the old relay is still used.
	Error string
	// Done describes the relay just switched to, with its fingerprint.
	Done string
}

// Info is shown in the settings window.
type Info struct {
	Version          string
	RelayAddress     string
	RelayFingerprint string
	Name             string
	KeyFingerprint   string
	KeyLine          string
	KeyDir           string
	LastHelper       string // "name (SHA256:...)" of the last session, if any
	CheckUpdates     bool   // "Check for updates on startup"
}

// UI is the window, as the controller sees it.
type UI interface {
	// Render shows v. It is called from any goroutine, one call at a time,
	// and must not wait for the controller.
	Render(v View)
	// ConfirmHelperKeyChange asks whether to continue although the helper
	// shows a different key than last time. It may block.
	ConfirmHelperKeyChange(name, oldFingerprint, newFingerprint string) bool
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
	// once in the background by Start unless the user turned the check off.
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
	addr     string // address of the running client
	client   *session.Client
	gen      int // identifies the running client; events of older ones are dropped
	lastGen  int // the last generation handed out, to the running client or a probe
	st       session.State
	sawPend  bool // the running first-run client reached the relay
	firstRun bool
	inputErr string // address parse error
	closed   bool
	closing  <-chan struct{}

	// probe is the client trying a new relay address from the settings; the
	// running client stays in use until probe reached the new relay.
	probe     *session.Client
	probeGen  int
	probeAddr string
	change    RelayChange
	// switched is set once the relay was changed in this run, for the
	// wording of the key screen.
	switched bool

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
	if v.Screen == ScreenKeyError {
		return v
	}
	v.Relay = c.change
	switch {
	case c.firstRun:
		v.Relay.Blocked = "Type the relay address in the first window."
	case c.st.Phase == session.PhaseRequested || c.st.Phase == session.PhaseTaken:
		v.Relay.Blocked = "Cancel your request for help first."
	case c.st.Phase == session.PhaseInSession:
		v.Relay.Blocked = "Not possible during a session."
	}
	idle := c.probe == nil && (v.Screen == ScreenAddress && !v.Connecting ||
		v.Screen == ScreenMain && !v.InSession && v.Button == ButtonRequest &&
			c.st.Phase != session.PhaseTaken)
	if idle {
		v.Update = c.newRelease
	}
	return v
}

// screenLocked computes the View without the settings and update parts.
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
	v.RelayFingerprint = c.st.RelayFingerprint
	running := c.client != nil && c.st.Phase != session.PhaseStopped

	if c.firstRun {
		switch {
		case running && c.sawPend:
			v.Screen = ScreenKey
			v.CanChangeAddress = true
			if c.st.Phase == session.PhaseConnecting {
				v.Notice = "The connection to the relay was lost. Trying again..."
			}
		default:
			v.Screen = ScreenAddress
			v.Address = c.addr
			if v.Address == "" {
				v.Address = c.cfg.LastAddress
			}
			v.Connecting = running
			if c.inputErr != "" {
				v.Error = c.inputErr
			} else if !running && c.st.Err != nil {
				v.Error = session.Describe(c.st.Err)
			}
		}
		return v
	}

	if c.st.Phase == session.PhasePending {
		v.Screen = ScreenKey
		if c.switched {
			v.Notice = "This PC is not registered on this relay yet. Send this key to a helper who uses this relay."
		} else {
			v.Notice = "This PC is not registered on the relay (anymore). Send this key to your helper again."
		}
		return v
	}
	v.Screen = ScreenMain
	v.Name = c.cfg.Name
	switch c.st.Phase {
	case session.PhaseReady:
		v.Button, v.ButtonEnabled = ButtonRequest, true
	case session.PhaseRequested:
		v.Button, v.ButtonEnabled = ButtonCancel, true
	case session.PhaseTaken:
		v.Button = ButtonCancel
	case session.PhaseInSession:
		v.Button = ButtonRequest
		v.InSession = true
	case session.PhaseConnecting:
		v.Notice = "Connecting to the relay..."
	case session.PhaseStopped:
		if c.st.Err != nil {
			v.Error = session.Describe(c.st.Err)
		}
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

// Start loads the key and the configuration and shows the first screen;
// with a stored configuration it connects right away.
func (c *Controller) Start() {
	c.mu.Lock()
	c.loadLocked()
	c.startUpdateCheckLocked()
	c.mu.Unlock()
	c.update()
}

// startUpdateCheckLocked runs Deps.CheckUpdate once in the background.
func (c *Controller) startUpdateCheckLocked() {
	if c.d.CheckUpdate == nil || c.updateStarted || c.cfg.NoUpdateCheck {
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
	c.firstRun = !cfg.Registered()
	if c.keyErr == nil && !c.firstRun {
		c.startClientLocked(cfg.RelayAddress, cfg.RelayHostKey, true)
	}
}

// startClientLocked replaces the running client.
func (c *Controller) startClientLocked(addr, hostKey string, retry bool) {
	c.stopClientLocked()
	c.lastGen++
	c.gen = c.lastGen
	c.addr = addr
	c.sawPend = false
	c.st = session.State{Phase: session.PhaseConnecting}
	c.client = c.newClientLocked(addr, hostKey, c.cfg.Clone().KnownHelpers, retry, c.gen)
	c.client.Start()
}

// newClientLocked returns a relay client whose events carry gen.
func (c *Controller) newClientLocked(addr, hostKey string, helpers map[string]string, retry bool, gen int) *session.Client {
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
		Retry:        retry,
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

// stopClientLocked stops the running client and any relay change.
func (c *Controller) stopClientLocked() {
	c.stopProbeLocked()
	c.change = RelayChange{}
	if c.client != nil {
		c.client.Close()
		c.client = nil
	}
	c.st = session.State{}
}

func (c *Controller) stopProbeLocked() {
	if c.probe != nil {
		c.probe.Close()
		c.probe = nil
	}
}

// Connect is the first-run Connect button.
func (c *Controller) Connect(text string) {
	c.mu.Lock()
	if c.closed || c.key == nil {
		c.mu.Unlock()
		return
	}
	a, err := relayaddr.Parse(text)
	if err != nil {
		c.stopClientLocked()
		c.addr = text
		c.inputErr = capitalize(err.Error()) + "."
	} else {
		c.inputErr = ""
		c.startClientLocked(a.String(), "", false)
	}
	c.mu.Unlock()
	c.update()
}

// ChangeAddress goes back from the key screen to the address field.
func (c *Controller) ChangeAddress() {
	c.mu.Lock()
	if c.firstRun {
		addr := c.addr
		c.stopClientLocked()
		c.addr = addr
		c.sawPend = false
	}
	c.mu.Unlock()
	c.update()
}

// ChangeRelay is the Connect button of "Change relay address" in the
// settings. It tries text as the new relay address while the current relay
// stays in use. The new relay's host key is pinned on first use, as on the
// first-run window. Once the new relay answered, the client switches to it
// for good: the new address and host key replace the old ones, the key
// stays the same, and the known helpers of the old relay are forgotten. If
// the key is not registered there, the window shows the key to send to a
// helper of that relay. If the new relay can't be reached, nothing changes
// and View.Relay.Error says why.
func (c *Controller) ChangeRelay(text string) {
	c.mu.Lock()
	v := c.viewLocked()
	if c.closed || c.key == nil || v.Screen == ScreenKeyError || v.Relay.Blocked != "" {
		c.mu.Unlock()
		return
	}
	c.stopProbeLocked()
	c.change = RelayChange{}
	a, err := relayaddr.Parse(text)
	switch {
	case err != nil:
		c.change.Error = capitalize(err.Error()) + "."
	case a.String() == c.cfg.RelayAddress:
		c.change.Error = "This PC already uses this relay."
	default:
		c.lastGen++
		c.probeGen = c.lastGen
		c.probeAddr = a.String()
		c.change.Connecting = true
		c.probe = c.newClientLocked(c.probeAddr, "", nil, false, c.probeGen)
		c.probe.Start()
	}
	c.mu.Unlock()
	c.update()
}

// EndRelayChange stops a relay change that is still being tried and clears
// its result; the settings window calls it when it closes.
func (c *Controller) EndRelayChange() {
	c.mu.Lock()
	c.stopProbeLocked()
	c.change = RelayChange{}
	c.mu.Unlock()
	c.update()
}

// probeEventLocked handles an event of the client trying a new relay. It
// returns the client to close and whether the configuration must be saved.
func (c *Controller) probeEventLocked(s session.State, name string) (old *session.Client, save bool) {
	switch s.Phase {
	case session.PhaseStopped:
		c.probe = nil
		c.change = RelayChange{Error: session.Describe(s.Err)}
		if s.Err == nil {
			c.change.Error = "The relay could not be reached."
		}
		return nil, false
	case session.PhaseConnecting:
		if name == "" {
			return nil, false
		}
	}
	// The new relay answered: switch to it.
	old = c.client
	c.client, c.probe = c.probe, nil
	c.gen = c.probeGen
	c.addr = c.probeAddr
	c.st = s
	c.sawPend = false
	c.switched = true
	c.cfg.RelayAddress = c.probeAddr
	c.cfg.RelayHostKey = s.RelayHostKey
	c.cfg.KnownHelpers = nil
	c.cfg.LastAddress = ""
	if name != "" {
		c.cfg.Name = name
	}
	c.change = RelayChange{Done: "Connected to " + c.probeAddr + ".\nRelay fingerprint: " + s.RelayFingerprint}
	c.log.Info("relay changed", "relay", c.probeAddr, "fingerprint", s.RelayFingerprint)
	return old, true
}

// RequestHelp is the Request help button.
func (c *Controller) RequestHelp() {
	c.mu.Lock()
	cl := c.client
	if c.probe != nil {
		cl = nil // the relay is being changed
	}
	c.mu.Unlock()
	if cl != nil {
		cl.RequestHelp()
	}
}

// CancelRequest is the Cancel request button.
func (c *Controller) CancelRequest() {
	c.mu.Lock()
	cl := c.client
	c.mu.Unlock()
	if cl != nil {
		cl.CancelRequest()
	}
}

// Reset forgets the configuration (not the key) and starts the first-run
// flow again.
func (c *Controller) Reset() error {
	c.mu.Lock()
	c.stopClientLocked()
	err := c.d.Store.Reset()
	prefs := c.cfg.Preferences()
	if err == nil && (prefs.PerformanceMode || prefs.NoUpdateCheck) {
		err = c.d.Store.Save(prefs)
	}
	c.cfg = prefs
	c.switched = false
	c.firstRun = true
	c.addr, c.inputErr = "", ""
	c.mu.Unlock()
	c.update()
	return err
}

// RemoveKey deletes the key and the configuration, then creates a new key
// and starts the first-run flow. Call only after the user confirmed.
func (c *Controller) RemoveKey() error {
	c.mu.Lock()
	c.stopClientLocked()
	err := c.d.RemoveKey()
	if err == nil {
		c.addr, c.inputErr = "", ""
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

// Info returns what the settings window shows.
func (c *Controller) Info() Info {
	c.mu.Lock()
	defer c.mu.Unlock()
	in := Info{Version: c.d.AppVersion, KeyDir: c.d.Store.Dir}
	if c.key != nil {
		in.KeyFingerprint, in.KeyLine = c.key.Fingerprint, c.key.Line
	}
	if c.cfg != nil {
		in.RelayAddress, in.Name = c.cfg.RelayAddress, c.cfg.Name
	}
	if in.RelayAddress == "" {
		in.RelayAddress = c.addr
	}
	in.RelayFingerprint = c.st.RelayFingerprint
	if h := c.st.Helper; h.Name != "" {
		in.LastHelper = h.Name + " (" + h.Fingerprint + ")"
	}
	in.CheckUpdates = c.cfg == nil || !c.cfg.NoUpdateCheck
	return in
}

// SetCheckUpdates is the "Check for updates on startup" checkbox. It takes
// effect on the next start.
func (c *Controller) SetCheckUpdates(on bool) {
	c.mu.Lock()
	if c.cfg == nil || c.cfg.NoUpdateCheck == !on {
		c.mu.Unlock()
		return
	}
	c.cfg.NoUpdateCheck = !on
	c.mu.Unlock()
	c.save()
}

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
	if c.probe != nil && h.gen == c.probeGen {
		old, save := c.probeEventLocked(s, "")
		c.mu.Unlock()
		if old != nil {
			old.Close()
		}
		if save {
			c.save()
		}
		c.update()
		return
	}
	if c.gen != h.gen || c.client == nil {
		c.mu.Unlock()
		return
	}
	c.st = s
	var save bool
	if s.Phase == session.PhasePending && c.firstRun && !c.sawPend {
		c.sawPend = true
		if c.cfg.LastAddress != c.addr {
			c.cfg.LastAddress = c.addr
			save = true
		}
	}
	c.mu.Unlock()
	if save {
		c.save()
	}
	c.update()
}

func (h handler) Registered(name, relayHostKey string) {
	c := h.c
	c.mu.Lock()
	if c.probe != nil && h.gen == c.probeGen {
		// Registered comes before the first StateChanged of a connection.
		old, save := c.probeEventLocked(session.State{
			Phase:            session.PhaseReady,
			RelayHostKey:     relayHostKey,
			RelayFingerprint: fingerprint(relayHostKey),
			Name:             name,
		}, name)
		c.mu.Unlock()
		if old != nil {
			old.Close()
		}
		if save {
			c.save()
		}
		c.update()
		return
	}
	if c.gen != h.gen || c.client == nil {
		c.mu.Unlock()
		return
	}
	changed := c.cfg.RelayAddress != c.addr || c.cfg.RelayHostKey != relayHostKey ||
		c.cfg.Name != name || c.cfg.LastAddress != ""
	c.cfg.RelayAddress = c.addr
	c.cfg.RelayHostKey = relayHostKey
	c.cfg.Name = name
	c.cfg.LastAddress = ""
	c.firstRun = false
	c.mu.Unlock()
	if changed {
		c.log.Info("registered on the relay", "name", name)
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

func (h handler) ConfirmHelperKeyChange(name, oldFP, newFP string) bool {
	return h.c.d.UI.ConfirmHelperKeyChange(name, oldFP, newFP)
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
