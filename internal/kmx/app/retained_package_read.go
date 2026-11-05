package app

import (
	"context"
	"errors"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

type RetainedEntrySummary struct {
	Handle    string `json:"handle"`
	Digest    string `json:"digest,omitempty"`
	State     string `json:"state"`
	Available bool   `json:"available"`
}
type RetainedListResult struct {
	Packages   []RetainedEntrySummary `json:"packages"`
	Count      int                    `json:"count"`
	NextCursor *string                `json:"nextCursor"`
}
type RetainedRunSummary struct {
	ReservationID string `json:"reservationId,omitempty"`
}
type RetainedInspectResult struct {
	Package        RetainedEntrySummary `json:"package"`
	SourceRevision string               `json:"sourceRevision,omitempty"`
	Fingerprint    string               `json:"inputFingerprint,omitempty"`
	RunCount       int                  `json:"runCount"`
	Runs           []RetainedRunSummary `json:"runs"`
	NextCursor     *string              `json:"nextCursor"`
	ActiveLock     *bool                `json:"activeLock"`
}

func (s *retainedStore) Inspect(ctx context.Context, h, after string) (result RetainedInspectResult, err error) {
	result.Runs = []RetainedRunSummary{}
	if !retainedHandlePattern.MatchString(h) || after != "" && !retainedHandlePattern.MatchString(after) {
		return result, errRetainedUnavailable
	}
	if err = s.check(ctx); err != nil {
		return result, err
	}
	dir, err := fileaccess.OpenDirectoryAt(ctx, s.dir, retainedComponent(h))
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	if err = fileaccess.CheckPrivate(ctx, dir, true); err != nil {
		return result, err
	}
	result.Package = RetainedEntrySummary{Handle: h, State: "incomplete"}
	c, err := s.control(ctx, dir, h)
	if errors.Is(err, fileaccess.ErrMissing) {
		entries, e := dir.ReadDir(1)
		if e == nil && len(entries) == 0 {
			return result, nil
		}
		return result, errRetainedUnavailable
	}
	if err != nil {
		return result, err
	}
	e := &retainedEntry{store: s, handle: h, dir: dir, control: c}
	released, err := retainedReleased(ctx, e)
	if err != nil {
		return result, err
	}
	if released {
		result.Package.State = "released"
		return result, s.check(ctx)
	}
	ready, err := retainedCompletion(ctx, e)
	if errors.Is(err, fileaccess.ErrMissing) {
		return result, s.check(ctx)
	}
	if err != nil {
		return result, err
	}
	result.Package.State = "ready"
	result.Package.Available = true
	result.Package.Digest = ready.Package.Digest
	result.SourceRevision = ready.Package.SourceRevision
	result.Fingerprint = ready.Package.Fingerprint
	return result, s.check(ctx)
}
func (s *retainedStore) List(ctx context.Context, after string) (result RetainedListResult, err error) {
	result.Packages = []RetainedEntrySummary{}
	if after != "" && !retainedHandlePattern.MatchString(after) {
		return result, errRetainedUnavailable
	}
	names, err := s.handles(ctx)
	if err != nil {
		return result, err
	}
	result.Count = len(names)
	for _, h := range names {
		if h <= after {
			continue
		}
		if len(result.Packages) == 32 {
			last := result.Packages[31].Handle
			result.NextCursor = &last
			break
		}
		inspected, err := s.Inspect(ctx, h, "")
		if err != nil {
			return result, err
		}
		result.Packages = append(result.Packages, inspected.Package)
	}
	return result, s.check(ctx)
}
