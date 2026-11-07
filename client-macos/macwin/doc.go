// Package macwin is the AppKit side of the macOS client's window: an
// NSWindow with one layer-backed view that shows the pixels the shared
// window (client-linux/ui) draws, and passes keys and mouse events back.
// AppKit only works on the main thread, so the program's main goroutine
// runs the AppKit loop (Main) and the window's goroutine hands work over
// with dispatch_async.
package macwin
