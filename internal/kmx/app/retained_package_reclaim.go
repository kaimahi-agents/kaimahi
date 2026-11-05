package app

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

var errRetainedReady = errors.New("ready retained package requires release")

type RetainedReclaimResult struct {
	Handle             string `json:"handle"`
	LocalState         string `json:"localState"`
	Released           bool   `json:"released"`
	RemovedFiles       int    `json:"removedFiles"`
	RemovedDirectories int    `json:"removedDirectories"`
	RemainingEntries   *int   `json:"remainingEntries"`
}

func (s *retainedStore) Release(ctx context.Context, h string) (RetainedReclaimResult, error) {
	return s.reclaim(ctx, h, true, nil)
}
func (s *retainedStore) Cleanup(ctx context.Context, h, reservation string) (RetainedReclaimResult, error) {
	// Qualification history is not produced by this library delivery. A token
	// cannot turn an ordinary cleanup into authority over a future run/record.
	if reservation != "" {
		return RetainedReclaimResult{}, errRetainedUnavailable
	}
	return s.reclaim(ctx, h, false, nil)
}
func (s *retainedStore) reclaim(ctx context.Context, h string, release bool, fault func(string) error) (result RetainedReclaimResult, err error) {
	result = RetainedReclaimResult{Handle: h, LocalState: "unchanged"}
	if !retainedHandlePattern.MatchString(h) {
		return result, errRetainedUnavailable
	}
	e, err := s.openEntry(ctx, h)
	if errors.Is(err, fileaccess.ErrMissing) && !release {
		return s.cleanupTerminal(ctx, h)
	}
	if err != nil {
		return result, err
	}
	defer func() {
		err = errors.Join(err, e.Close())
		if err != nil && result.Released {
			result.LocalState = "partial"
		}
	}()
	revoked, err := retainedReleased(ctx, e)
	if err != nil {
		return result, err
	}
	if revoked {
		result.Released = true
		result.LocalState = "partial"
	}
	if !release && !revoked {
		_, readyErr := retainedCompletion(ctx, e)
		if readyErr == nil {
			return result, errRetainedReady
		}
		if !errors.Is(readyErr, fileaccess.ErrMissing) {
			return result, readyErr
		}
	}
	// This complete bounded result has no run locators until qualification
	// storage is delivered. Pre-encode before any mutation/output adapter.
	if _, err = retainedEncode(result); err != nil {
		return result, err
	}
	for _, name := range []string{"control.json", "ready.json", "released.json"} {
		if err = retainedRetireStage(ctx, e.dir, name); err != nil {
			return result, err
		}
	}
	if !revoked {
		publication, publishErr := s.publishMetadata(ctx, e.dir, "released.json", e.control)
		if publication.Visible {
			result.Released = true
			result.LocalState = "partial"
		} else if !publication.VisibilityKnown {
			result.LocalState = "unknown"
		}
		if publishErr != nil {
			return result, publishErr
		}
	}
	result.Released = true
	result.LocalState = "partial"
	point := func(name string) error {
		if fault != nil && fault(name) != nil {
			return errRetainedUnavailable
		}
		return ctx.Err()
	}
	if err = point("revoked"); err != nil {
		return result, err
	}
	path := filepath.Join(s.path, retainedComponent(h))
	if _, err = retainedInventory(ctx, path, e.dir, false); err != nil {
		return result, err
	}
	fresh, err := fileaccess.OpenDirectoryAt(ctx, s.dir, retainedComponent(h))
	if err != nil {
		return result, err
	}
	entries, readErr := fresh.ReadDir(1025)
	err = errors.Join(readErr, retainedSame(ctx, e.dir, fresh), fresh.Close())
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "control.json", "use.lock", "released.json":
			continue
		}
		if err = e.check(ctx); err != nil {
			return result, err
		}
		if err = retainedRemoveTree(ctx, e.dir, entry.Name(), entry.IsDir(), &result, 0); err != nil {
			return result, err
		}
	}
	if err = point("data"); err != nil {
		return result, err
	}
	family, err := s.familyLock(ctx)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, family.Close()) }()
	if err = e.check(ctx); err != nil {
		return result, err
	}
	// Keep revocation's full ownership binding until last. Every terminal crash
	// leaves either a lockable revoked entry, exactly that marker, or empty H.
	for _, step := range []struct{ name, point string }{{"control.json", "control"}, {"use.lock", "lock"}, {"released.json", "revocation"}} {
		f, openErr := fileaccess.OpenPrivateFileAt(ctx, e.dir, step.name)
		if errors.Is(openErr, fileaccess.ErrMissing) && step.name == "control.json" {
			continue
		}
		if openErr != nil {
			return result, openErr
		}
		removeErr := fileaccess.RemovePrivateFileAt(ctx, e.dir, step.name, f)
		if removeErr == nil {
			result.RemovedFiles++
		}
		if err = errors.Join(removeErr, f.Close(), fileaccess.SyncDirectory(context.WithoutCancel(ctx), e.dir)); err != nil {
			return result, err
		}
		if err = point(step.point); err != nil {
			return result, err
		}
	}
	if err = fileaccess.RemovePrivateDirectoryAt(ctx, s.dir, retainedComponent(h), e.dir); err != nil {
		return result, err
	}
	result.RemovedDirectories++
	if err = point("directory"); err != nil {
		return result, err
	}
	zero := 0
	result.RemainingEntries = &zero
	result.LocalState = "removed"
	return result, nil
}

