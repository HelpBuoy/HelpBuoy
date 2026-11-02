//go:build !windows

package capture

// Options tune the GDI capture.
type Options struct {
	// CaptureBlt adds CAPTUREBLT to BitBlt so layered windows are included.
	CaptureBlt bool
}

// New returns ErrUnsupported outside Windows.
func New(opts Options) (Screen, error) { return nil, ErrUnsupported }
