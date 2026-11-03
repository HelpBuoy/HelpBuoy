package logfile

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRotate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "server.log")
	f, err := Open(p, 100)
	if err != nil {
		t.Fatal(err)
	}
	line := bytes.Repeat([]byte("x"), 39)
	line = append(line, '\n')
	for range 5 {
		if _, err := f.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	cur, _ := os.ReadFile(p)
	old, _ := os.ReadFile(p + ".1")
	if len(cur) != 40 || len(old) != 80 {
		t.Errorf("current %d bytes, rotated %d bytes", len(cur), len(old))
	}
	// Reopening appends.
	f, _ = Open(p, 100)
	f.Write(line)
	f.Close()
	if cur, _ := os.ReadFile(p); len(cur) != 80 {
		t.Errorf("after reopen %d bytes", len(cur))
	}
}
