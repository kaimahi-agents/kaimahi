//go:build windows

package fileaccess

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// rawTokenSID is deliberately independent of production tokenDefaultOwner and
// currentUser. TOKEN_OWNER and TOKEN_USER both begin with a SID pointer.
func rawTokenSID(class uint32) (*windows.SID, error) {
	token := windows.GetCurrentProcessToken()
	var size uint32
	err := windows.GetTokenInformation(token, class, nil, 0, &size)
	if err != windows.ERROR_INSUFFICIENT_BUFFER || size == 0 {
		return nil, fmt.Errorf("token sizing class %d: %v", class, err)
	}
	buf := make([]byte, size)
	if err := windows.GetTokenInformation(token, class, &buf[0], size, &size); err != nil {
		return nil, err
	}
	sid := *(**windows.SID)(unsafe.Pointer(&buf[0]))
	if sid == nil || !sid.IsValid() {
		return nil, fmt.Errorf("invalid token SID class %d", class)
	}
	return sid.Copy()
}

func faUser(t *testing.T) string {
	t.Helper()
	sid, err := rawTokenSID(windows.TokenUser)
	if err != nil {
		t.Fatal(err)
	}
	return sid.String()
}

// Private fixture authority is test-only: supply the explicit user owner and
// protected DACL in SECURITY_ATTRIBUTES before any content is written.
// Inheritance fixtures supply only the owner and deliberately inherit their
// disposable parent's DACL; they contain no private content. No production
// creator/checker, privileges, token mutation or owner repair is involved.
func fixtureSD(protected bool) (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := rawTokenSID(windows.TokenUser)
	if err != nil {
		return nil, err
	}
	if !protected {
		return windows.SecurityDescriptorFromString("O:" + user.String())
	}
	return windows.SecurityDescriptorFromString("O:" + user.String() + "D:P(A;OICI;FA;;;" + user.String() + ")")
}

func fixtureCreateDirectory(path string, protected bool) error {
	sd, err := fixtureSD(protected)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	return windows.CreateDirectory(name, &sa)
}

func fixtureMkdir(path string, _ os.FileMode) error { return fixtureCreateDirectory(path, true) }
func faMkdir(t *testing.T, path string) string {
	t.Helper()
	if err := fixtureMkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
func fixtureInheritedMkdir(t *testing.T, path string) string {
	t.Helper()
	if err := fixtureCreateDirectory(path, false); err != nil {
		t.Fatal(err)
	}
	return path
}
func faPrivateDir(t *testing.T) string {
	t.Helper()
	path := faMkdir(t, filepath.Join(t.TempDir(), "private"))
	// Independently inspect the OS result, rather than letting the new checker
	// be the fixture's only oracle.
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatal(err)
	}
	user, err := rawTokenSID(windows.TokenUser)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if !sd.IsValid() || owner == nil || !owner.Equals(user) || control&windows.SE_DACL_PROTECTED == 0 || dacl == nil || dacl.AceCount != 1 {
		t.Fatalf("independent private fixture descriptor: %s", sd.String())
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE || ace.Mask != 0x001f01ff || !(*windows.SID)(unsafe.Pointer(&ace.SidStart)).Equals(user) {
		t.Fatalf("independent private fixture ACE: %s", sd.String())
	}
	return path
}
func fixtureWrite(path string, data []byte, protected bool) error {
	sd, err := fixtureSD(protected)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	handle, err := windows.CreateFile(name, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		&sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(handle), path)
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
func faWriteFile(path string, data []byte, _ os.FileMode) error {
	return fixtureWrite(path, data, true)
}
func fixtureInheritedWrite(path string) error { return fixtureWrite(path, nil, false) }

// openNoFollow uses raw Win32 opens to inspect the named object, not its target.
func openNoFollow(t *testing.T, path string) windows.Handle {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatalf("CreateFile %s: %v", path, err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(handle) })
	return handle
}
