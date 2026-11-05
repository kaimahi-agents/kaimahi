//go:build windows

package app

import (
	"context"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

func retainedPlatform() error { return errRetainedUnsupported }
func retainedFinalize(context.Context, string, *fileaccess.Directory) error {
	return errRetainedUnsupported
}
