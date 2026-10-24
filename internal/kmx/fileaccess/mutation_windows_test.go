//go:build windows

package fileaccess

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func makeExistingPrivateUnsafe(t *testing.T, path string) error {
	t.Helper()
	setTestACL(t, path, "P(A;;FA;;;"+windowsTestUser(t)+")(A;;FR;;;WD)", true)
	return nil
}

func mutationTestBase(t *testing.T) string {
	t.Helper()
	base := foundationTempDir(t)
	setTestACL(t, base, "P(A;OICI;FA;;;"+windowsTestUser(t)+")", true)
	return base
}

func TestWindowsPrivateCreationUsesExplicitProtectedDescriptor(t *testing.T) {
	user := windowsTestUser(t)
	base := foundationTempDir(t)
	path := filepath.Join(base, "broad-parent")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	// The Everyone grant applies only to children. The parent itself remains
	// safe, while a creator that relied on inheritance would produce a broad
	// child descriptor.
	setTestACL(t, path, "P(A;OICI;FA;;;"+user+")(A;OICIIO;FA;;;WD)", true)
	directory := openTestDirectory(t, path)
	file, err := CreatePrivateFileAt(t.Context(), directory, "file")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	if err := checkPrivatePath(t, filepath.Join(path, "file"), false); err != nil {
		t.Fatalf("created file descriptor = %v", err)
	}
	requireExplicitPrivateDescriptor(t, filepath.Join(path, "file"), false)
	child, err := CreatePrivateDirectoryAt(t.Context(), directory, "child")
	if err != nil {
		t.Fatal(err)
	}
	child.Close()
	if err := checkPrivatePath(t, filepath.Join(path, "child"), true); err != nil {
		t.Fatalf("created directory descriptor = %v", err)
	}
	requireExplicitPrivateDescriptor(t, filepath.Join(path, "child"), true)
}

