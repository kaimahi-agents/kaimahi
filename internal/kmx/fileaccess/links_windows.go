//go:build windows

package fileaccess

import (
	"context"

	"golang.org/x/sys/windows"
)

// LinkCount reports how many directory entries identify object.
func LinkCount(ctx context.Context, object Object) (uint64, error) {
	if err := checkContext(ctx); err != nil {
		return 0, wrapAccess("count object links", err)
	}
	var info windows.ByHandleFileInformation
	err := withHandle(rawObject(object), func(handle windows.Handle) error {
		if err := qualifyHandleAny(ctx, handle); err != nil {
			return err
		}
		if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
			return unreadableError(err)
		}
		return checkContext(ctx)
	})
	if err != nil {
		return 0, wrapAccess("count object links", err)
	}
	return uint64(info.NumberOfLinks), nil
}