// Called only for a controlled H with its exclusive use held, after the entire
// bounded private inventory has been checked. No recursive OS deletion exists.
func retainedRemoveTree(ctx context.Context, parent *fileaccess.Directory, name string, directory bool, result *RetainedReclaimResult, depth int) error {
	if depth > 16 {
		return errRetainedUnavailable
	}
	if !directory {
		f, err := fileaccess.OpenPrivateFileAt(ctx, parent, name)
		if err != nil {
			return err
		}
		err = fileaccess.RemovePrivateFileAt(ctx, parent, name, f)
		if err == nil {
			result.RemovedFiles++
		}
		return errors.Join(err, f.Close(), fileaccess.SyncDirectory(context.WithoutCancel(ctx), parent))
	}
	child, err := fileaccess.OpenDirectoryAt(ctx, parent, name)
	if err != nil {
		return err
	}
	defer child.Close()
	if err = fileaccess.CheckPrivate(ctx, child, true); err != nil {
		return err
	}
	entries, err := child.ReadDir(1025)
	if err != nil || len(entries) > 1024 {
		return errRetainedUnavailable
	}
	for _, entry := range entries {
		if err = retainedRemoveTree(ctx, child, entry.Name(), entry.IsDir(), result, depth+1); err != nil {
			return err
		}
	}
	err = fileaccess.RemovePrivateDirectoryAt(ctx, parent, name, child)
	if err == nil {
		result.RemovedDirectories++
	}
	return errors.Join(err, child.Close())
}

func (s *retainedStore) cleanupTerminal(ctx context.Context, h string) (result RetainedReclaimResult, err error) {
	result = RetainedReclaimResult{Handle: h, LocalState: "unchanged"}
	if removed, recoveryErr := s.recoverInitialization(ctx, h); recoveryErr != nil || removed {
		if removed {
			zero := 0
			result.RemainingEntries = &zero
			result.LocalState = "removed"
			result.RemovedDirectories = 1
		}
		return result, recoveryErr
	}
	family, err := s.familyLock(ctx)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, family.Close()) }()
	dir, err := fileaccess.OpenDirectoryAt(ctx, s.dir, retainedComponent(h))
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	if err = fileaccess.CheckPrivate(ctx, dir, true); err != nil {
		return result, err
	}
	entries, err := dir.ReadDir(2)
	if err != nil {
		return result, err
	}
	if len(entries) == 1 && entries[0].Name() == "released.json" && !entries[0].IsDir() {
		// Missing use.lock is admitted only at this exact revoked terminal state.
		// Its previous mutator had to hold this same family lock before unlinking it.
		if _, err = s.control(ctx, dir, h); err != nil {
			return result, err
		}
		result.Released = true
		result.LocalState = "partial"
		if err = retainedRemoveTree(ctx, dir, "released.json", false, &result, 0); err != nil {
			return result, err
		}
	} else if len(entries) != 0 {
		return result, errRetainedUnavailable
	}
	if err = fileaccess.RemovePrivateDirectoryAt(ctx, s.dir, retainedComponent(h), dir); err != nil {
		return result, err
	}
	result.RemovedDirectories++
	zero := 0
	result.RemainingEntries = &zero
	result.LocalState = "removed"
	return result, nil
}
