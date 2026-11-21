package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// admin sends one request to the admin command and returns its reply.
func admin(t *testing.T, req string) adminReply {
	t.Helper()
	var out, errOut bytes.Buffer
	if err := adminCmd(nil, strings.NewReader(req), &out, &errOut); err != nil {
		t.Fatalf("admin(%s): %v (stderr: %s)", req, err, errOut.String())
	}
	var r adminReply
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("admin(%s) replied %q: %v", req, out.String(), err)
	}
	if !strings.HasSuffix(out.String(), "}\n") {
		t.Errorf("reply %q does not end with a newline", out.String())
	}
	return r
}

func adminDirs(t *testing.T) (vhosts, stopped string) {
	base := t.TempDir()
	vhosts, stopped = filepath.Join(base, "vhosts"), filepath.Join(base, "stopped")
	t.Setenv("HELPBUOY_SERVER_VHOSTS", vhosts)
	t.Setenv("HELPBUOY_SERVER_STOPPED", stopped)
	return vhosts, stopped
}

func TestCheckAdmin(t *testing.T) {
	vhosts, stopped := adminDirs(t)
	ok := [][]string{
		{"version"},
		{"vhost", "list", "--vhosts", vhosts},
		{"vhost", "list", "--vhosts=" + stopped + "/"},
		{"techie", "list", "-vhosts", vhosts, "--vhost", "a.example.com"},
		{"techie", "add", "--vhost", "a.example.com", "--", "alice", "--", "ssh-ed25519 AAAA"},
		{"vhost", "stop", "a.example.com"},
		{"vhost", "stop", "a.example.com", "--vhosts", vhosts, "--stopped", stopped},
	}
	for _, args := range ok {
		if err := checkAdmin(args); err != nil {
			t.Errorf("checkAdmin(%q) = %v, want ok", args, err)
		}
	}
	bad := [][]string{
		nil,
		{"serve"},
		{"admin"},
		{"rm", "-rf", "/"},
		{"techie", "list", "--state", "/etc"},
		{"techie", "list", "-state=/etc"},
		{"vhost", "stop", "a.example.com", "--stopped", "/tmp"},
		{"vhost", "stop", "a.example.com", "--stopped", vhosts},
		{"vhost", "stop", "a.example.com", "--stopped"},
		{"vhost", "list", "--vhosts", "/etc"},
		{"vhost", "list", "--vhosts=/etc"},
		{"vhost", "list", "--vhosts"},
		{"vhost", "list", "--vhosts", vhosts + "/.."},
		{"vhost", "list", "--vhosts", ""},
		// after "--" the next value is positional, but a flag after it is read again
		{"techie", "list", "--", "x", "--vhosts", "/etc"},
		{"techie", "list", "--vhosts", vhosts, "--vhosts", "/etc"},
	}
	for _, args := range bad {
		if err := checkAdmin(args); err == nil {
			t.Errorf("checkAdmin(%q) = ok, want refused", args)
		}
	}
}

func TestCheckAdminWithoutDirs(t *testing.T) {
	t.Setenv("HELPBUOY_SERVER_VHOSTS", "")
	t.Setenv("HELPBUOY_SERVER_STOPPED", "")
	if err := checkAdmin([]string{"vhost", "list", "--vhosts", "/"}); err == nil {
		t.Error("--vhosts allowed while the service names no directories")
	}
}

