package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCustomerCommands(t *testing.T) {
	dir := t.TempDir()
	runOK(t, "customer", "add", "Acme BV", "--state", dir)
	runOK(t, "customer", "add", "Other", "--state", dir)
	line, fp := keyLine(t, "anna@PC")
	runOK(t, "computer", "add", line, "--customer", "acme bv", "--state", dir)
	line2, fp2 := keyLine(t, "bob@PC")
	runOK(t, "computer", "add", line2, "--state", dir)

	if out := runOK(t, "computer", "list", "--customer", "Acme BV", "--state", dir); !strings.Contains(out, fp) || strings.Contains(out, fp2) {
		t.Errorf("computer list --customer:\n%s", out)
	}
	if out := runOK(t, "customer", "list", "--state", dir); !strings.Contains(out, "Acme BV") || !strings.Contains(out, "(no customer)") {
		t.Errorf("customer list:\n%s", out)
	}
	runOK(t, "computer", "move", fp2, "Other", "--state", dir)
	runOK(t, "computer", "move", fp, "--none", "--state", dir)
	if out := runOK(t, "computer", "list", "--customer", "Other", "--state", dir); !strings.Contains(out, fp2) || strings.Contains(out, fp) {
		t.Errorf("after move:\n%s", out)
	}

	tl, _ := keyLine(t, "")
	runOK(t, "techie", "add", "carol", tl, "--customer", "Acme BV", "--customer", "Other", "--state", dir)
	if out := runOK(t, "techie", "list", "--state", dir); !strings.Contains(out, "Acme BV, Other") {
		t.Errorf("techie list:\n%s", out)
	}
	runOK(t, "customer", "rename", "acme bv", "Acme Holding", "--state", dir)
	runOK(t, "customer", "remove", "Other", "--state", dir)
	if out := runOK(t, "techie", "limit", "carol", "--all", "--state", dir); !strings.Contains(out, "reaches all") {
		t.Errorf("techie limit --all: %s", out)
	}
	if out := runOK(t, "techie", "limit", "carol", "Acme Holding", "--state", dir); !strings.Contains(out, "reaches Acme Holding") {
		t.Errorf("techie limit: %s", out)
	}

	for _, args := range [][]string{
		{"customer", "add", "acme holding"},
		{"customer", "remove", "Nobody"},
		{"computer", "move", fp, "Nobody"},
		{"techie", "limit", "carol"},
		{"techie", "limit", "carol", "x", "--all"},
		{"computer", "move", fp},
	} {
		var out, errOut bytes.Buffer
		if code := run(append(args, "--state", dir), &out, &errOut); code == 0 {
			t.Errorf("run(%q) succeeded", args)
		}
	}
}
