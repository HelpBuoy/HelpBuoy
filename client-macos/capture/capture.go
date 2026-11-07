// Package capture reads the macOS screen and cursor (PLAN.md 5.2). On Macs
// without Metal frames come from CGWindowListCreateImage (see Backend);
// otherwise they come from ScreenCaptureKit, which only delivers a frame when something
// changed and scales it on the GPU: displays are captured at their size in
// points, not in Retina pixels, which is a quarter of the pixels to compare,
// encode and send. Monitors, the cursor and the injector's mouse positions
// all use global display points. It implements the capture.Screen of the
// Windows client, so the session code is shared.
package capture

// unpremultiply turns premultiplied RGBA into straight alpha, in place.
func unpremultiply(pix []byte) {
	for i := 0; i+3 < len(pix); i += 4 {
		a := int(pix[i+3])
		if a == 0 || a == 255 {
			continue
		}
		for j := 0; j < 3; j++ {
			pix[i+j] = byte(min(255, (int(pix[i+j])*255+a/2)/a))
		}
	}
}
