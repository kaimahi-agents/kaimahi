package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

// RecordingSaver freezes the exact executed source bytes and the physical file
// and parent identity for one later recording publication. The callback grants
// no other path authority, publishes at most once and never retries. Freezing
// requires the caller's executed revision, so the saved bytes extend the source
// that actually ran rather than whatever the path names later.
func (f *Files) RecordingSaver(ctx context.Context, p, expected string) (func(context.Context, []agentfile.Recording) (string, error), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := f.rootRelative(p)
	if err != nil {
		return nil, recordingSaveError(ctx, err)
	}
	if !f.allowedPath(p) {
		return nil, agentfile.ErrRecordingSave
	}
	parent, err := f.parent(p, false)
	if err != nil {
		return nil, recordingSaveError(ctx, err)
	}
	defer parent.Close()
	file, source, info, err := readTarget(parent, filepath.Base(p))
	if err != nil {
		return nil, recordingSaveError(ctx, err)
	}
	file.Close()
	parentInfo, err := parent.Stat(".")
	if err != nil {
		return nil, agentfile.ErrRecordingSave
	}
	observed := &sourceIdentity{file: info, parent: parentInfo}
	if !f.observedParent(p, parent, observed) || revision(source) != expected {
		return nil, agentfile.ErrRecordingChanged
	}
	if f.validate(p, source) != nil {
		return nil, agentfile.ErrRecordingInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var used atomic.Bool
	return func(ctx context.Context, recordings []agentfile.Recording) (string, error) {
		if !used.CompareAndSwap(false, true) {
			return "", agentfile.ErrRecordingSave
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		candidate, err := agentfile.ApplyRecordings(source, recordings)
		if err != nil {
			return "", agentfile.ErrRecordingInvalid
		}
		saved, err := f.editObserved(ctx, p, expected, candidate, observed)
		if err != nil {
			return "", recordingSaveError(ctx, err)
		}
		return saved.Revision, nil
	}, nil
}

// FileRecordingSaver authorizes exactly one CLI-selected physical file. The
// temporary Files value is never returned, so the exact-path capability cannot
// widen inbound MCP or OpenFiles. Load refuses aliases; a path that became one
// after Load refuses here before any tool runs.
func FileRecordingSaver(ctx context.Context, filename, expected string) (func(context.Context, []agentfile.Recording) (string, error), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return nil, agentfile.ErrRecordingSave
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, agentfile.ErrRecordingChanged
	}
	// Match Load's no-link policy, not spelling equality: Windows can normalize
	// case or short names for an ordinary file without changing its authority.
	for p := absolute; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return nil, agentfile.ErrRecordingChanged
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	f := &Files{validate: func(p string, b []byte) error { _, err := agentfile.Parse(p, b); return err }, exact: resolved}
	return f.RecordingSaver(ctx, absolute, expected)
}

// Translate fixed file refusals to shared sentinels; never expose OS or parser text.
func recordingSaveError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var refused *Error
	if errors.As(err, &refused) {
		switch refused.Code {
		case "stale_revision", "not_found":
			return agentfile.ErrRecordingChanged
		case "invalid_file":
			return agentfile.ErrRecordingInvalid
		}
	}
	return agentfile.ErrRecordingSave
}