func TestAdminRequests(t *testing.T) {
	vhosts, stopped := adminDirs(t)
	req := func(args ...string) string {
		b, _ := json.Marshal(args)
		return string(b) + "\n"
	}
	r := admin(t, req("vhost", "add", "--vhosts", vhosts, "a.example.com"))
	if r.Exit != 0 || !strings.Contains(r.Output, "added a.example.com") {
		t.Fatalf("vhost add: %+v", r)
	}
	r = admin(t, req("info", "--vhost", "a.example.com", "--company", "Acme", "--phone", "+1 234"))
	if r.Exit != 0 || !strings.Contains(r.Output, "Acme") {
		t.Fatalf("info: %+v", r)
	}
	// a failing command replies with its exit code and error, not an error of admin
	r = admin(t, req("techie", "remove", "--vhost", "a.example.com", "--", "nobody"))
	if r.Exit != 1 || !strings.HasPrefix(r.Output, "helpbuoy-server: ") {
		t.Errorf("techie remove nobody: %+v", r)
	}
	if r = admin(t, req("vhost", "stop", "a.example.com")); r.Exit != 0 {
		t.Fatalf("vhost stop: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(stopped, "a.example.com", "state.json")); err != nil {
		t.Errorf("not moved to the stopped directory: %v", err)
	}
	if r = admin(t, req("info", "--vhosts", stopped, "--vhost", "a.example.com")); r.Exit != 0 || !strings.Contains(r.Output, "Acme") {
		t.Errorf("info of a stopped vhost: %+v", r)
	}
	if r = admin(t, req("vhost", "start", "a.example.com")); r.Exit != 0 {
		t.Fatalf("vhost start: %+v", r)
	}
	if r = admin(t, req("vhost", "purge", "a.example.com")); r.Exit != 1 {
		t.Errorf("purged a running vhost: %+v", r)
	}
	admin(t, req("vhost", "stop", "a.example.com"))
	if r = admin(t, req("vhost", "purge", "a.example.com")); r.Exit != 0 {
		t.Fatalf("vhost purge: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(stopped, "a.example.com")); !os.IsNotExist(err) {
		t.Errorf("purge left the directory: %v", err)
	}

	for _, bad := range []string{"", "\n", "nope\n", `{"a":1}` + "\n", `["serve"]` + "\n", req("vhost", "list", "--vhosts", "/")} {
		if r := admin(t, bad); r.Exit != 2 {
			t.Errorf("request %q: %+v, want refused", bad, r)
		}
	}
	// without a newline at the end the request is read up to EOF
	if r := admin(t, `["version"]`); r.Exit != 0 || !strings.HasPrefix(r.Output, "helpbuoy-server ") {
		t.Errorf("version without newline: %+v", r)
	}
}

func TestVhostMoveRefuses(t *testing.T) {
	vhosts, stopped := adminDirs(t)
	var out bytes.Buffer
	if code := run([]string{"vhost", "stop", "a.example.com"}, &out, &out); code != 1 {
		t.Errorf("stopped a vhost that is not there: %d %s", code, out.String())
	}
	for _, name := range []string{"../x", "A_B", "."} {
		if code := run([]string{"vhost", "purge", name}, &out, &out); code == 0 {
			t.Errorf("purge %q worked", name)
		}
	}
	run([]string{"vhost", "add", "a.example.com"}, &out, &out)
	os.MkdirAll(filepath.Join(stopped, "a.example.com"), 0o700)
	if code := run([]string{"vhost", "stop", "a.example.com"}, &out, &out); code != 1 {
		t.Errorf("stopped onto a stopped vhost of the same name: %d", code)
	}
	if code := run([]string{"vhost", "stop", "a.example.com", "--stopped", vhosts}, &out, &out); code != 1 {
		t.Errorf("stopped into the vhosts directory itself: %d", code)
	}
	t.Setenv("HELPBUOY_SERVER_STOPPED", "")
	if code := run([]string{"vhost", "stop", "a.example.com"}, &out, &out); code != 2 {
		t.Errorf("stop without a stopped directory: %d", code)
	}
}

func TestLog(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	for i := range 5000 {
		lines = append(lines, fmt.Sprintf(`{"msg":"line %d","pad":"%s"}`, i, strings.Repeat("x", 40)))
	}
	if err := os.WriteFile(filepath.Join(dir, "server.log"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"log", "--state", dir, "--lines", "3"}, &out, &errOut); code != 0 {
		t.Fatalf("log: %d %s", code, errOut.String())
	}
	if want := strings.Join(lines[4997:], "\n") + "\n"; out.String() != want {
		t.Errorf("log --lines 3 = %q, want %q", out.String(), want)
	}
	out.Reset()
	run([]string{"log", "--state", dir, "--lines", "10000"}, &out, &errOut)
	if got := strings.Count(out.String(), "\n"); got != 5000 {
		t.Errorf("log of the whole file printed %d lines", got)
	}
	for _, n := range []string{"0", "10001"} {
		if code := run([]string{"log", "--state", dir, "--lines", n}, &out, &errOut); code != 2 {
			t.Errorf("log --lines %s: %d", n, code)
		}
	}
	if code := run([]string{"log", "--state", t.TempDir()}, &out, &errOut); code != 1 {
		t.Errorf("log without server.log: %d", code)
	}
}

func TestTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	for _, tt := range []struct {
		text string
		n    int
		want string
	}{
		{"", 3, ""},
		{"a\n", 3, "a\n"},
		{"a\nb\nc\n", 2, "b\nc\n"},
		{"a\nb\nc", 2, "b\nc"},
		{"a\nb\nc\n", 3, "a\nb\nc\n"},
	} {
		os.WriteFile(p, []byte(tt.text), 0o600)
		got, err := tail(p, tt.n)
		if err != nil || string(got) != tt.want {
			t.Errorf("tail(%q, %d) = %q, %v; want %q", tt.text, tt.n, got, err, tt.want)
		}
	}
}
