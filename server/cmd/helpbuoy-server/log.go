package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// maxLogLines is the most lines log prints.
const maxLogLines = 10000

// logCmd prints the last lines of the server.log in the state directory.
func logCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	fs.SetOutput(stderr)
	state := addStateFlags(fs)
	lines := fs.Int("lines", 200, "number of lines to print")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || *lines < 1 || *lines > maxLogLines {
		return fmt.Errorf("%w: log [--lines N], N from 1 to %d", errUsage, maxLogLines)
	}
	dir, err := state.dir()
	if err != nil {
		return err
	}
	b, err := tail(filepath.Join(dir, "server.log"), *lines)
	if err != nil {
		return err
	}
	_, err = stdout.Write(b)
	return err
}

// tail returns the last n lines of a file, reading it backwards in blocks.
func tail(name string, n int) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	const block = 64 << 10
	var buf []byte
	for pos := size; pos > 0; {
		step := min(int64(block), pos)
		pos -= step
		chunk := make([]byte, step)
		if _, err := f.ReadAt(chunk, pos); err != nil {
			return nil, err
		}
		buf = append(chunk, buf...)
		// one newline more than n: the one that ends the line before them,
		// not counting the one that ends the file
		if bytes.Count(bytes.TrimSuffix(buf, []byte("\n")), []byte("\n")) >= n {
			break
		}
	}
	trimmed := bytes.TrimSuffix(buf, []byte("\n"))
	for i := len(trimmed) - 1; i >= 0; i-- {
		if trimmed[i] == '\n' {
			if n--; n == 0 {
				return buf[i+1:], nil
			}
		}
	}
	return buf, nil
}
