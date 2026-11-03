package identity

import (
	"strings"
	"testing"
)

func TestUserMachine(t *testing.T) {
	um := UserMachine()
	if !strings.Contains(um, "@") || strings.HasPrefix(um, "@") {
		t.Errorf("UserMachine = %q", um)
	}
}

func TestTechieName(t *testing.T) {
	if n := TechieName(); !validName(n) {
		t.Errorf("TechieName = %q is not valid", n)
	}
	for s, want := range map[string]bool{"anna": true, "a.b-c_1": true, "": false, "Anna": false, "a b": false} {
		if validName(s) != want {
			t.Errorf("validName(%q) = %v", s, !want)
		}
	}
}

func TestDir(t *testing.T) {
	d, err := Dir()
	if err != nil {
		t.Skip(err)
	}
	if !strings.HasSuffix(d, AppName) {
		t.Errorf("Dir = %q", d)
	}
}