func requireExplicitPrivateDescriptor(t *testing.T, path string, directory bool) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.Equals(user.User.Sid) {
		t.Fatalf("owner = %v, %v", owner, err)
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("DACL control = %#x, %v", control, err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 {
		t.Fatalf("DACL = %v, %v (%s)", dacl, err, descriptor)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	wantFlags := uint8(0)
	if directory {
		wantFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !sid.Equals(user.User.Sid) || ace.Header.AceFlags != wantFlags {
		t.Fatalf("created descriptor is not explicit owner-only: %s", descriptor)
	}
	const fileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
	if ace.Mask != fileAllAccess {
		t.Fatalf("created descriptor mask = %#x, want full access %#x", ace.Mask, fileAllAccess)
	}
}

func TestWindowsPrivateExistingObjectsRefuseWithoutRepair(t *testing.T) {
	path, directory := privateTestDirectory(t)
	file, err := CreatePrivateFileAt(t.Context(), directory, "file")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	filePath := filepath.Join(path, "file")
	if err := os.WriteFile(filePath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	setTestACL(t, filePath, "P(A;;FA;;;"+windowsTestUser(t)+")(A;;FR;;;WD)", true)
	before := securitySnapshot(t, filePath)
	if opened, err := EnsurePrivateFileAt(t.Context(), directory, "file"); !errors.Is(err, ErrUnsafe) {
		if opened != nil {
			opened.Close()
		}
		t.Fatalf("broad existing file = %v", err)
	}
	if after := securitySnapshot(t, filePath); after != before {
		t.Fatalf("broad existing file was repaired: %s -> %s", before, after)
	}
	if data, err := os.ReadFile(filePath); err != nil || string(data) != "keep" {
		t.Fatalf("broad existing file bytes = %q, %v", data, err)
	}

	user := windowsTestUser(t)
	descriptor, err := windows.SecurityDescriptorFromString("O:" + user + "D:P(A;;FR;;;" + user + ")")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(filePath, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	before = securitySnapshot(t, filePath)
	if opened, err := EnsurePrivateFileAt(t.Context(), directory, "file"); !errors.Is(err, ErrUnreadable) {
		if opened != nil {
			opened.Close()
		}
		t.Fatalf("read-only existing file = %v", err)
	}
	if after := securitySnapshot(t, filePath); after != before {
		t.Fatalf("read-only existing file was repaired: %s -> %s", before, after)
	}
	if data, err := os.ReadFile(filePath); err != nil || string(data) != "keep" {
		t.Fatalf("read-only existing file bytes = %q, %v", data, err)
	}
}

func TestWindowsSyncDirectoryExecutesFlushAndRefusesReadOnlyDACL(t *testing.T) {
	path, directory := privateTestDirectory(t)
	oldFlush := flushDirectoryHandle
	injected := errors.New("injected directory flush failure")
	calls := 0
	flushDirectoryHandle = func(windows.Handle) error {
		calls++
		return injected
	}
	if err := SyncDirectory(t.Context(), directory); !errors.Is(err, ErrUnreadable) || errors.Is(err, injected) {
		t.Fatalf("injected directory flush = %v", err)
	}
	flushDirectoryHandle = oldFlush
	t.Cleanup(func() { flushDirectoryHandle = oldFlush })
	if calls != 1 {
		t.Fatalf("directory flush calls = %d, want 1", calls)
	}

	user := windowsTestUser(t)
	setTestACL(t, path, "P(A;;FR;;;"+user+")", true)
	before := securitySnapshot(t, path)
	if err := SyncDirectory(t.Context(), directory); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("read-only directory sync = %v", err)
	}
	if after := securitySnapshot(t, path); after != before {
		t.Fatalf("sync refusal repaired ACL: %s -> %s", before, after)
	}
}

func TestWindowsLinkErrorAfterEffectReconcilesWithoutCallerContext(t *testing.T) {
	path, directory := privateTestDirectory(t)
	ctx, cancel := context.WithCancel(context.Background())
	oldSet := setPrivateLinkInformation
	setPrivateLinkInformation = func(handle windows.Handle, status *windows.IO_STATUS_BLOCK, info *byte, size uint32) error {
		err := oldSet(handle, status, info, size)
		if err == nil {
			cancel()
			return windows.ERROR_GEN_FAILURE
		}
		return err
	}
	t.Cleanup(func() { setPrivateLinkInformation = oldSet })
	result, err := PublishPrivate(ctx, directory, "result", []byte("complete"))
	if err == nil || !result.Visible || !result.VisibilityKnown || errors.Is(err, context.Canceled) {
		t.Fatalf("post-effect native link error = %+v, %v", result, err)
	}
	data, readErr := os.ReadFile(filepath.Join(path, "result"))
	if readErr != nil || string(data) != "complete" {
		t.Fatalf("reconciled visible output = %q, %v", data, readErr)
	}
}

func TestWindowsLinkErrorObservationClosePreservesVisibility(t *testing.T) {
	path, directory := privateTestDirectory(t)
	oldSet := setPrivateLinkInformation
	oldClose := closeObservedFile
	setPrivateLinkInformation = func(handle windows.Handle, status *windows.IO_STATUS_BLOCK, info *byte, size uint32) error {
		err := oldSet(handle, status, info, size)
		if err == nil {
			return windows.ERROR_GEN_FAILURE
		}
		return err
	}
	closeObservedFile = func(file *PrivateFile) error {
		if err := oldClose(file); err != nil {
			return err
		}
		return errors.New("injected observation close failure")
	}
	t.Cleanup(func() {
		setPrivateLinkInformation = oldSet
		closeObservedFile = oldClose
	})
	result, err := PublishPrivate(t.Context(), directory, "result", []byte("complete"))
	if err == nil || !result.Visible || !result.VisibilityKnown || !errors.Is(err, ErrUnreadable) {
		t.Fatalf("post-effect observation close = %+v, %v", result, err)
	}
	data, readErr := os.ReadFile(filepath.Join(path, "result"))
	if readErr != nil || string(data) != "complete" {
		t.Fatalf("visible output after close failure = %q, %v", data, readErr)
	}
}
