//go:build windows

package fileio

import (
	"golang.org/x/sys/windows"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretDACLDoesNotInheritBroadAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := WritePrivateBytes(path, []byte("private fixture")); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	text := sd.String()
	if !strings.Contains(text, "D:P") || strings.Contains(text, ";;;WD)") || strings.Contains(text, ";;;BU)") || strings.Contains(text, ";;;AU)") {
		t.Fatal("secret DACL permits broad/inherited access", text)
	}
}
