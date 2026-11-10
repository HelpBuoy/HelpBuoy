package views

import (
	"strings"
	"testing"

	"github.com/helpbuoy/helpbuoy/proto/control"
)

func TestCheckInfo(t *testing.T) {
	ok := control.ServerInfo{Company: "Example Inc", Phone: "+1 234 567 890"}
	for _, tc := range []struct {
		name    string
		edit    func(i *control.ServerInfo)
		valid   bool
		missing bool
	}{
		{"required only", func(i *control.ServerInfo) {}, true, false},
		{"all fields", func(i *control.ServerInfo) { i.Address, i.Description = "helpme.example.com", "Weekdays 9 to 5" }, true, false},
		{"no company", func(i *control.ServerInfo) { i.Company = "" }, false, true},
		{"no phone", func(i *control.ServerInfo) { i.Phone = "" }, false, true},
		{"bad address", func(i *control.ServerInfo) { i.Address = "https://helpme.example.com/" }, false, false},
		{"long company", func(i *control.ServerInfo) { i.Company = strings.Repeat("x", control.MaxCompany+1) }, false, false},
		{"long phone", func(i *control.ServerInfo) { i.Phone = strings.Repeat("1", control.MaxPhone+1) }, false, false},
		{"long description", func(i *control.ServerInfo) { i.Description = strings.Repeat("x", control.MaxDescription+1) }, false, false},
		{"control character", func(i *control.ServerInfo) { i.Company = "Example\x07Inc" }, false, false},
	} {
		info := ok
		tc.edit(&info)
		problem, missing := checkInfo(info)
		if (problem == "") != tc.valid || missing != tc.missing {
			t.Errorf("%s: problem %q, missing %v", tc.name, problem, missing)
		}
	}
}

func TestNormAddress(t *testing.T) {
	for in, want := range map[string]string{
		"":                        "",
		"helpme.example.com":      "helpme.example.com:5959",
		"HelpMe.example.com:7000": "helpme.example.com:7000",
		"not an address":          "not an address",
	} {
		if got := normAddress(in); got != want {
			t.Errorf("normAddress(%q) = %q, want %q", in, got, want)
		}
	}
}
