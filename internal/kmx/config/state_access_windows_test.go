//go:build windows

package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
	"golang.org/x/sys/windows"
)

func windowsPrivateStateParent(t *testing.T) string {
	t.Helper()
	temporary, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() + "D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(temporary, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		user.User.Sid, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(temporary, "private-parent")
	directory, err := fileaccess.EnsurePrivateDirectory(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	return base
}

func stateSecurity(t *testing.T, path string) string {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	return descriptor.String()
}

func requirePrivateStateSecurity(t *testing.T, path string) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.Equals(user.User.Sid) {
		t.Fatalf("state owner = %v, %v", owner, err)
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("state DACL is not protected: %#x, %v", control, err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 {
		t.Fatalf("state DACL = %v, %v", dacl, err)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !sid.Equals(user.User.Sid) {
		t.Fatalf("state DACL grants a non-owner: %s", descriptor)
	}
}

func broadenStateSecurity(t *testing.T, path string) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() +
		"D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;;FR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsStateAccessUsesLocalAppDataAndSeparatesCreation(t *testing.T) {
	local := windowsPrivateStateParent(t)
	t.Setenv("KMX_HOME", "")
	t.Setenv("LOCALAPPDATA", local)
	home := filepath.Join(local, "kmx")
	if got, err := StateDir(); err != nil || got != home {
		t.Fatalf("StateDir = %q, %v", got, err)
	}
	if _, err := ExistingStateDir(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent ExistingStateDir = %v", err)
	}
	if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only lookup created state: %v", err)
	}
	if got, err := PrepareStateDir(); err != nil || got != home {
		t.Fatalf("PrepareStateDir = %q, %v", got, err)
	}
	requirePrivateStateSecurity(t, home)
	if got, err := ExistingStateDir(); err != nil || got != home {
		t.Fatalf("ExistingStateDir = %q, %v", got, err)
	}
}

func TestWindowsStateAccessRefusesUnsafeRootWithoutRepair(t *testing.T) {
	parent := windowsPrivateStateParent(t)
	home := filepath.Join(parent, "state")
	t.Setenv("KMX_HOME", home)
	if _, err := PrepareStateDir(); err != nil {
		t.Fatal(err)
	}
	broadenStateSecurity(t, home)
	before := stateSecurity(t, home)
	for _, resolve := range []func() (string, error){ExistingStateDir, PrepareStateDir} {
		if _, err := resolve(); err == nil {
			t.Fatal("unsafe state root accepted")
		}
	}
	if after := stateSecurity(t, home); after != before {
		t.Fatalf("unsafe state root was repaired: %s -> %s", before, after)
	}
}
