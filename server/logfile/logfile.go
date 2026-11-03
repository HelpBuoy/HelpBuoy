// Package logfile is an append-only log file that rotates itself: when it
// grows past a size limit it is renamed to NAME.1 (replacing an older one)
// and a new file is started.
package logfile

import (
	"os"
	"sync"
)

// DefaultMaxSize is the rotation size from PLAN 3.4.
const DefaultMaxSize = 10 << 20

// File is a rotating log file. It is safe for concurrent use.
type File struct {
	path string
	max  int64

	mu   sync.Mutex
	f    *os.File
	size int64
}

// Open opens (or creates) the log file at path, rotating at max bytes.
func Open(path string, max int64) (*File, error) {
	if max <= 0 {
		max = DefaultMaxSize
	}
	l := &File{path: path, max: max}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *File) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, fi.Size()
	return nil
}

// Write appends p, rotating first if p would take the file past the limit.
func (l *File) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return 0, os.ErrClosed
	}
	if l.size > 0 && l.size+int64(len(p)) > l.max {
		l.f.Close()
		os.Rename(l.path, l.path+".1")
		if err := l.open(); err != nil {
			l.f = nil
			return 0, err
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

// Close closes the file.
func (l *File) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
