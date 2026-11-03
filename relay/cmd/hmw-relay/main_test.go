package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/helpbuoy/relay/auth"
	"github.com/mevdschee/helpbuoy/relay/store"
)

func TestRunExitCodes(t *testing.T) {
	tests := []struct {
		args []string
		code int
	}{
		{nil, 2},
		{[]string{"bogus"}, 2},
		{[]string{"version"}, 0},
		{[]string{"help"}, 0},
		{[]string{"serve", "--nope"}, 1},
		{[]string{"serve", "extra"}, 1},
		{[]string{"serve", "--state", t.TempDir(), "--listen", "bad address"}, 1},
		{[]string{"helper"}, 2},
		{[]string{"helper", "bogus", "--state", t.TempDir()}, 2},
		{[]string{"helper", "add", "x", "--state", t.TempDir()}, 2},
		{[]string{"client", "remove", "--state", t.TempDir()}, 2},
		{[]string{"hostkey", "extra", "--state", t.TempDir()}, 2},
	}
	for _, tt := range tests {
		var out, errOut bytes.Buffer
		if got := run(tt.args, &out, &errOut); got != tt.code {
			t.Errorf("run(%q) = %d, want %d (stderr: %s)", tt.args, got, tt.code, errOut.String())
		}
	}
}

func TestVersion(t *testing.T) {
	var out bytes.Buffer
	run([]string{"version"}, &out, &bytes.Buffer{})
	if !strings.HasPrefix(out.String(), "hmw-relay ") {
		t.Errorf("version output %q", out.String())
	}
}

func TestParseServeDefaults(t *testing.T) {
	cfg, err := parseServe(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.listen != ":5938" {
		t.Errorf("listen = %q, want :5938", cfg.listen)
	}
	if !strings.HasSuffix(cfg.state, "hmw-relay") {
		t.Errorf("state = %q, want a path ending in hmw-relay", cfg.state)
	}
}

func keyLine(t *testing.T, comment string) (string, string) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return auth.KeyLine(k, comment), auth.Fingerprint(k)
}

func runOK(t *testing.T, args ...string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := run(args, &out, &errOut); code != 0 {
		t.Fatalf("run(%q) = %d: %s", args, code, errOut.String())
	}
	return out.String()
}

func runFail(t *testing.T, args ...string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := run(args, &out, &errOut); code == 0 {
		t.Fatalf("run(%q) succeeded: %s", args, out.String())
	}
	return errOut.String()
}

func TestAdmin(t *testing.T) {
	dir := t.TempDir()
	hl, hfp := keyLine(t, "alice-laptop")
	runOK(t, "helper", "add", "alice", hl, "--state", dir)
	runFail(t, "helper", "add", "alice", hl, "--state", dir)
	runFail(t, "helper", "add", "bob", "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQC7", "--state", dir)
	out := runOK(t, "helper", "list", "--state", dir)
	if !strings.Contains(out, "alice") || !strings.Contains(out, hfp) {
		t.Errorf("helper list:\n%s", out)
	}

	// Clients are registered by helpers through the relay; simulate that.
	cl, cfp := keyLine(t, "anna@PC")
	st, _ := store.Open(dir)
	if _, _, err := st.Update(func(s *store.State) error {
		_, _, err := s.AddClient(cl, "", "alice", time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// A key can't be both.
	runFail(t, "helper", "add", "carol", cl, "--state", dir)
	out = runOK(t, "client", "list", "--state", dir)
	if !strings.Contains(out, "anna@PC") || !strings.Contains(out, cfp) || !strings.Contains(out, "never") {
		t.Errorf("client list:\n%s", out)
	}
	runOK(t, "client", "remove", cfp, "--state", dir)
	runFail(t, "client", "remove", cfp, "--state", dir)
	if out := runOK(t, "client", "list", "--state", dir); strings.Contains(out, cfp) {
		t.Error("client still listed")
	}
	runOK(t, "helper", "remove", "alice", "--state", dir)
	runFail(t, "helper", "remove", "alice", "--state", dir)

	out1 := runOK(t, "hostkey", "--state", dir)
	out2 := runOK(t, "hostkey", "--state", dir)
	lines := strings.Split(strings.TrimSpace(out1), "\n")
	if out1 != out2 || len(lines) != 2 || !strings.HasPrefix(lines[0], "ssh-ed25519 ") || !strings.HasPrefix(lines[1], "SHA256:") {
		t.Errorf("hostkey output:\n%s", out1)
	}
}

func TestStateDirEnv(t *testing.T) {
	t.Setenv("HMW_RELAY_STATE", "/var/lib/hmw-relay")
	if d, err := stateDir(""); err != nil || d != "/var/lib/hmw-relay" {
		t.Fatalf("env: %q, %v", d, err)
	}
	if d, _ := stateDir("/tmp/x"); d != "/tmp/x" {
		t.Fatalf("flag must win over env: %q", d)
	}
	t.Setenv("HMW_RELAY_STATE", "")
	if d, err := stateDir(""); err != nil || d == "" || d == "/var/lib/hmw-relay" {
		t.Fatalf("default: %q, %v", d, err)
	}
}
