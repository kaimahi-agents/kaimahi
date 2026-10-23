//go:build windows

package fileaccess

import (
	"context"
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	privateFileAccess      = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.FILE_WRITE_ATTRIBUTES | windows.READ_CONTROL
	privateDirectoryAccess = directoryAccess | windows.READ_CONTROL
	privateFileCreate      = privateFileAccess | windows.DELETE
	privateDirectoryCreate = privateDirectoryAccess | windows.DELETE
)

func privateDescriptor(ctx context.Context, directory bool) (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := currentUser(ctx)
	if err != nil {
		return nil, err
	}
	inherit := ""
	if directory {
		inherit = "OICI"
	}
	sid := user.String()
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf("O:%sD:P(A;%s;FA;;;%s)", sid, inherit, sid))
	if err != nil {
		return nil, unreadableError(err)
	}
	return descriptor, nil
}

// CreatePrivateFileAt exclusively creates a protected owner-only regular file
// immediately below parent.
func CreatePrivateFileAt(ctx context.Context, parent *Directory, name string) (*PrivateFile, error) {
	return createPrivateFileAt(ctx, parent, name)
}

// EnsurePrivateFileAt opens a protected owner-only regular file immediately
// below parent, creating it if absent. Existing objects are checked, never
// repaired or truncated.
func EnsurePrivateFileAt(ctx context.Context, parent *Directory, name string) (*PrivateFile, error) {
	file, err := openExistingPrivateFileAt(ctx, parent, name)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return file, wrapAccess("open private file", err)
	}
	file, err = createPrivateFileAt(ctx, parent, name)
	if errors.Is(err, errEntryExists) {
		file, err = openExistingPrivateFileAt(ctx, parent, name)
	}
	return file, wrapAccess("open private file", err)
}

func createPrivateFileAt(ctx context.Context, parent *Directory, name string) (*PrivateFile, error) {
	if err := CheckSafeDirectory(ctx, parent, true); err != nil {
		return nil, wrapAccess("open private file", err)
	}
	if err := checkWindowsComponent(name); err != nil {
		return nil, wrapAccess("open private file", err)
	}
	descriptor, err := privateDescriptor(ctx, false)
	if err != nil {
		return nil, wrapAccess("open private file", err)
	}
	var creator windows.Handle
	parentFile := rawObject(parent)
	err = withHandle(parentFile, func(parentHandle windows.Handle) error {
		if err := qualifyHandle(ctx, parentHandle, true); err != nil {
			return err
		}
		opened, _, openErr := ntCreate(parentHandle, name, privateFileCreate, windows.FILE_NON_DIRECTORY_FILE, windows.FILE_CREATE, descriptor)
		if openErr != nil {
			if errors.Is(openErr, os.ErrExist) {
				return &classifiedCause{class: ClassUnsafe, cause: errEntryExists, reason: "private file already exists"}
			}
			return openErr
		}
		if err := qualifyHandle(ctx, opened, false); err != nil {
			cleanupErr := discardWindowsHandle(opened)
			closeErr := windows.CloseHandle(opened)
			return creationFailure(err, cleanupErr, closeErr)
		}
		creator = opened
		return nil
	})
	if err != nil {
		return nil, wrapAccess("open private file", err)
	}
	creatorFile := os.NewFile(uintptr(creator), childWindowsName(parentFile, name))
	created := &PrivateFile{file: creatorFile}
	if err := CheckPrivate(ctx, created, false); err != nil {
		return nil, wrapAccess("open private file", creationFailure(err, discardCreatedWindowsFile(creatorFile)))
	}
	identity, err := PhysicalIdentity(ctx, created)
	if err != nil {
		return nil, wrapAccess("open private file", creationFailure(err, discardCreatedWindowsFile(creatorFile)))
	}
	retained, err := openExistingPrivateFileAt(ctx, parent, name)
	if err != nil {
		return nil, wrapAccess("open private file", creationFailure(err, discardCreatedWindowsFile(creatorFile)))
	}
	if err := requireObjectIdentity(ctx, retained, identity); err != nil {
		retainedCloseErr := retained.Close()
		return nil, wrapAccess("open private file", creationFailure(err, retainedCloseErr, discardCreatedWindowsFile(creatorFile)))
	}
	if err := creatorFile.Close(); err != nil {
		retainedCloseErr := retained.Close()
		cleanupErr := cleanupCreatedWindowsEntry(context.WithoutCancel(ctx), parent, name, identity, false)
		return nil, wrapAccess("open private file", creationFailure(err, retainedCloseErr, cleanupErr))
	}
	retained.identity = identity
	return retained, nil
}

