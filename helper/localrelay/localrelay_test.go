package localrelay

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/helpbuoy/relay/store"
)

const keyLine = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJsaI2z9Ean/dC7UtgWXE4Sfld8k4km6FTbrTsI6K3RI alice@pc"

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitState(t *testing.T, r *Relay, want State) Status {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := r.Status()
		if st.State == want {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("state %v (err %v), want %v; log:\n%s", st.State, st.Err, want, strings.Join(st.Lines, "\n"))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func helpers(t *testing.T, dir string) []store.Helper {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	idx, _, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	return idx.State.Helpers
}

// TestRunsInProcess starts the relay inside the test process, checks that it
// speaks SSH, adds the helper's key once, and stops cleanly.
func TestRunsInProcess(t *testing.T) {
	dir := t.TempDir()
	port := freePort(t)
	r := Start(Options{Port: port, StateDir: dir, Version: "test", HelperName: "alice", KeyLine: keyLine})
	st := waitState(t, r, StateRunning)
	if st.HelperAdded != 1 || st.StateDir != dir {
		t.Fatalf("status %+v", st)
	}

	// A real SSH handshake with the relay's host key.
	nc, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(port)), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	nc.SetDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 64)
	n, _ := nc.Read(buf)
	nc.Close()
	if !strings.HasPrefix(string(buf[:n]), "SSH-2.0-") {
		t.Fatalf("banner %q", buf[:n])
	}

	if hs := helpers(t, dir); len(hs) != 1 || hs[0].Name != "alice" {
		t.Fatalf("helpers %+v", hs)
	}
	r.Stop()
	if st := r.Status(); st.State != StateStopped {
		t.Fatalf("after Stop: %v", st.State)
	}
	if Listening(port) {
		t.Fatal("still listening after Stop")
	}

	// A second start finds the helper and adds nothing; the host key stays.
	r = Start(Options{Port: port, StateDir: dir, HelperName: "bob", KeyLine: keyLine})
	if st := waitState(t, r, StateRunning); st.HelperAdded != 0 {
		t.Fatalf("added again: %+v", st)
	}
	r.Stop()
	if hs := helpers(t, dir); len(hs) != 1 {
		t.Fatalf("helpers %+v", hs)
	}
}

// TestAddsThisHelper: a relay that already has other helpers still gets
// this helper's key, with a number added when the name is taken.
func TestAddsThisHelper(t *testing.T) {
	dir := t.TempDir()
	port := freePort(t)
	r := Start(Options{Port: port, StateDir: dir, HelperName: "alice", KeyLine: keyLine})
	waitState(t, r, StateRunning)
	r.Stop()

	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	other := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	r = Start(Options{Port: port, StateDir: dir, HelperName: "alice", KeyLine: other})
	if st := waitState(t, r, StateRunning); st.HelperAdded != 1 {
		t.Fatalf("not added: %+v", st)
	}
	r.Stop()
	if hs := helpers(t, dir); len(hs) != 2 || hs[1].Name != "alice2" {
		t.Fatalf("helpers %+v", hs)
	}
}

// TestExternalRelay leaves an existing listener alone but still adds the key
// to the shared state, which a standalone relay reloads by itself.
func TestExternalRelay(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	dir := t.TempDir()
	r := Start(Options{Port: ln.Addr().(*net.TCPAddr).Port, StateDir: dir, HelperName: "alice", KeyLine: keyLine})
	st := waitState(t, r, StateExternal)
	r.Stop()
	if st.HelperAdded != 1 || len(helpers(t, dir)) != 1 {
		t.Fatalf("status %+v", st)
	}
}

// TestBadStateDir reports a start-up failure instead of hanging.
func TestBadStateDir(t *testing.T) {
	file := t.TempDir() + "/not-a-dir"
	if err := writeFile(file); err != nil {
		t.Fatal(err)
	}
	r := Start(Options{Port: freePort(t), StateDir: file})
	st := waitState(t, r, StateExited)
	r.Stop()
	if st.Err == nil {
		t.Fatal("want an error")
	}
}

func TestKeyLineParses(t *testing.T) {
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(keyLine)); err != nil {
		t.Fatal(err)
	}
}
