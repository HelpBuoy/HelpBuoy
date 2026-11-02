//go:build !windows

package appkey

import "os"

// createExcl creates path with O_CREATE|O_EXCL, failing with an error
// wrapping fs.ErrExist if it already exists. A private file gets mode 0600,
// a public one 0644 (both subject to the umask).
func createExcl(path string, private bool) (*os.File, error) {
	perm := os.FileMode(0o644)
	if private {
		perm = 0o600
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
}
