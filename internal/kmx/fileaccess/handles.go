package fileaccess

import (
	"io"
	"io/fs"
	"os"
)

// Object is a checked file or directory returned by this package. The
// unexported method prevents callers from supplying unchecked implementations.
type Object interface {
	fileaccessObject() *os.File
}

// Directory is an existing checked directory. It deliberately exposes no raw
// descriptor or direct mutation methods. Package operations may use it as an
// anchored parent for private creation, publication, and directory sync.
type Directory struct{ file *os.File }

func (d *Directory) fileaccessObject() *os.File {
	if d == nil {
		return nil
	}
	return d.file
}

func (d *Directory) Close() error {
	if d == nil || d.file == nil {
		return wrapAccess("close directory", unsafeCause("directory handle is absent"))
	}
	return wrapAccess("close directory", d.file.Close())
}

func (d *Directory) Stat() (fs.FileInfo, error) {
	if d == nil || d.file == nil {
		return nil, wrapAccess("stat directory", unsafeCause("directory handle is absent"))
	}
	info, err := d.file.Stat()
	return info, wrapAccess("stat directory", err)
}

// ReadDir reads entries from the retained directory handle. It never reopens
// the directory pathname. Callers must use a fresh handle for a fresh scan.
func (d *Directory) ReadDir(n int) ([]fs.DirEntry, error) {
	if d == nil || d.file == nil {
		return nil, wrapAccess("read directory", unsafeCause("directory handle is absent"))
	}
	entries, err := d.file.ReadDir(n)
	if err == io.EOF {
		err = nil
	}
	return entries, wrapAccess("read directory", err)
}

// File is an existing checked regular file opened for reading only. It
// deliberately exposes no raw descriptor or mutation methods.
type File struct{ file *os.File }

func (f *File) fileaccessObject() *os.File {
	if f == nil {
		return nil
	}
	return f.file
}

func (f *File) Close() error {
	if f == nil || f.file == nil {
		return wrapAccess("close file", unsafeCause("file handle is absent"))
	}
	return wrapAccess("close file", f.file.Close())
}

func (f *File) Stat() (fs.FileInfo, error) {
	if f == nil || f.file == nil {
		return nil, wrapAccess("stat file", unsafeCause("file handle is absent"))
	}
	info, err := f.file.Stat()
	return info, wrapAccess("stat file", err)
}

func (f *File) Read(p []byte) (int, error) {
	if f == nil || f.file == nil {
		return 0, wrapAccess("read file", unsafeCause("file handle is absent"))
	}
	n, err := f.file.Read(p)
	return n, wrapReadError("read file", err)
}

func (f *File) ReadAt(p []byte, offset int64) (int, error) {
	if f == nil || f.file == nil {
		return 0, wrapAccess("read file", unsafeCause("file handle is absent"))
	}
	n, err := f.file.ReadAt(p, offset)
	return n, wrapReadError("read file", err)
}

// PrivateFile is an existing protected owner-only regular file opened for
// package-managed mutation or locking. It deliberately exposes no raw
// descriptor, writer, truncation, rename, delete, or sync method.
type PrivateFile struct {
	file     *os.File
	identity string
}

func (f *PrivateFile) Read(p []byte) (int, error) {
	if f == nil || f.file == nil {
		return 0, wrapAccess("read private file", unsafeCause("private file handle is absent"))
	}
	n, err := f.file.Read(p)
	return n, wrapReadError("read private file", err)
}

func (f *PrivateFile) fileaccessObject() *os.File {
	if f == nil {
		return nil
	}
	return f.file
}

func (f *PrivateFile) Close() error {
	if f == nil || f.file == nil {
		return wrapAccess("close private file", unsafeCause("private file handle is absent"))
	}
	err := f.file.Close()
	f.file = nil
	return wrapAccess("close private file", err)
}

func (f *PrivateFile) Stat() (fs.FileInfo, error) {
	if f == nil || f.file == nil {
		return nil, wrapAccess("stat private file", unsafeCause("private file handle is absent"))
	}
	info, err := f.file.Stat()
	return info, wrapAccess("stat private file", err)
}

func wrapReadError(operation string, err error) error {
	if err == nil || err == io.EOF {
		return err
	}
	return wrapAccess(operation, err)
}

func rawObject(object Object) *os.File {
	if object == nil {
		return nil
	}
	return object.fileaccessObject()
}
