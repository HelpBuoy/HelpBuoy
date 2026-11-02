//go:build windows

package appkey

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// createExcl creates path with CREATE_NEW, failing with an error wrapping
// fs.ErrExist if it already exists. A public file is created by os.OpenFile
// and inherits the directory's ACL. A private file is created with a
// protected DACL that grants full control to the current user and SYSTEM
// only: the security descriptor is passed to CreateFile, so the file never
// exists with any other ACL, and the same DACL is applied once more through
// SetSecurityInfo with PROTECTED_DACL_SECURITY_INFORMATION so that no
// inherited entries remain.
func createExcl(path string, private bool) (*os.File, error) {
	if !private {
		return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	}
	acl, err := ownerSystemACL()
	if err != nil {
		return nil, fmt.Errorf("building ACL for %s: %w", path, err)
	}
	sd, err := windows.NewSecurityDescriptor()
	if err != nil {
		return nil, err
	}
	if err := sd.SetDACL(acl, true, false); err != nil {
		return nil, err
	}
	if err := sd.SetControl(windows.SE_DACL_PROTECTED, windows.SE_DACL_PROTECTED); err != nil {
		return nil, err
	}
	sa := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name,
		windows.GENERIC_WRITE|windows.WRITE_DAC,
		0, // no sharing while the key is being written
		sa,
		windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_NORMAL,
		0)
	if err != nil {
		// ERROR_FILE_EXISTS satisfies errors.Is(err, fs.ErrExist).
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	err = windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil)
	if err != nil {
		windows.CloseHandle(h)
		os.Remove(path)
		return nil, fmt.Errorf("setting ACL on %s: %w", path, err)
	}
	return os.NewFile(uintptr(h), path), nil
}

// fileAllAccess is FILE_ALL_ACCESS: STANDARD_RIGHTS_REQUIRED | SYNCHRONIZE |
// all file-specific rights.
const fileAllAccess = 0x001f01ff

// ownerSystemACL returns an ACL granting full control to the user of the
// current process token and to SYSTEM, and nothing else.
func ownerSystemACL() (*windows.ACL, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, err
	}
	defer token.Close()
	tu, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, err
	}
	entry := func(sid *windows.SID, typ windows.TRUSTEE_TYPE) windows.EXPLICIT_ACCESS {
		return windows.EXPLICIT_ACCESS{
			AccessPermissions: fileAllAccess,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  typ,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		}
	}
	return windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		entry(tu.User.Sid, windows.TRUSTEE_IS_USER),
		entry(system, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
	}, nil)
}
