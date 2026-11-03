//go:build !windows

// Command help-me-win is the Windows client. On other systems it only says so,
// so that "go build ./..." and "go vet ./..." work everywhere.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "Help-Me-Win: this client runs on Windows only")
	os.Exit(1)
}
