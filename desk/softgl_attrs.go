package main

// Attribute lists of -[NSOpenGLPixelFormat initWithAttributes:], for the
// software OpenGL fallback on Macs without Metal (softgl_darwin.go). They
// build on every OS so the tests run anywhere.

import (
	"slices"
	"unsafe"
)

// NSOpenGLPixelFormatAttribute values (AppKit NSOpenGL.h).
const (
	nsglAccelerated   = 73
	nsglSampleBuffers = 55
	nsglSamples       = 56
)

// nsglValued holds the attributes followed by a value; the others are flags.
var nsglValued = map[uint32]bool{
	7: true, 8: true, 11: true, 12: true, 13: true, 14: true, // aux buffers, color, alpha, depth, stencil, accum size
	nsglSampleBuffers: true, nsglSamples: true,
	70: true, 84: true, 99: true, 128: true, // renderer ID, screen mask, profile, virtual screen count
}

// readAttrs copies the zero-terminated attribute list at p, terminator included.
func readAttrs(p *uint32) []uint32 {
	var attrs []uint32
	for i := range 256 {
		a := *(*uint32)(unsafe.Add(unsafe.Pointer(p), 4*i))
		attrs = append(attrs, a)
		if a == 0 {
			return attrs
		}
	}
	return append(attrs, 0)
}

// withoutAttrs returns attrs without the attributes in drop and their values.
func withoutAttrs(attrs, drop []uint32) []uint32 {
	var out []uint32
	for i := 0; i < len(attrs) && attrs[i] != 0; {
		n := 1
		if nsglValued[attrs[i]] {
			n = 2
		}
		if !slices.Contains(drop, attrs[i]) {
			out = append(out, attrs[i:min(i+n, len(attrs))]...)
		}
		i += n
	}
	return append(out, 0)
}
