package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/helpbuoy/proto/control"
	"github.com/mevdschee/helpbuoy/server/auth"
	"github.com/mevdschee/helpbuoy/server/store"
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
		{[]string{"techie"}, 2},
		{[]string{"techie", "bogus", "--state", t.TempDir()}, 2},
		{[]string{"techie", "add", "x", "--state", t.TempDir()}, 2},
		{[]string{"computer", "remove", "--state", t.TempDir()}, 2},
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
	if !strings.HasPrefix(out.String(), "helpbuoy-server ") {
		t.Errorf("version output %q", out.String())
	}
}

func TestParseServeDefaults(t *testing.T) {
	cfg, err := parseServe(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.listen != ":5959" {
		t.Errorf("listen = %q, want :5959", cfg.listen)
	}
	if !strings.HasSuffix(cfg.state, "helpbuoy-server") {
		t.Errorf("state = %q, want a path ending in helpbuoy-server", cfg.state)
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
	runOK(t, "techie", "add", "alice", hl, "--state", dir)
	runFail(t, "techie", "add", "alice", hl, "--state", dir)
	runFail(t, "techie", "add", "bob", "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQC7", "--state", dir)
	out := runOK(t, "techie", "list", "--state", dir)
	if !strings.Contains(out, "alice") || !strings.Contains(out, hfp) {
		t.Errorf("techie list:\n%s", out)
	}

	// Computers are registered by techies through the server; simulate that.
	cl, cfp := keyLine(t, "anna@PC")
	st, _ := store.Open(dir)
	if _, _, err := st.Update(func(s *store.State) error {
		_, _, err := s.AddComputer(cl, "", "alice", time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// A key can't be both.
	runFail(t, "techie", "add", "carol", cl, "--state", dir)
	out = runOK(t, "computer", "list", "--state", dir)
	if !strings.Contains(out, "anna@PC") || !strings.Contains(out, cfp) || !strings.Contains(out, "never") {
		t.Errorf("computer list:\n%s", out)
	}
	runOK(t, "computer", "remove", cfp, "--state", dir)
	runFail(t, "computer", "remove", cfp, "--state", dir)
	if out := runOK(t, "computer", "list", "--state", dir); strings.Contains(out, cfp) {
		t.Error("computer still listed")
	}

	// computer add: the name defaults to the key comment, a key without a
	// comment needs one, and a key is added only once and never as both roles.
	if out := runOK(t, "computer", "add", cl, "--state", dir); !strings.Contains(out, "anna@PC") || !strings.Contains(out, cfp) {
		t.Errorf("computer add:\n%s", out)
	}
	runFail(t, "computer", "add", cl, "--state", dir)
	runFail(t, "computer", "add", hl, "--state", dir)
	bl, bfp := keyLine(t, "")
	runFail(t, "computer", "add", bl, "--state", dir)
	runOK(t, "computer", "add", bl, "Bob desktop", "--state", dir)
	out = runOK(t, "computer", "list", "--state", dir)
	if !strings.Contains(out, "Bob desktop") || !strings.Contains(out, bfp) || !strings.Contains(out, "admin") {
		t.Errorf("computer list after add:\n%s", out)
	}
	runFail(t, "computer", "add", "--state", dir)
	runOK(t, "techie", "remove", "alice", "--state", dir)
	runFail(t, "techie", "remove", "alice", "--state", dir)

	out1 := runOK(t, "hostkey", "--state", dir)
	out2 := runOK(t, "hostkey", "--state", dir)
	lines := strings.Split(strings.TrimSpace(out1), "\n")
	if out1 != out2 || len(lines) != 2 || !strings.HasPrefix(lines[0], "ssh-ed25519 ") || !strings.HasPrefix(lines[1], "SHA256:") {
		t.Errorf("hostkey output:\n%s", out1)
	}
}

func TestStateDirEnv(t *testing.T) {
	t.Setenv("HELPBUOY_SERVER_STATE", "/var/lib/helpbuoy-server")
	if d, err := stateDir(""); err != nil || d != "/var/lib/helpbuoy-server" {
		t.Fatalf("env: %q, %v", d, err)
	}
	if d, _ := stateDir("/tmp/x"); d != "/tmp/x" {
		t.Fatalf("flag must win over env: %q", d)
	}
	t.Setenv("HELPBUOY_SERVER_STATE", "")
	if d, err := stateDir(""); err != nil || d == "" || d == "/var/lib/helpbuoy-server" {
		t.Fatalf("default: %q, %v", d, err)
	}
}

func TestTitleAndInstruction(t *testing.T) {
	dir := t.TempDir()
	if out := runOK(t, "title", "--state", dir); out != "\n" {
		t.Fatalf("empty title: %q", out)
	}
	if out := runOK(t, "instruction", "--state", dir); out != control.DefaultInstruction+" (default)\n" {
		t.Fatalf("empty instruction: %q", out)
	}
	runOK(t, "title", "  Example Inc ", "--state", dir)
	runOK(t, "instruction", "Call +1 234 567 890 for support", "--state", dir)
	if out := runOK(t, "title", "--state", dir); out != "Example Inc\n" {
		t.Fatalf("title: %q", out)
	}
	if out := runOK(t, "instruction", "--state", dir); out != "Call +1 234 567 890 for support\n" {
		t.Fatalf("instruction: %q", out)
	}
	runFail(t, "title", strings.Repeat("x", 65), "--state", dir)
	runFail(t, "instruction", "two\nlines", "--state", dir)
	runFail(t, "title", "a", "b", "--state", dir)
	runOK(t, "title", "", "--state", dir)
	if out := runOK(t, "title", "--state", dir); out != "\n" {
		t.Fatalf("cleared title: %q", out)
	}
	if out := runOK(t, "instruction", "--state", dir); out != "Call +1 234 567 890 for support\n" {
		t.Fatalf("instruction after clearing the title: %q", out)
	}
}
