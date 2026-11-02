//go:build !windows

package injector

// InstallEscHook is a no-op outside Windows: there is no local keyboard to
// watch. It exists so that platform-neutral code and tests build everywhere.
func InstallEscHook(onEsc func()) (stop func(), err error) {
	return func() {}, nil
}
