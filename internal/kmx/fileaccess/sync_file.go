package fileaccess

import (
	"context"
	"os"
)

var syncFileHandle = func(file *os.File) error { return file.Sync() }

// SyncFile flushes a checked private regular file without exposing a writer or
// descriptor. It never changes permissions, adopts a path or flushes a directory.
func SyncFile(ctx context.Context, file Object) error {
	if err := checkContext(ctx); err != nil {
		return wrapAccess("sync private file", err)
	}
	if err := CheckPrivate(ctx, file, false); err != nil {
		return wrapAccess("sync private file", err)
	}
	raw := rawObject(file)
	info, err := raw.Stat()
	if err != nil {
		return wrapAccess("sync private file", err)
	}
	if !info.Mode().IsRegular() {
		return wrapAccess("sync private file", unsafeCause("object is not a regular file"))
	}
	return wrapAccess("sync private file", syncFileHandle(raw))
}
