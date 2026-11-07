//go:build darwin

package capture

// Backend picks how frames are captured: "sck" for ScreenCaptureKit or "cg"
// for CGWindowListCreateImage. Empty, the default, means sck on Macs with a
// Metal device and cg without one: there (virtual machines) WindowServer
// crashes in WSSelectiveSharingUpdateDisplayStreamSurface as soon as a
// display stream runs. Set it before opening a monitor.
var Backend = ""

// backend resolves Backend.
func backend() string {
	if Backend != "" {
		return Backend
	}
	if hasMetal() {
		return "sck"
	}
	return "cg"
}