// CreatePrivateDirectoryAt exclusively creates a protected owner-only
// directory immediately below parent.
func CreatePrivateDirectoryAt(ctx context.Context, parent *Directory, name string) (*Directory, error) {
	return createPrivateDirectoryAt(ctx, parent, name)
}

// EnsurePrivateDirectoryAt opens a protected owner-only directory immediately
// below parent, creating it if absent. Existing directories are never repaired.
func EnsurePrivateDirectoryAt(ctx context.Context, parent *Directory, name string) (*Directory, error) {
	directory, err := openExistingPrivateDirectoryAt(ctx, parent, name)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return directory, wrapAccess("open private directory", err)
	}
	directory, err = createPrivateDirectoryAt(ctx, parent, name)
	if errors.Is(err, errEntryExists) {
		directory, err = openExistingPrivateDirectoryAt(ctx, parent, name)
	}
	return directory, wrapAccess("open private directory", err)
}

func createPrivateDirectoryAt(ctx context.Context, parent *Directory, name string) (*Directory, error) {
	if err := CheckSafeDirectory(ctx, parent, true); err != nil {
		return nil, wrapAccess("open private directory", err)
	}
	return createPrivateDirectoryAtQualified(ctx, parent, name)
}

func createPrivateDirectoryAtQualified(ctx context.Context, parent *Directory, name string) (*Directory, error) {
	if err := checkWindowsComponent(name); err != nil {
		return nil, wrapAccess("open private directory", err)
	}
	descriptor, err := privateDescriptor(ctx, true)
	if err != nil {
		return nil, wrapAccess("open private directory", err)
	}
	var creator windows.Handle
	parentFile := rawObject(parent)
	err = withHandle(parentFile, func(parentHandle windows.Handle) error {
		if err := qualifyHandle(ctx, parentHandle, true); err != nil {
			return err
		}
		opened, _, openErr := ntCreate(parentHandle, name, privateDirectoryCreate, windows.FILE_DIRECTORY_FILE, windows.FILE_CREATE, descriptor)
		if openErr != nil {
			if errors.Is(openErr, os.ErrExist) {
				return &classifiedCause{class: ClassUnsafe, cause: errEntryExists, reason: "private directory already exists"}
			}
			return openErr
		}
		if err := qualifyHandle(ctx, opened, true); err != nil {
			cleanupErr := discardWindowsHandle(opened)
			closeErr := windows.CloseHandle(opened)
			return creationFailure(err, cleanupErr, closeErr)
		}
		creator = opened
		return nil
	})
	if err != nil {
		return nil, wrapAccess("open private directory", err)
	}
	creatorFile := os.NewFile(uintptr(creator), childWindowsName(parentFile, name))
	created := &Directory{file: creatorFile}
	if err := CheckPrivate(ctx, created, true); err != nil {
		return nil, wrapAccess("open private directory", creationFailure(err, discardCreatedWindowsFile(creatorFile)))
	}
	identity, err := PhysicalIdentity(ctx, created)
	if err != nil {
		return nil, wrapAccess("open private directory", creationFailure(err, discardCreatedWindowsFile(creatorFile)))
	}
	retained, err := openExistingPrivateDirectoryAt(ctx, parent, name)
	if err != nil {
		return nil, wrapAccess("open private directory", creationFailure(err, discardCreatedWindowsFile(creatorFile)))
	}
	if err := requireObjectIdentity(ctx, retained, identity); err != nil {
		retainedCloseErr := retained.Close()
		return nil, wrapAccess("open private directory", creationFailure(err, retainedCloseErr, discardCreatedWindowsFile(creatorFile)))
	}
	if err := creatorFile.Close(); err != nil {
		retainedCloseErr := retained.Close()
		cleanupErr := cleanupCreatedWindowsEntry(context.WithoutCancel(ctx), parent, name, identity, true)
		return nil, wrapAccess("open private directory", creationFailure(err, retainedCloseErr, cleanupErr))
	}
	return retained, nil
}

// OpenPrivateDirectory opens an existing protected owner-only directory after
// checking every retained ancestor. It creates and repairs nothing.
func OpenPrivateDirectory(ctx context.Context, path string) (*Directory, error) {
	return privateDirectoryPath(ctx, path, false)
}

// EnsurePrivateDirectory opens or creates a protected owner-only directory,
// checking every existing ancestor and repairing nothing.
func EnsurePrivateDirectory(ctx context.Context, path string) (*Directory, error) {
	return privateDirectoryPath(ctx, path, true)
}

