//go:build !darwin

package injector

import "errors"

// newBackend returns nil: without macOS the injector only logs.
func newBackend() backend { return nil }

// InstallEscHook is not available without macOS.
func InstallEscHook(fn func()) (stop func(), err error) {
	return nil, errors.New("injector: the Esc hook needs macOS")
}
