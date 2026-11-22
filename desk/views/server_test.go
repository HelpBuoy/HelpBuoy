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
		{"all fields", func(i *control.ServerInfo) { i.Description = "Weekdays 9 to 5" }, true, false},
		{"no company", func(i *control.ServerInfo) { i.Company = "" }, false, true},
		{"no phone", func(i *control.ServerInfo) { i.Phone = "" }, false, true},
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

func TestCheckApproved(t *testing.T) {
	for _, tc := range []struct {
		name, label, address string
		want                 string // normalized address, "" when refused
		empty                bool
	}{
		{"host", " Vendor Inc ", " Vendor.example.com ", "vendor.example.com:443", false},
		{"port", "", "vendor.example.com:5959", "vendor.example.com:5959", false},
		{"nothing typed", "", "", "", true},
		{"only a name", "Vendor Inc", "", "", true},
		{"bad address", "", "not an address", "", false},
		{"long name", strings.Repeat("x", control.MaxApprovedName+1), "vendor.example.com", "", false},
	} {
		s, problem, empty := checkApproved(tc.label, tc.address)
		if s.Address != tc.want || empty != tc.empty || (problem == "") != (tc.want != "" || tc.empty) {
			t.Errorf("%s: %+v, problem %q, empty %v", tc.name, s, problem, empty)
		}
		if tc.want != "" && s.Name != strings.TrimSpace(tc.label) {
			t.Errorf("%s: name %q", tc.name, s.Name)
		}
	}
}