func privateDirectoryPath(ctx context.Context, path string, create bool) (*Directory, error) {
	parsed, err := parseLocalPath(path)
	if err != nil {
		return nil, wrapAccess("open private directory", err)
	}
	if len(parsed.parts) == 0 {
		return nil, wrapAccess("open private directory", unsafeCause("volume root cannot be private state"))
	}
	handle, err := openVolumeRoot(ctx, parsed.drive)
	if err != nil {
		return nil, wrapAccess("open private directory", err)
	}
	current := &Directory{file: os.NewFile(uintptr(handle), string(parsed.drive)+`:\`)}
	if err := CheckSafeDirectory(ctx, current, false); err != nil {
		current.Close()
		return nil, wrapAccess("open private directory", err)
	}
	for i, part := range parsed.parts {
		last := i == len(parsed.parts)-1
		if last {
			if err := CheckSafeDirectory(ctx, current, true); err != nil {
				current.Close()
				return nil, wrapAccess("open private directory", err)
			}
			var next *Directory
			if create {
				next, err = EnsurePrivateDirectoryAt(ctx, current, part)
			} else {
				next, err = OpenDirectoryAt(ctx, current, part)
				if err == nil {
					err = CheckPrivate(ctx, next, true)
				}
			}
			current.Close()
			if err != nil {
				if next != nil {
					next.Close()
				}
				return nil, wrapAccess("open private directory", err)
			}
			return next, nil
		}

		next, openErr := OpenDirectoryAt(ctx, current, part)
		if openErr != nil && create && errors.Is(openErr, os.ErrNotExist) {
			next, openErr = EnsurePrivateDirectoryAt(ctx, current, part)
		}
		if openErr == nil {
			openErr = CheckSafeDirectory(ctx, next, i == len(parsed.parts)-2)
		}
		current.Close()
		if openErr != nil {
			if next != nil {
				next.Close()
			}
			return nil, wrapAccess("open private directory", openErr)
		}
		current = next
	}
	current.Close()
	return nil, wrapAccess("open private directory", unsafeCause("private directory path is empty"))
}

func discardWindowsHandle(handle windows.Handle) error {
	info := fileDispositionInformationEx{Flags: windows.FILE_DISPOSITION_DELETE |
		windows.FILE_DISPOSITION_POSIX_SEMANTICS |
		windows.FILE_DISPOSITION_FORCE_IMAGE_SECTION_CHECK}
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(handle, &status, (*byte)(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)), windows.FileDispositionInformationEx)
}

func discardCreatedWindowsFile(file *os.File) error {
	removeErr := withHandle(file, discardWindowsHandle)
	closeErr := file.Close()
	return errors.Join(removeErr, closeErr)
}

func requireObjectIdentity(ctx context.Context, object Object, identity string) error {
	got, err := PhysicalIdentity(ctx, object)
	if err != nil {
		return err
	}
	if got != identity {
		return unsafeCause("created object identity changed")
	}
	return nil
}

func openExistingPrivateDirectoryAt(ctx context.Context, parent *Directory, name string) (*Directory, error) {
	directory, err := OpenDirectoryAt(ctx, parent, name)
	if err != nil {
		return nil, err
	}
	if err := CheckPrivate(ctx, directory, true); err != nil {
		closeErr := directory.Close()
		return nil, creationFailure(err, closeErr)
	}
	return directory, nil
}

func cleanupCreatedWindowsEntry(ctx context.Context, parent *Directory, name, identity string, directory bool) error {
	options := uint32(windows.FILE_NON_DIRECTORY_FILE)
	if directory {
		options = windows.FILE_DIRECTORY_FILE
	}
	var handle windows.Handle
	parentFile := rawObject(parent)
	err := withHandle(parentFile, func(parentHandle windows.Handle) error {
		opened, openErr := ntOpen(parentHandle, name,
			windows.DELETE|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, options)
		if openErr != nil {
			return openErr
		}
		if err := qualifyHandle(ctx, opened, directory); err != nil {
			closeErr := windows.CloseHandle(opened)
			return creationFailure(err, closeErr)
		}
		handle = opened
		return nil
	})
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(handle), childWindowsName(parentFile, name))
	var object Object = &PrivateFile{file: file}
	if directory {
		object = &Directory{file: file}
	}
	if err := CheckPrivate(ctx, object, directory); err != nil {
		closeErr := file.Close()
		return creationFailure(err, closeErr)
	}
	if err := requireObjectIdentity(ctx, object, identity); err != nil {
		closeErr := file.Close()
		return creationFailure(err, closeErr)
	}
	removeErr := withHandle(file, discardWindowsHandle)
	closeErr := file.Close()
	return errors.Join(removeErr, closeErr)
}
