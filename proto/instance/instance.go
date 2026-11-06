// Package instance keeps an app to one running copy per data directory. Two
// copies would share the app key, and the server allows one connection per
// key, so they would keep pushing each other off and end each other's
// sessions.
//
// The lock is a file in the data directory, which is per user, so every user
// on a shared machine can run their own copy. The operating system drops the
// lock when the process ends, also after a crash, so a stale file never
// blocks a start. The file holds the owner's process id, so a second start
// can bring the running copy's window to the front.
package instance

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrRunning means another process holds the lock for this data directory.
var ErrRunning = errors.New("instance: the app is already running")

// Lock is a held lock; release it with Unlock or by ending the process.
type Lock struct {
	f *os.File
}

// Acquire takes the lock file name in dir, creating dir if needed, and
// records this process id in it. It returns ErrRunning when another process
// holds the lock.
func Acquire(dir, name string) (*Lock, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := lockFile(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	if err := f.Truncate(0); err == nil {
		f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return &Lock{f: f}, nil
}

// Unlock releases the lock.
func (l *Lock) Unlock() error { return l.f.Close() }

// Owner returns the process id recorded in the lock file name in dir, or 0
// if there is none. Use it after Acquire returned ErrRunning.
func Owner(dir, name string) int {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
