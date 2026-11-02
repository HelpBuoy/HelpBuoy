//go:build !windows

package perfmode

import "log/slog"

// Enable does nothing outside Windows; restore is a no-op.
func Enable(log *slog.Logger) (restore func()) { return func() {} }
