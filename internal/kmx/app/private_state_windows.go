//go:build windows

package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
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
func openSuitePassStore(ctx context.Context, workspaceRoot, cache, suiteDigest string) (_ *suitePassStore, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !suitePrefixedDigest(suiteDigest) {
		return nil, errSuitePassStore
	}
	workspaceRoot, err = filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, errSuitePassStore
	}
	workspace, err := fileaccess.OpenDirectory(ctx, workspaceRoot)
	if err != nil {
		return nil, errSuitePassStore
	}
	workspaceIdentity, identityErr := fileaccess.PhysicalIdentity(ctx, workspace)
	workspaceCloseErr := workspace.Close()
	if identityErr != nil || workspaceCloseErr != nil {
		return nil, errSuitePassStore
	}
	state, err := config.StateDir()
	if err != nil {
		return nil, errSuitePassStore
	}
	identity := fmt.Sprintf("%x", sha256.Sum256([]byte(workspaceIdentity)))
	base := filepath.Join(state, "passing")
	path := filepath.Join(base, identity)
	if scaffold.RefuseKeyShapes(path) != nil || suiteReceiptInside(path, workspaceRoot) || suiteReceiptInside(path, cache) || suiteReceiptPhysicallyInside(ctx, path, workspaceRoot) {
		return nil, errSuitePassStore
	}
	if _, cacheErr := os.Stat(cache); cacheErr == nil && suiteReceiptPhysicallyInside(ctx, path, cache) {
		return nil, errSuitePassStore
	} else if cacheErr != nil && !os.IsNotExist(cacheErr) {
		return nil, errSuitePassStore
	}
	state, err = config.PrepareStateDir()
	if err != nil {
		return nil, errSuitePassStore
	}
	for _, directory := range []string{base, path} {
		opened, openErr := fileaccess.EnsurePrivateDirectory(ctx, directory)
		if openErr != nil {
			return nil, errSuitePassStore
		}
		if closeErr := opened.Close(); closeErr != nil {
			return nil, errSuitePassStore
		}
	}
	directory, err := fileaccess.OpenPrivateDirectory(ctx, path)
	if err != nil {
		return nil, errSuitePassStore
	}
	s := &suitePassStore{
		workspace:   workspaceRoot,
		cache:       cache,
		path:        path,
		partition:   filepath.Join(path, strings.TrimPrefix(suiteDigest, "sha256:")),
		identity:    identity,
		suiteDigest: suiteDigest,
		directory:   directory,
	}
	defer func() {
		if err != nil {
			_ = s.Close()
		}
	}()
	s.privateLock, err = fileaccess.EnsurePrivateFileAt(ctx, directory, "lock")
	if err != nil {
		return nil, errSuitePassStore
	}
	s.privateLockID, err = fileaccess.PhysicalIdentity(ctx, s.privateLock)
	if err != nil {
		return nil, errSuitePassStore
	}
	info, statErr := s.privateLock.Stat()
	links, linksErr := fileaccess.LinkCount(ctx, s.privateLock)
	if statErr != nil || linksErr != nil || !info.Mode().IsRegular() || info.Size() != 0 || links != 1 {
		return nil, errSuitePassStore
	}
	locked, lockErr := fileaccess.TryLockOutcome(ctx, s.privateLock)
	if lockErr != nil {
		return nil, errSuitePassStore
	}
	if !locked {
		return nil, errors.New("another workspace eval or push is active; retry after it exits (no stale lock deletion is needed)")
	}
	partition, err := fileaccess.EnsurePrivateDirectoryAt(ctx, directory, strings.TrimPrefix(suiteDigest, "sha256:"))
	if err != nil {
		return nil, errSuitePassStore
	}
	if err := errors.Join(fileaccess.SyncDirectory(ctx, directory), partition.Close()); err != nil {
		return nil, errSuitePassStore
	}
	if err := s.unchanged(); err != nil {
		return nil, err
	}
	return s, nil
}
func openReleaseLiftStorage(ctx context.Context) (*releaseLiftRecordWriter, error) {
	return nil, fmt.Errorf("open release lift storage: native Windows not supported yet: %w", errors.ErrUnsupported)
}
