//go:build windows

package fileaccess

import (
	"errors"
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Query overrides are serialized: none of these tests or their callers run
// in parallel. Cleanup restores both native functions even after Fatal.
func qualificationQueries(t *testing.T, file *os.File, flags uint32, fs string) {
	t.Helper()
	oldVolume, oldID := volumeInformationByHandle, fileInformationByHandleEx
	t.Cleanup(func() { volumeInformationByHandle, fileInformationByHandleEx = oldVolume, oldID })
	volumeInformationByHandle = func(h windows.Handle, label *uint16, labelLen uint32, serial, maxComponent, outFlags *uint32, name *uint16, nameLen uint32) error {
		if h != windows.Handle(file.Fd()) || label != nil || labelLen != 0 || serial != nil || maxComponent != nil {
			t.Fatalf("unexpected volume query arguments")
		}
		if outFlags == nil && name == nil {
			t.Fatal("volume query has no requested output")
		}
		if outFlags != nil {
			*outFlags = flags
		}
		if name == nil {
			if nameLen != 0 {
				t.Fatal("nil filesystem name with nonzero length")
			}
		} else {
			encoded, err := windows.UTF16FromString(fs)
			if err != nil || nameLen < uint32(len(encoded)) {
				t.Fatalf("filesystem name output length %d: %v", nameLen, err)
			}
			copy(unsafe.Slice(name, nameLen), encoded)
		}
		return nil
	}
}

func TestQualificationCheckPrivateRequiresPersistentACLs(t *testing.T) {
	file := faOpenDir(t, faPrivateDir(t))
	qualificationQueries(t, file, windows.FILE_PERSISTENT_ACLS, "NTFS")
	if err := CheckPrivate(file, true); err != nil {
		t.Fatalf("same-fixture passing control: %v", err)
	}
	qualificationQueries(t, file, 0, "NTFS")
	if err := CheckPrivate(file, true); err == nil || err.Error() != "filesystem does not enforce persistent ACLs" {
		t.Fatalf("missing persistent ACL qualification = %v; want exact persistent-ACL refusal", err)
	}
}

func TestQualificationCheckSafeDirectoryRequiresPersistentACLs(t *testing.T) {
	file := faOpenDir(t, faPrivateDir(t))
	qualificationQueries(t, file, windows.FILE_PERSISTENT_ACLS, "NTFS")
	if err := CheckSafeDirectory(file, true); err != nil {
		t.Fatalf("same-fixture passing control: %v", err)
	}
	qualificationQueries(t, file, 0, "NTFS")
	if err := CheckSafeDirectory(file, true); err == nil || err.Error() != "filesystem does not enforce persistent ACLs" {
		t.Fatalf("missing safe-directory ACL qualification = %v; want exact persistent-ACL refusal", err)
	}
}

func fakeFileID(t *testing.T, file *os.File, id fileIDInfo, queryErr error, calls *int) {
	t.Helper()
	previous := fileInformationByHandleEx
	t.Cleanup(func() { fileInformationByHandleEx = previous })
	fileInformationByHandleEx = func(h windows.Handle, class uint32, output *byte, size uint32) error {
		if h != windows.Handle(file.Fd()) || class != windows.FileIdInfo || output == nil || size != 24 {
			t.Fatalf("unexpected FileIdInfo query: handle=%v class=%d output=%p size=%d", h, class, output, size)
		}
		*calls++
		if queryErr != nil {
			return queryErr
		}
		*(*fileIDInfo)(unsafe.Pointer(output)) = id
		return nil
	}
}

func TestQualificationPhysicalIdentityRequiresNTFS(t *testing.T) {
	file := faOpenDir(t, faPrivateDir(t))
	qualificationQueries(t, file, windows.FILE_PERSISTENT_ACLS, "NTFS")
	calls := 0
	// This succeeds even if wrongly reached after the non-NTFS response.
	fakeFileID(t, file, fileIDInfo{VolumeSerialNumber: 1, FileID: [16]byte{1}}, nil, &calls)
	if got, err := PhysicalIdentity(file); err != nil || got != "0000000000000001-01000000000000000000000000000000" {
		t.Fatalf("same-fixture NTFS control = %q, %v", got, err)
	}
	qualificationQueries(t, file, windows.FILE_PERSISTENT_ACLS, "ReFS")
	calls = 0
	if got, err := PhysicalIdentity(file); got != "" || err == nil || err.Error() != "query physical identity: filesystem \"ReFS\" is not qualified for private state (NTFS required)" {
		t.Fatalf("non-NTFS qualification = %q, %v; want exact NTFS refusal", got, err)
	}
	if calls != 0 {
		t.Fatalf("non-NTFS qualification queried FileIdInfo %d times", calls)
	}
}

func TestQualificationPhysicalIdentityFullWidth(t *testing.T) {
	file := faOpenDir(t, faPrivateDir(t))
	qualificationQueries(t, file, windows.FILE_PERSISTENT_ACLS, "NTFS")
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
		calls := 0
		fakeFileID(t, file, tc.id, nil, &calls)
		got, err := PhysicalIdentity(file)
		if err != nil || got != tc.want || seen[got] || calls != 1 {
			t.Fatalf("full-width identity = %q, %v, calls=%d; want %q distinct", got, err, calls, tc.want)
		}
		seen[got] = true
	}
}

func TestQualificationQueryErrorsPropagate(t *testing.T) {
	file := faOpenDir(t, faPrivateDir(t))
	qualificationQueries(t, file, windows.FILE_PERSISTENT_ACLS, "NTFS")
	injected := errors.New("injected native query failure")
	calls := 0
	fakeFileID(t, file, fileIDInfo{}, injected, &calls)
	if got, err := PhysicalIdentity(file); got != "" || !errors.Is(err, injected) {
		t.Fatalf("FileIdInfo failure = %q, %v", got, err)
	}
	passingVolumeQuery := volumeInformationByHandle
	volumeInformationByHandle = func(h windows.Handle, label *uint16, labelLen uint32, serial, maxComponent, flags *uint32, name *uint16, nameLen uint32) error {
		if err := passingVolumeQuery(h, label, labelLen, serial, maxComponent, flags, name, nameLen); err != nil {
			return err
		}
		return injected
	}
	for _, check := range []func(*os.File, bool) error{CheckPrivate, CheckSafeDirectory} {
		if err := check(file, true); !errors.Is(err, injected) {
			t.Fatalf("volume error lost by security check: %v", err)
		}
	}
	calls = 0
	if got, err := PhysicalIdentity(file); got != "" || !errors.Is(err, injected) || calls != 0 {
		t.Fatalf("volume failure = %q, %v, ID calls=%d", got, err, calls)
	}
}
