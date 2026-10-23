//go:build windows

package fileaccess

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func qualificationTestDirectory(t *testing.T) *Directory {
	t.Helper()
	path := filepath.Join(foundationTempDir(t), "qualified")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	user := windowsTestUser(t)
	setTestACL(t, path, "P(A;OICI;FA;;;"+user+")", true)
	return openTestDirectory(t, path)
}

// Native query overrides are process-wide, so these tests deliberately remain
// serial. Each helper restores its override before another test can use it.
type filesystemQueryCalls struct {
	flags int
	name  int
}

func overrideFilesystemQueries(t *testing.T, query func(windows.Handle) (uint32, string)) *filesystemQueryCalls {
	t.Helper()
	previous := volumeInformationByHandle
	calls := filesystemQueryCalls{}
	t.Cleanup(func() { volumeInformationByHandle = previous })
	volumeInformationByHandle = func(handle windows.Handle, label *uint16, labelLen uint32, serial, maxComponent, outFlags *uint32, name *uint16, nameLen uint32) error {
		if label != nil || labelLen != 0 || serial != nil || maxComponent != nil {
			t.Fatalf("unexpected volume query arguments")
		}
		flags, filesystem := query(handle)
		if outFlags == nil && name == nil {
			t.Fatal("filesystem query requested no qualification output")
		}
		if outFlags != nil {
			calls.flags++
			*outFlags = flags
		}
		if name == nil {
			if nameLen != 0 {
				t.Fatal("nil filesystem name has nonzero output length")
			}
			return nil
		}
		calls.name++
		encoded, err := windows.UTF16FromString(filesystem)
		if err != nil || nameLen < uint32(len(encoded)) {
			t.Fatalf("filesystem name output length %d: %v", nameLen, err)
		}
		copy(unsafe.Slice(name, nameLen), encoded)
		return nil
	}
	return &calls
}

func overrideVolumeInformation(t *testing.T, object Object, flags uint32, filesystem string) *filesystemQueryCalls {
	t.Helper()
	file := rawObject(object)
	wantHandle := windows.Handle(file.Fd())
	return overrideFilesystemQueries(t, func(handle windows.Handle) (uint32, string) {
		if handle != wantHandle {
			t.Fatalf("unexpected volume query handle %v", handle)
		}
		return flags, filesystem
	})
}

func requireFilesystemQuery(t *testing.T, calls *filesystemQueryCalls, wantFlags, wantName int) {
	t.Helper()
	if calls.flags != wantFlags || calls.name != wantName {
		t.Fatalf("filesystem qualification queries = flags:%d name:%d, want flags:%d name:%d", calls.flags, calls.name, wantFlags, wantName)
	}
}

func overrideFileInformation(t *testing.T, object Object, id fileIDInfo, queryErr error) *int {
	t.Helper()
	file := rawObject(object)
	wantHandle := windows.Handle(file.Fd())
	previous := fileInformationByHandleEx
	calls := 0
	t.Cleanup(func() { fileInformationByHandleEx = previous })
	fileInformationByHandleEx = func(handle windows.Handle, class uint32, output *byte, size uint32) error {
		calls++
		if handle != wantHandle || class != windows.FileIdInfo || output == nil || size != uint32(unsafe.Sizeof(fileIDInfo{})) {
			t.Fatalf("unexpected FileIdInfo query: handle=%v class=%d output=%p size=%d", handle, class, output, size)
		}
		if queryErr != nil {
			return queryErr
		}
		*(*fileIDInfo)(unsafe.Pointer(output)) = id
		return nil
	}
	return &calls
}

func TestWindowsSecurityChecksExecuteFilesystemQualification(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check func(*Directory) error
	}{
		{"private", func(directory *Directory) error { return CheckPrivate(t.Context(), directory, false) }},
		{"safe-directory", func(directory *Directory) error { return CheckSafeDirectory(t.Context(), directory, true) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := qualificationTestDirectory(t)
			qualifiedCalls := overrideVolumeInformation(t, directory, windows.FILE_PERSISTENT_ACLS, "NTFS")
			if err := tc.check(directory); err != nil {
				t.Fatalf("same-handle qualified control = %v", err)
			}
			requireFilesystemQuery(t, qualifiedCalls, 1, 1)

			missingACLCalls := overrideVolumeInformation(t, directory, 0, "NTFS")
			if err := tc.check(directory); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("missing persistent ACL refusal = %v, want unsupported", err)
			}
			requireFilesystemQuery(t, missingACLCalls, 1, 1)
		})
	}
}

