//go:build windows

package appkey

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestPrivateKeyACL checks the DACL of a new private key file. Wine
// synthesizes file security from Unix permissions, so it is skipped there.
func TestPrivateKeyACL(t *testing.T) {
	if windows.NewLazySystemDLL("ntdll.dll").NewProc("wine_get_version").Find() == nil {
		t.Skip("running under Wine, which does not store file ACLs")
	}
	dir := t.TempDir()
	if _, _, err := LoadOrCreate(dir, "a@b"); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(filepath.Join(dir, PrivateFile),
		windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	ctrl, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if ctrl&windows.SE_DACL_PROTECTED == 0 {
		t.Errorf("DACL is not protected: %s", sd)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl.AceCount != 2 {
		t.Errorf("DACL has %d entries, want 2: %s", dacl.AceCount, sd)
	}
	s := sd.String()
	if !strings.Contains(s, ";;;SY)") || strings.Contains(s, ";;;BA)") || strings.Contains(s, ";;;WD)") || strings.Contains(s, ";;;BU)") {
		t.Errorf("unexpected DACL %s", s)
	}
	t.Logf("DACL: %s", s)
}
