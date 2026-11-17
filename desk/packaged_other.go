//go:build !windows

package main

// packaged reports whether the app runs from its MSIX package, which only
// exists on Windows.
func packaged() bool { return false }
