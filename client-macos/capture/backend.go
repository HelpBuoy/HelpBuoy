package capture

import "runtime"

// Backend picks how frames are captured: "sck" for ScreenCaptureKit or "cg"
// for CGDisplayCreateImage. ScreenCaptureKit crashes WindowServer on some
// Intel Macs with macOS 14 (in WSSelectiveSharingUpdateDisplayStreamSurface,
// as soon as the stream runs), so Intel Macs use CoreGraphics. Set it before
// opening a monitor.
var Backend = defaultBackend()

func defaultBackend() string {
	if runtime.GOARCH == "amd64" {
		return "cg"
	}
	return "sck"
}
