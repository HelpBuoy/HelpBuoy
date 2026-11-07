//go:build !darwin

// Command helpbuoy is the macOS client; see main.go. On other systems it
// only says so, which keeps the module buildable for vet and tests.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "helpbuoy: this is the macOS client")
	os.Exit(1)
}