func TestWindowsOpenAPIsRefuseUnsupportedFilesystem(t *testing.T) {
	base := foundationTempDir(t)
	child := filepath.Join(base, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "file"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := openTestDirectory(t, base)
	parentHandle := windows.Handle(rawObject(parent).Fd())
	calls := overrideFilesystemQueries(t, func(handle windows.Handle) (uint32, string) {
		if handle == parentHandle {
			return windows.FILE_PERSISTENT_ACLS, "NTFS"
		}
		return windows.FILE_PERSISTENT_ACLS, "ReFS"
	})

	for _, tc := range []struct {
		operation string
		open      func() (Object, error)
	}{
		{"open directory", func() (Object, error) {
			object, err := OpenDirectory(t.Context(), currentVolumeRoot(t))
			if object == nil {
				return nil, err
			}
			return object, err
		}},
		{"open directory entry", func() (Object, error) {
			object, err := OpenDirectoryAt(t.Context(), parent, "child")
			if object == nil {
				return nil, err
			}
			return object, err
		}},
		{"open regular file", func() (Object, error) {
			object, err := OpenRegularAt(t.Context(), parent, "file")
			if object == nil {
				return nil, err
			}
			return object, err
		}},
	} {
		object, err := tc.open()
		if object != nil || !errors.Is(err, ErrUnsupported) || errors.Is(err, ErrMissing) || errors.Is(err, ErrUnsafe) || errors.Is(err, ErrUnreadable) {
			t.Fatalf("%s unsupported filesystem = %T, %v", tc.operation, object, err)
		}
		var access *AccessError
		if !errors.As(err, &access) || access.Operation != tc.operation || access.Class != ClassUnsupported || err.Error() != tc.operation+": unsupported filesystem" {
			t.Fatalf("%s refusal = %#v, %v", tc.operation, access, err)
		}
	}
	if calls.flags != 5 || calls.name != 5 {
		t.Fatalf("open filesystem queries = flags:%d name:%d, want 5 each", calls.flags, calls.name)
	}
}

func TestWindowsPhysicalIdentityExecutesFilesystemQualification(t *testing.T) {
	directory := qualificationTestDirectory(t)
	volumeCalls := overrideVolumeInformation(t, directory, windows.FILE_PERSISTENT_ACLS, "ReFS")
	idCalls := overrideFileInformation(t, directory, fileIDInfo{VolumeSerialNumber: 1, FileID: [16]byte{1}}, nil)
	if identity, err := PhysicalIdentity(t.Context(), directory); identity != "" || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("non-NTFS identity = %q, %v, want unsupported", identity, err)
	}
	requireFilesystemQuery(t, volumeCalls, 1, 1)
	if *idCalls != 0 {
		t.Fatalf("non-NTFS identity queried FileIdInfo %d times", *idCalls)
	}
}

func TestWindowsPhysicalIdentityUsesFullNativeWidths(t *testing.T) {
	directory := qualificationTestDirectory(t)
	volumeCalls := overrideVolumeInformation(t, directory, windows.FILE_PERSISTENT_ACLS, "NTFS")
	cases := []struct {
		id   fileIDInfo
		want string
	}{
		{fileIDInfo{0x1234567887654321, [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}}, "1234567887654321-0102030405060708090a0b0c0d0e0f10"},
		{fileIDInfo{0x1234567887654321, [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 17, 18, 19, 20, 21, 22, 23, 24}}, "1234567887654321-01020304050607081112131415161718"},
		{fileIDInfo{0xabcdef0187654321, [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}}, "abcdef0187654321-0102030405060708090a0b0c0d0e0f10"},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		idCalls := overrideFileInformation(t, directory, tc.id, nil)
		got, err := PhysicalIdentity(t.Context(), directory)
		if err != nil || got != tc.want || seen[got] || *idCalls != 1 {
			t.Fatalf("full-width identity = %q, %v, calls=%d; want %q distinct", got, err, *idCalls, tc.want)
		}
		seen[got] = true
	}
	requireFilesystemQuery(t, volumeCalls, len(cases), len(cases))
}

func TestWindowsNativeQualificationErrorsStayUnreadable(t *testing.T) {
	directory := qualificationTestDirectory(t)
	injected := errors.New("injected native query failure")
	previousVolume := volumeInformationByHandle
	volumeInformationByHandle = func(windows.Handle, *uint16, uint32, *uint32, *uint32, *uint32, *uint16, uint32) error {
		return injected
	}
	t.Cleanup(func() { volumeInformationByHandle = previousVolume })
	if err := CheckPrivate(t.Context(), directory, true); !errors.Is(err, ErrUnreadable) || errors.Is(err, injected) {
		t.Fatalf("filesystem query error = %v, want redacted unreadable", err)
	}

	volumeInformationByHandle = previousVolume
	overrideVolumeInformation(t, directory, windows.FILE_PERSISTENT_ACLS, "NTFS")
	idCalls := overrideFileInformation(t, directory, fileIDInfo{}, injected)
	if identity, err := PhysicalIdentity(t.Context(), directory); identity != "" || !errors.Is(err, ErrUnreadable) || errors.Is(err, injected) {
		t.Fatalf("FileIdInfo error = %q, %v, want redacted unreadable", identity, err)
	}
	if *idCalls != 1 {
		t.Fatalf("FileIdInfo query calls = %d, want 1", *idCalls)
	}
}
