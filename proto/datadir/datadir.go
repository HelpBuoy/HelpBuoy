// Package datadir returns the per-user directory where helpbuoy programs
// keep their keys and state:
//
//	Linux:   $XDG_DATA_HOME/<name>, default ~/.local/share/<name>
//	macOS:   ~/Library/Application Support/<name>
//	Windows: %LOCALAPPDATA%\<name>
//
// The directory is local to the machine on purpose (not roaming), so a key
// never follows a user to another PC.
package datadir

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// Dir returns the data directory for the program name. It does not create it.
func Dir(name string) (string, error) {
	return dirFor(runtime.GOOS, os.Getenv, os.UserHomeDir, name)
}

func dirFor(goos string, getenv func(string) string, home func() (string, error), name string) (string, error) {
	if name == "" {
		return "", errors.New("datadir: empty program name")
	}
	switch goos {
	case "windows":
		base := getenv("LOCALAPPDATA")
		if base == "" {
			return "", errors.New("datadir: LOCALAPPDATA is not set")
		}
		return filepath.Join(base, name), nil
	case "darwin", "ios":
		h, err := home()
		if err != nil {
			return "", err
		}
		return filepath.Join(h, "Library", "Application Support", name), nil
	default:
		if x := getenv("XDG_DATA_HOME"); x != "" && filepath.IsAbs(x) {
			return filepath.Join(x, name), nil
		}
		h, err := home()
		if err != nil {
			return "", err
		}
		return filepath.Join(h, ".local", "share", name), nil
	}
}

// Migrate returns the data directory for name like Dir, after moving the
// directory of the program's former name (legacy) to it when only the old
// one exists. If the move fails, it returns the old directory so existing
// keys keep working.
func Migrate(name, legacy string) (string, error) {
	dir, err := Dir(name)
	if err != nil {
		return "", err
	}
	old, err := Dir(legacy)
	if err != nil {
		return dir, nil
	}
	return migrate(dir, old), nil
}

func migrate(dir, old string) string {
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		return dir
	}
	if fi, err := os.Stat(old); err != nil || !fi.IsDir() {
		return dir
	}
	if err := os.Rename(old, dir); err != nil {
		return old
	}
	return dir
}
