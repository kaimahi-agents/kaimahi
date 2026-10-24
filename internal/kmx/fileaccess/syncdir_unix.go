//go:build !windows

package fileaccess

import (
	"context"
	"os"
)

var syncDirectoryHandle = func(file *os.File) error { return file.Sync() }

// SyncDirectory flushes the anchored private directory after entry changes.
func SyncDirectory(ctx context.Context, directory *Directory) error {
	if err := checkContext(ctx); err != nil {
		return wrapAccess("sync private directory", err)
	}
	if err := CheckPrivate(ctx, directory, true); err != nil {
		return wrapAccess("sync private directory", err)
	}
	return wrapAccess("sync private directory", syncDirectoryHandle(rawObject(directory)))
}
