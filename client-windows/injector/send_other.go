//go:build !windows

package injector

import "image"

// sendInput is nil outside Windows: the Injector only decodes and logs.
var sendInput func([]event) error

// screenRects reports no screens outside Windows.
func screenRects() (virtual, primary image.Rectangle) { return }
