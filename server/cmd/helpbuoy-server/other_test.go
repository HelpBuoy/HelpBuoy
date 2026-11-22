package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestOtherCommands(t *testing.T) {
	dir := t.TempDir()
	_, fp := keyLine(t, "")
	_, fp2 := keyLine(t, "")
	probed := ""
	old := probeServer
	probeServer = func(addr string) (string, error) {
		probed = addr
		if strings.HasPrefix(addr, "down.") {
			return "", errors.New("connection refused")
		}
		return fp, nil
	}
	defer func() { probeServer = old }()

	// Without a fingerprint the key the server shows now is taken.
	if out := runOK(t, "other", "add", "Vendor.example.com", "Vendor Inc", "--state", dir); !strings.Contains(out, "vendor.example.com:443") || !strings.Contains(out, fp) {
		t.Errorf("other add:\n%s", out)
	}
	if probed != "vendor.example.com:443" {
		t.Errorf("probed %q", probed)
	}
	probed = ""
	runOK(t, "other", "add", "payroll.example.com:5959", "--fingerprint", fp2, "--state", dir)
	if probed != "" {
		t.Errorf("probed %q although the fingerprint was given", probed)
	}
	out := runOK(t, "other", "list", "--state", dir)
	for _, want := range []string{"vendor.example.com:443", "Vendor Inc", fp, "payroll.example.com:5959", fp2} {
		if !strings.Contains(out, want) {
			t.Errorf("other list misses %q:\n%s", want, out)
		}
	}

	runOK(t, "other", "rename", "vendor.example.com", "Vendor Ltd", "--state", dir)
	if out := runOK(t, "other", "list", "--state", dir); !strings.Contains(out, "Vendor Ltd") || strings.Contains(out, "Vendor Inc") {
		t.Errorf("after rename:\n%s", out)
	}
	runOK(t, "other", "rename", "vendor.example.com", "--none", "--state", dir)
	if out := runOK(t, "other", "list", "--state", dir); strings.Contains(out, "Vendor Ltd") {
		t.Errorf("after rename --none:\n%s", out)
	}
	runOK(t, "other", "remove", "payroll.example.com:5959", "--state", dir)
	if out := runOK(t, "other", "list", "--state", dir); strings.Contains(out, "payroll") {
		t.Errorf("after remove:\n%s", out)
	}

	for _, args := range [][]string{
		{"other"},
		{"other", "add"},
		{"other", "add", "not an address"},
		{"other", "add", "down.example.com"},
		{"other", "add", "vendor.example.com"}, // already there with this key
		{"other", "add", "new.example.com", "--fingerprint", "SHA256:short"},
		{"other", "rename", "vendor.example.com"},
		{"other", "rename", "vendor.example.com", "X", "--none"},
		{"other", "rename", "nowhere.example.com", "X"},
		{"other", "remove", "nowhere.example.com"},
	} {
		var out, errOut bytes.Buffer
		if code := run(append(args, "--state", dir), &out, &errOut); code == 0 {
			t.Errorf("run(%q) succeeded", args)
		}
	}
}
