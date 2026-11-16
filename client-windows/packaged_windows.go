//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// appmodelErrorNoPackage is what GetCurrentPackageFullName returns for a
// process that runs without package identity, such as one installed by the MSI.
const appmodelErrorNoPackage = 15700

var procGetCurrentPackageFullName = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetCurrentPackageFullName")

// packaged reports whether the app runs from its MSIX package (the Microsoft
// Store version). The Store updates that version itself, so it must not
// download and run the MSI.
func packaged() bool {
	if procGetCurrentPackageFullName.Find() != nil {
		return false
	}
	var n uint32
	r, _, _ := procGetCurrentPackageFullName.Call(uintptr(unsafe.Pointer(&n)), 0)
	return r != appmodelErrorNoPackage
}
