// Package localrelay runs the relay inside the helper app (PLAN.md 7.1): the
// same relay/server code as the standalone helpbuoy-relay, compiled in, so there is
// no separate executable to find or start. Unless something already listens
// on the port, it serves on that port until Stop, keeps the tail of the relay
// log for the settings page, and adds the helper's own key when it is not a
// helper there yet.
//
// It uses the same state directory as "helpbuoy-relay serve" (the per-user data
// directory "helpbuoy-relay"), so the standalone relay and its admin commands
// (helpbuoy-relay helper add, client list, ...) work on the same helpers, clients
// and host key.
package localrelay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mevdschee/helpbuoy/proto/datadir"
	"github.com/mevdschee/helpbuoy/relay/auth"
	"github.com/mevdschee/helpbuoy/relay/logfile"
	"github.com/mevdschee/helpbuoy/relay/server"
	"github.com/mevdschee/helpbuoy/relay/store"
)

// State of the local relay.
type State int

const (
	StateStarting State = iota // opening the state directory and the port
	StateRunning               // serving inside this app
	StateExternal              // something else already listens on the port
	StateExited                // failed to start or stopped serving on its own
	StateStopped               // Stop was called
)

func (s State) String() string {
	switch s {
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateExternal:
		return "already running"
	case StateExited:
		return "stopped unexpectedly"
	case StateStopped:
		return "stopped"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

// Options configure the local relay.
type Options struct {
	Port     int    // listen port, on all interfaces
	StateDir string // "" means the per-user data directory "helpbuoy-relay"
	Version  string // reported to apps in Hello

	// The helper's own key, added when it is not a helper on the relay yet.
	HelperName string
	KeyLine    string // authorized_keys line; "" skips the step
}

// Status is a snapshot for the settings page.
type Status struct {
	State    State
	Err      error
	StateDir string
	Lines    []string // log tail, oldest first
	// HelperAdded counts how often the helper's own key was added; the app
	// reconnects to the relay when it changes.
	HelperAdded int
}

// Relay is a running local relay.
type Relay struct {
	opts   Options
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	status Status
	lines  []string
}

const maxLines = 200

// Start begins running the local relay in the background.
func Start(opts Options) *Relay {
	ctx, cancel := context.WithCancel(context.Background())
	r := &Relay{opts: opts, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go r.run()
	return r
}

// Status returns the current status.
func (r *Relay) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.status
	s.Lines = append([]string(nil), r.lines...)
	return s
}

// Stop stops the relay, if this Relay runs one, and waits until it has
// closed all connections and written its state.
func (r *Relay) Stop() {
	r.cancel()
	<-r.done
}

func (r *Relay) set(st State, err error) {
	r.mu.Lock()
	r.status.State, r.status.Err = st, err
	r.mu.Unlock()
}

// logLine appends one line to the log tail.
func (r *Relay) logLine(s string) {
	s = strings.TrimRight(s, "\r\n")
	if s == "" {
		return
	}
	if len(s) > 400 {
		s = s[:400] + "..."
	}
	r.mu.Lock()
	r.lines = append(r.lines, s)
	if len(r.lines) > maxLines {
		r.lines = append(r.lines[:0], r.lines[len(r.lines)-maxLines:]...)
	}
	r.mu.Unlock()
}

// fail records a start-up error and waits for Stop.
func (r *Relay) fail(err error) {
	r.set(StateExited, err)
	<-r.ctx.Done()
}

func (r *Relay) run() {
	defer close(r.done)
	dir := r.opts.StateDir
	if dir == "" {
		d, err := datadir.Migrate("helpbuoy-relay", "hmw-relay")
		if err != nil {
			r.fail(err)
			return
		}
		dir = d
	}
	r.mu.Lock()
	r.status.StateDir = dir
	r.mu.Unlock()

	if Listening(r.opts.Port) {
		// Probably a standalone helpbuoy-relay on this machine; it reloads
		// state.json by itself, so adding the key there still works.
		r.set(StateExternal, nil)
		r.ensureHelper(dir)
		<-r.ctx.Done()
		r.set(StateStopped, nil)
		return
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		r.fail(err)
		return
	}
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(r.opts.Port))
	if err != nil {
		r.fail(err)
		return
	}
	lf, err := logfile.Open(filepath.Join(dir, "relay.log"), logfile.DefaultMaxSize)
	if err != nil {
		ln.Close()
		r.fail(err)
		return
	}
	defer lf.Close()
	log := slog.New(slog.NewJSONHandler(io.MultiWriter(lf, tailWriter{r}), nil))

	// Before the server reads its state, so the key works on the first try.
	r.ensureHelper(dir)

	srv, err := server.New(server.Config{StateDir: dir, Version: r.opts.Version, Log: log})
	if err != nil {
		ln.Close()
		r.fail(err)
		return
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	r.set(StateRunning, nil)
	r.logLine(fmt.Sprintf("relay listening on :%d, state in %s", r.opts.Port, dir))

	select {
	case err := <-served:
		srv.Close()
		if err == nil || errors.Is(err, server.ErrServerClosed) {
			err = errors.New("relay stopped serving")
		}
		r.fail(err)
	case <-r.ctx.Done():
		srv.Close()
		<-served
		r.set(StateStopped, nil)
	}
}

// ensureHelper adds the helper's own key unless it already is a helper on
// this relay. A name that another helper has gets a number appended.
func (r *Relay) ensureHelper(dir string) {
	if r.opts.KeyLine == "" {
		return
	}
	k, _, err := auth.ParseKey(r.opts.KeyLine)
	if err != nil {
		r.logLine("this helper app's key: " + err.Error())
		return
	}
	fp := auth.Fingerprint(k)
	base := r.opts.HelperName
	if base == "" {
		base = "helper"
	}
	st, err := store.Open(dir)
	if err != nil {
		r.logLine("relay state: " + err.Error())
		return
	}
	errKnown := errors.New("already a helper")
	var name string
	_, _, err = st.Update(func(s *store.State) error {
		taken := map[string]bool{}
		for _, h := range s.Helpers {
			if hk, _, err := auth.ParseKey(h.PublicKey); err == nil && auth.Fingerprint(hk) == fp {
				return errKnown
			}
			taken[h.Name] = true
		}
		name = base
		for n := 2; taken[name]; n++ {
			name = fmt.Sprintf("%s%d", base, n)
		}
		return s.AddHelper(name, r.opts.KeyLine)
	})
	switch {
	case errors.Is(err, errKnown):
		return
	case err != nil:
		r.logLine("adding this helper app's key: " + err.Error())
		return
	}
	r.logLine("added this helper app's key to the relay as " + name)
	r.mu.Lock()
	r.status.HelperAdded++
	r.mu.Unlock()
}

// tailWriter feeds the relay's log lines into the log tail.
type tailWriter struct{ r *Relay }

func (w tailWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		w.r.logLine(line)
	}
	return len(p), nil
}

// Listening reports whether something accepts TCP connections on
// 127.0.0.1:port.
func Listening(port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 300*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}
