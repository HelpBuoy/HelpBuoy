package main

// OpenGL on Macs without Metal. The GLFW in the embedded raylib always asks
// NSOpenGLPixelFormat for an accelerated renderer (NSOpenGLPFAAccelerated).
// A Mac without a GPU, such as a macOS VM in QEMU, has only Apple's software
// renderer, so no pixel format matches and InitWindow fails. The prebuilt
// library cannot be changed, so -[NSOpenGLPixelFormat initWithAttributes:]
// is wrapped instead: when the attributes GLFW asks for give no pixel format,
// it tries again without NSOpenGLPFAAccelerated, and then also without
// multisampling. Macs with a GPU get their pixel format on the first try.

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

func init() {
	if err := wrapPixelFormatInit(); err != nil {
		fmt.Fprintln(os.Stderr, "software OpenGL fallback:", err)
	}
}

func wrapPixelFormatInit() error {
	if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_GLOBAL|purego.RTLD_NOW); err != nil {
		return err
	}
	lib, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_GLOBAL|purego.RTLD_NOW)
	if err != nil {
		return err
	}
	var (
		getInstanceMethod func(cls objc.Class, sel objc.SEL) uintptr
		setImplementation func(m uintptr, imp objc.IMP) objc.IMP
	)
	purego.RegisterLibFunc(&getInstanceMethod, lib, "class_getInstanceMethod")
	purego.RegisterLibFunc(&setImplementation, lib, "method_setImplementation")

	cls := objc.GetClass("NSOpenGLPixelFormat")
	if cls == 0 {
		return errors.New("no NSOpenGLPixelFormat class")
	}
	sel := objc.RegisterName("initWithAttributes:")
	m := getInstanceMethod(cls, sel)
	if m == 0 {
		return errors.New("no -[NSOpenGLPixelFormat initWithAttributes:]")
	}
	alloc := objc.RegisterName("alloc")
	var orig objc.IMP
	initWith := func(self objc.ID, cmd objc.SEL, attrs []uint32) objc.ID {
		r, _, _ := purego.SyscallN(uintptr(orig), uintptr(self), uintptr(cmd), uintptr(unsafe.Pointer(&attrs[0])))
		runtime.KeepAlive(attrs)
		return objc.ID(r)
	}
	orig = setImplementation(m, objc.NewIMP(func(self objc.ID, cmd objc.SEL, p *uint32) objc.ID {
		attrs := readAttrs(p)
		c := self.Class()
		if pf := initWith(self, cmd, attrs); pf != 0 {
			return pf
		}
		// A failed init released self, so each retry starts from a new alloc.
		for _, drop := range [][]uint32{{nsglAccelerated}, {nsglAccelerated, nsglSampleBuffers, nsglSamples}} {
			if pf := initWith(objc.ID(c).Send(alloc), cmd, withoutAttrs(attrs, drop)); pf != 0 {
				fmt.Fprintln(os.Stderr, "no accelerated OpenGL renderer, using the software renderer")
				return pf
			}
		}
		return 0
	}))
	return nil
}
