package datadir

import (
	"os"
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
		{"linux", nil, filepath.Join("/home/anna", ".local", "share", "helpbuoy-relay")},
		{"linux", map[string]string{"XDG_DATA_HOME": "/data"}, filepath.Join("/data", "helpbuoy-relay")},
		{"linux", map[string]string{"XDG_DATA_HOME": "relative"}, filepath.Join("/home/anna", ".local", "share", "helpbuoy-relay")},
		{"darwin", nil, filepath.Join("/home/anna", "Library", "Application Support", "helpbuoy-relay")},
		{"windows", map[string]string{"LOCALAPPDATA": `C:\Users\anna\AppData\Local`}, filepath.Join(`C:\Users\anna\AppData\Local`, "helpbuoy-relay")},
	}
	for _, tt := range tests {
		got, err := dirFor(tt.goos, env(tt.env), home, "helpbuoy-relay")
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
	if _, err := dirFor("windows", none, home, "helpbuoy-relay"); err == nil {
		t.Error("windows without LOCALAPPDATA: want error")
	}
	if _, err := dirFor("linux", none, home, ""); err == nil {
		t.Error("empty name: want error")
	}
}

func TestMigrate(t *testing.T) {
	base := t.TempDir()
	dir, old := filepath.Join(base, "new"), filepath.Join(base, "old")

	// Neither exists: the new directory, nothing created.
	if got := migrate(dir, old); got != dir {
		t.Fatalf("none: got %q", got)
	}

	// Only the old one exists: it is moved.
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "key"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := migrate(dir, old); got != dir {
		t.Fatalf("old only: got %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "key")); err != nil {
		t.Fatalf("key not moved: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old dir still there: %v", err)
	}

	// Both exist: the new one wins and the old one is left alone.
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := migrate(dir, old); got != dir {
		t.Fatalf("both: got %q", got)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("old dir touched: %v", err)
	}
}
