//go:build !windows

// Command helpbuoy is the Windows client. On other systems it only says so,
// so that "go build ./..." and "go vet ./..." work everywhere.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "HelpBuoy: this client runs on Windows only")
	os.Exit(1)
}
