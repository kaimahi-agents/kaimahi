package app

import (
	"context"
	"errors"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

// Family exclusion covers the interval before use.lock exists. Only a valid
// control binding (or an exact empty allocation) grants this recovery path;
// an unmarked lock-only directory is never adopted.
func (s *retainedStore) recoverInitialization(ctx context.Context, h string) (removed bool, err error) {
	if !retainedHandlePattern.MatchString(h) {
		return false, errRetainedUnavailable
	}
	lock, err := s.familyLock(ctx)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	dir, err := fileaccess.OpenDirectoryAt(ctx, s.dir, retainedComponent(h))
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	if err = fileaccess.CheckPrivate(ctx, dir, true); err != nil {
		return false, err
	}
	entries, err := dir.ReadDir(3)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() != "control.json" && entry.Name() != "control.stage" {
			return false, nil
		}
	}
	if len(entries) > 0 {
		if _, err = s.control(ctx, dir, h); err != nil {
			return false, err
		}
		if err = retainedRetireStage(ctx, dir, "control.json"); err != nil {
			return false, err
		}
		f, openErr := fileaccess.OpenPrivateFileAt(ctx, dir, "control.json")
		if !errors.Is(openErr, fileaccess.ErrMissing) {
			if openErr != nil {
				return false, openErr
			}
			removeErr := fileaccess.RemovePrivateFileAt(ctx, dir, "control.json", f)
			if err = errors.Join(removeErr, f.Close(), fileaccess.SyncDirectory(context.WithoutCancel(ctx), dir)); err != nil {
				return false, err
			}
		}
	}
	if err = fileaccess.RemovePrivateDirectoryAt(ctx, s.dir, retainedComponent(h), dir); err != nil {
		return false, err
	}
	return true, nil
}
