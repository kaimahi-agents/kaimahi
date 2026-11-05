//go:build windows

package fileaccess

import "context"

// RemovePrivateDirectoryAt is unavailable on Windows; retained package adapters
// remain outside the native Windows qualification boundary.
func RemovePrivateDirectoryAt(ctx context.Context, parent *Directory, name string, expected *Directory) error {
	if err := checkContext(ctx); err != nil {
		return wrapAccess("remove private directory", err)
	}
	return wrapAccess("remove private directory", &classifiedCause{class: ClassUnsupported})
}
