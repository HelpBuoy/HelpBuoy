package datadir

import (
	"path/filepath"
	"testing"
)

func TestDirFor(t *testing.T) {
	home := func() (string, error) { return "/home/anna", nil }
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	tests := []struct {
		goos string
		env  map[string]string
		want string
	}{
		{"linux", nil, filepath.Join("/home/anna", ".local", "share", "helpbuoy-server")},
		{"linux", map[string]string{"XDG_DATA_HOME": "/data"}, filepath.Join("/data", "helpbuoy-server")},
		{"linux", map[string]string{"XDG_DATA_HOME": "relative"}, filepath.Join("/home/anna", ".local", "share", "helpbuoy-server")},
		{"darwin", nil, filepath.Join("/home/anna", "Library", "Application Support", "helpbuoy-server")},
		{"windows", map[string]string{"LOCALAPPDATA": `C:\Users\anna\AppData\Local`}, filepath.Join(`C:\Users\anna\AppData\Local`, "helpbuoy-server")},
	}
	for _, tt := range tests {
		got, err := dirFor(tt.goos, env(tt.env), home, "helpbuoy-server")
		if err != nil {
			t.Errorf("%s %v: %v", tt.goos, tt.env, err)
			continue
		}
		if got != tt.want {
			t.Errorf("%s %v: got %q, want %q", tt.goos, tt.env, got, tt.want)
		}
	}
}

func TestDirForErrors(t *testing.T) {
	home := func() (string, error) { return "/home/anna", nil }
	none := func(string) string { return "" }
	if _, err := dirFor("windows", none, home, "helpbuoy-server"); err == nil {
		t.Error("windows without LOCALAPPDATA: want error")
	}
	if _, err := dirFor("linux", none, home, ""); err == nil {
		t.Error("empty name: want error")
	}
}
