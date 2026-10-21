//go:build windows

package app

import (
	"context"
	"errors"
	"fmt"
	"os"
)

func openSessionReferenceDirectory(path string) (*os.Root, string, os.FileInfo, error) {
	return nil, "", nil, fmt.Errorf("open session reference directory: native Windows not supported yet: %w", errors.ErrUnsupported)
}
func lockSessionReferenceManifest(ctx context.Context, root *os.Root, name string) (func(), error) {
	return nil, fmt.Errorf("lock session reference manifest: native Windows not supported yet: %w", errors.ErrUnsupported)
}
func replaceSessionReferenceManifest(root *os.Root, name, stage string, original os.FileInfo, originalRaw []byte) error {
	return fmt.Errorf("replace session reference manifest: native Windows not supported yet: %w", errors.ErrUnsupported)
}
func openSuitePassStore(ctx context.Context, workspaceRoot, cache, suiteDigest string) (*suitePassStore, error) {
	return nil, fmt.Errorf("open passing evidence store: native Windows not supported yet: %w", errors.ErrUnsupported)
}
func openReleaseLiftStorage(ctx context.Context) (*releaseLiftRecordWriter, error) {
	return nil, fmt.Errorf("open release lift storage: native Windows not supported yet: %w", errors.ErrUnsupported)
}
