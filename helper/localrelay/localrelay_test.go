package localrelay

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary act as a fake hmw-relay when FAKE_HMW_RELAY_STATE
// is set: "serve --listen :PORT" listens and logs, "helper list" prints the
// state file, "helper add NAME --display D KEY" appends to it.
func TestMain(m *testing.M) {
	state := os.Getenv("FAKE_HMW_RELAY_STATE")
	if state == "" {
		os.Exit(m.Run())
	}
	args := os.Args[1:]
	switch {
	case len(args) == 3 && args[0] == "serve" && args[1] == "--listen":
		ln, err := net.Listen("tcp", args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, "listen:", err)
			os.Exit(1)
		}
		fmt.Println(`{"msg":"listening","addr":"` + args[2] + `"}`)
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				c.Close()
			}
		}()
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		<-sig
		fmt.Println(`{"msg":"stopping"}`)
		os.Exit(0)
	case len(args) == 2 && args[0] == "helper" && args[1] == "list":
		b, _ := os.ReadFile(state)
		os.Stdout.Write(b)
		os.Exit(0)
	case len(args) == 6 && args[0] == "helper" && args[1] == "add" && args[3] == "--display":
		f, _ := os.OpenFile(state, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		fmt.Fprintf(f, "%s %s SHA256:x %s\n", args[2], args[4], args[5])
		f.Close()
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, "fake hmw-relay: bad args", args)
	os.Exit(2)
}

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitState(t *testing.T, r *Relay, want State) Status {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if s := r.Status(); s.State == want {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("state %v, want %v (err %v)", r.Status().State, want, r.Status().Err)
	return Status{}
}

func TestStartAddsHelperAndStops(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("FAKE_HMW_RELAY_STATE", state)
	exe, _ := os.Executable()
	port := freePort(t)
	r := Start(Options{Exe: exe, Port: port, HelperName: "alice", HelperDisplay: "Alice", KeyLine: "ssh-ed25519 AAAA alice@pc"})
	waitState(t, r, StateRunning)

	deadline := time.Now().Add(10 * time.Second)
	for r.Status().HelperAdded == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	b, _ := os.ReadFile(state)
	if got := string(b); got != "alice Alice SHA256:x ssh-ed25519 AAAA alice@pc\n" {
		t.Errorf("state = %q", got)
	}
	if !Listening(port) {
		t.Error("not listening")
	}
	r.Stop()
	if r.Status().State != StateStopped {
		t.Errorf("state after stop = %v", r.Status().State)
	}
	if Listening(port) {
		t.Error("still listening after Stop")
	}
	lines := strings.Join(r.Status().Lines, "\n")
	if !strings.Contains(lines, "listening") || !strings.Contains(lines, "added this helper") {
		t.Errorf("log tail:\n%s", lines)
	}

	// Second start: the relay has a helper now, so nothing is added.
	r = Start(Options{Exe: exe, Port: port, HelperName: "alice", KeyLine: "ssh-ed25519 AAAA alice@pc"})
	waitState(t, r, StateRunning)
	time.Sleep(300 * time.Millisecond)
	r.Stop()
	if r.Status().HelperAdded != 0 {
		t.Error("helper added although the relay had one")
	}
}

func TestExternalRelay(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	r := Start(Options{Exe: "/nonexistent/hmw-relay", Port: ln.Addr().(*net.TCPAddr).Port})
	waitState(t, r, StateExternal)
	r.Stop()
}

func TestNotFound(t *testing.T) {
	r := Start(Options{Port: freePort(t)})
	waitState(t, r, StateNotFound)
	r.Stop()
}

func TestExits(t *testing.T) {
	t.Setenv("FAKE_HMW_RELAY_STATE", filepath.Join(t.TempDir(), "state"))
	exe, _ := os.Executable()
	// Port 1 is privileged, so the fake fails to listen and exits.
	r := Start(Options{Exe: exe, Port: 1})
	if os.Getuid() == 0 {
		t.Skip("root can listen on port 1")
	}
	s := waitState(t, r, StateExited)
	if s.Err == nil {
		t.Error("no error")
	}
	r.Stop()
}

func TestHasHelpers(t *testing.T) {
	if HasHelpers([]byte("")) || HasHelpers([]byte("no helpers\n")) {
		t.Error("empty list has helpers")
	}
	if !HasHelpers([]byte("alice  Alice  SHA256:abc\n")) {
		t.Error("list with a fingerprint has no helpers")
	}
}
