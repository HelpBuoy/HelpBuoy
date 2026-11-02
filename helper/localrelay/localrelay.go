// Package localrelay runs hmw-relay on the helper's own machine (PLAN.md 7.1):
// it starts "hmw-relay serve" from next to the helper's executable as a child
// process unless something already listens on the port, keeps the tail of its
// log, adds the helper's own key with "hmw-relay helper add" when the relay has
// no helpers yet, and stops the child when the app exits.
package localrelay

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// State of the local relay.
type State int

const (
	StateStarting State = iota // looking for hmw-relay or waiting for it to listen
	StateRunning               // our child process is serving
	StateExternal              // something else already listens on the port
	StateNotFound              // no hmw-relay next to the helper app
	StateExited                // our child process ended or failed to start
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
	case StateNotFound:
		return "hmw-relay not found"
	case StateExited:
		return "stopped unexpectedly"
	case StateStopped:
		return "stopped"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

// Options configure the local relay.
type Options struct {
	Exe  string // path of hmw-relay; "" means next to the running executable
	Port int    // listen port

	// For "hmw-relay helper add" when the relay has no helpers yet.
	HelperName    string
	HelperDisplay string
	KeyLine       string // the helper's own authorized_keys line; "" skips the step
}

// Status is a snapshot for the settings page.
type Status struct {
	State State
	Err   error
	Exe   string
	Lines []string // log tail, oldest first
	// HelperAdded counts successful "helper add" runs; the app reconnects to
	// the relay when it changes.
	HelperAdded int
}

// Relay is a running local relay manager.
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

// ExeName is the hmw-relay file name for this OS.
func ExeName() string {
	if runtime.GOOS == "windows" {
		return "hmw-relay.exe"
	}
	return "hmw-relay"
}

// FindExe returns the path of hmw-relay next to the running executable.
func FindExe() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if p, err := filepath.EvalSymlinks(self); err == nil {
		self = p
	}
	p := filepath.Join(filepath.Dir(self), ExeName())
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		return "", fmt.Errorf("%s not found next to %s", ExeName(), filepath.Base(self))
	}
	return p, nil
}

// Start begins managing the local relay in the background.
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

// Stop stops the child process, if this Relay started one, and waits for it.
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

func (r *Relay) run() {
	defer close(r.done)
	exe := r.opts.Exe
	var findErr error
	if exe == "" {
		exe, findErr = FindExe()
	}
	r.mu.Lock()
	r.status.Exe = exe
	r.mu.Unlock()

	if Listening(r.opts.Port) {
		r.set(StateExternal, nil)
		if findErr == nil {
			r.ensureHelper(exe)
		}
		<-r.ctx.Done()
		r.set(StateStopped, nil)
		return
	}
	if findErr != nil {
		r.set(StateNotFound, findErr)
		<-r.ctx.Done()
		return
	}

	cmd := exec.Command(exe, "serve", "--listen", ":"+strconv.Itoa(r.opts.Port))
	setSysProcAttr(cmd)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		r.set(StateExited, err)
		<-r.ctx.Done()
		return
	}
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			r.logLine(sc.Text())
		}
		io.Copy(io.Discard, pr)
	}()
	exited := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		pw.Close()
		exited <- err
	}()

	// Wait until it listens, exits, or we are stopped.
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	deadline := time.Now().Add(15 * time.Second)
wait:
	for {
		select {
		case err := <-exited:
			r.set(StateExited, exitErr(err))
			<-r.ctx.Done()
			return
		case <-r.ctx.Done():
			stopProcess(cmd, exited)
			r.set(StateStopped, nil)
			return
		case <-tick.C:
			if Listening(r.opts.Port) {
				break wait
			}
			if time.Now().After(deadline) {
				stopProcess(cmd, exited)
				r.set(StateExited, errors.New("hmw-relay did not start listening"))
				<-r.ctx.Done()
				return
			}
		}
	}
	r.set(StateRunning, nil)
	r.ensureHelper(exe)

	select {
	case err := <-exited:
		r.set(StateExited, exitErr(err))
		<-r.ctx.Done()
	case <-r.ctx.Done():
		stopProcess(cmd, exited)
		r.set(StateStopped, nil)
	}
}

func exitErr(err error) error {
	if err == nil {
		return errors.New("hmw-relay exited")
	}
	return fmt.Errorf("hmw-relay exited: %w", err)
}

// stopProcess asks the child to stop, and kills it after a grace period.
func stopProcess(cmd *exec.Cmd, exited <-chan error) {
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		cmd.Process.Kill() // Windows has no interrupt for other processes
	}
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		cmd.Process.Kill()
		<-exited
	}
}

// ensureHelper adds the helper's own key when the relay has no helpers yet.
// "hmw-relay helper list" output counts as empty when it lists no key: no line
// mentions ssh-ed25519 or a SHA256: fingerprint.
func (r *Relay) ensureHelper(exe string) {
	if r.opts.KeyLine == "" {
		return
	}
	out, err := r.cli(exe, "helper", "list")
	if err != nil {
		r.logLine("hmw-relay helper list: " + err.Error())
		return
	}
	if HasHelpers(out) {
		return
	}
	name := r.opts.HelperName
	if name == "" {
		name = "helper"
	}
	display := r.opts.HelperDisplay
	if display == "" {
		display = name
	}
	if _, err := r.cli(exe, "helper", "add", name, "--display", display, r.opts.KeyLine); err != nil {
		r.logLine("hmw-relay helper add: " + err.Error())
		return
	}
	r.logLine("added this helper app's key to the relay as " + name)
	r.mu.Lock()
	r.status.HelperAdded++
	r.mu.Unlock()
}

// HasHelpers reports whether "hmw-relay helper list" output lists any key.
func HasHelpers(out []byte) bool {
	return bytes.Contains(out, []byte("ssh-ed25519")) || bytes.Contains(out, []byte("SHA256:"))
}

// cli runs a short hmw-relay admin command.
func (r *Relay) cli(exe string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(r.ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	setSysProcAttr(cmd)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(errOut.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return out.Bytes(), nil
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
