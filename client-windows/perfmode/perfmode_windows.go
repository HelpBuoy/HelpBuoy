//go:build windows

package perfmode

import (
	"log/slog"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procSystemParametersInfoW = windows.NewLazySystemDLL("user32.dll").NewProc("SystemParametersInfoW")

const (
	spiGetDeskWallpaper       = 0x0073
	spiSetDeskWallpaper       = 0x0014
	spiGetAnimation           = 0x0048
	spiSetAnimation           = 0x0049
	spiGetDragFullWindows     = 0x0026
	spiSetDragFullWindows     = 0x0025
	spiGetMenuAnimation       = 0x1002
	spiSetMenuAnimation       = 0x1003
	spiGetClientAreaAnimation = 0x1042
	spiSetClientAreaAnimation = 0x1043

	// spifSendChange broadcasts the change; SPIF_UPDATEINIFILE is left out
	// on purpose so nothing is persisted.
	spifSendChange = 0x0002
)

type animationInfo struct {
	Size       uint32
	MinAnimate int32
}

func spi(action, uiParam uint32, pv unsafe.Pointer, winIni uint32) bool {
	r, _, _ := procSystemParametersInfoW.Call(uintptr(action), uintptr(uiParam), uintptr(pv), uintptr(winIni))
	return r != 0
}

// spiValue is spi for settings that take their value in pvParam itself.
func spiValue(action uint32, value uintptr, winIni uint32) bool {
	r, _, _ := procSystemParametersInfoW.Call(uintptr(action), 0, value, uintptr(winIni))
	return r != 0
}

func getBool(action uint32) (bool, bool) {
	var v int32
	ok := spi(action, 0, unsafe.Pointer(&v), 0)
	return v != 0, ok
}

func boolParam(b bool) uintptr {
	if b {
		return 1
	}
	return 0
}

// Enable switches performance mode on and returns the function that puts
// every changed setting back. restore is safe to call more than once.
func Enable(log *slog.Logger) (restore func()) {
	var undo []func()

	// Wallpaper: an empty path leaves the solid background color.
	buf := make([]uint16, 1024)
	if spi(spiGetDeskWallpaper, uint32(len(buf)), unsafe.Pointer(&buf[0]), 0) {
		old := windows.UTF16ToString(buf)
		if old != "" {
			empty := []uint16{0}
			if spi(spiSetDeskWallpaper, 0, unsafe.Pointer(&empty[0]), spifSendChange) {
				undo = append(undo, func() {
					p, err := windows.UTF16PtrFromString(old)
					if err == nil {
						spi(spiSetDeskWallpaper, 0, unsafe.Pointer(p), spifSendChange)
					}
				})
			}
		}
	}

	// Minimize and restore animation.
	ai := animationInfo{Size: uint32(unsafe.Sizeof(animationInfo{}))}
	if spi(spiGetAnimation, ai.Size, unsafe.Pointer(&ai), 0) && ai.MinAnimate != 0 {
		off := animationInfo{Size: ai.Size}
		if spi(spiSetAnimation, ai.Size, unsafe.Pointer(&off), spifSendChange) {
			old := ai
			undo = append(undo, func() { spi(spiSetAnimation, old.Size, unsafe.Pointer(&old), spifSendChange) })
		}
	}

	// Settings whose value is passed in pvParam.
	for _, s := range []struct{ get, set uint32 }{
		{spiGetClientAreaAnimation, spiSetClientAreaAnimation},
		{spiGetMenuAnimation, spiSetMenuAnimation},
	} {
		if on, ok := getBool(s.get); ok && on {
			if spiValue(s.set, boolParam(false), spifSendChange) {
				set := s.set
				undo = append(undo, func() { spiValue(set, boolParam(true), spifSendChange) })
			}
		}
	}

	// Full-window dragging takes its value in uiParam.
	if on, ok := getBool(spiGetDragFullWindows); ok && on {
		if spi(spiSetDragFullWindows, 0, nil, spifSendChange) {
			undo = append(undo, func() { spi(spiSetDragFullWindows, 1, nil, spifSendChange) })
		}
	}

	if log != nil {
		log.Info("performance mode on", "changed", len(undo))
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for i := len(undo) - 1; i >= 0; i-- {
				undo[i]()
			}
			if log != nil {
				log.Info("performance mode off")
			}
		})
	}
}
