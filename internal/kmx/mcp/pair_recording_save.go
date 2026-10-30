package mcp

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

// FilePairRecordingSaver binds one trusted CLI execution to its sole recording
// destination. Preparation holds only metadata; a skipped callback owns no FDs.
func FilePairRecordingSaver(ctx context.Context, executed *agentfile.Pair) (app.PairRecordingSaver, error) {
	current := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if executed == nil || executed.Agent == nil {
			return agentfile.ErrRecordingChanged
		}
		override := ""
		if executed.Tests.Selection.Explicit {
			override = executed.Tests.Selection.Path
		}
		now, err := agentfile.LoadPair(executed.AgentPath, override)
		if err != nil || !executed.SameSourceVersion(now) {
			return agentfile.ErrRecordingChanged
		}
		return nil
	}
	if err := current(ctx); err != nil {
		return nil, err
	}
	if executed.Tests.State != "external" && executed.Tests.State != "inline" {
		return nil, agentfile.ErrRecordingInvalid
	}
	var used atomic.Bool
	return func(saveCtx context.Context, recordings []agentfile.Recording) (app.PairRecordingSaveResult, error) {
		var empty app.PairRecordingSaveResult
		if !used.CompareAndSwap(false, true) {
			return empty, agentfile.ErrRecordingSave
		}
		if err := saveCtx.Err(); err != nil {
			return empty, err
		}
		agent, err := holdPairSource(executed.AgentPath, executed.Agent.Revision)
		if err != nil {
			return empty, recordingSaveError(saveCtx, err)
		}
		defer agent.close()
		var tests *heldPairSource
		if executed.Tests.Selection.Path != "" {
			expected := ""
			if executed.Tests.State == "external" {
				expected = executed.Tests.Revision
			}
			tests, err = holdPairSource(executed.Tests.Selection.Path, expected)
			if err != nil {
				return empty, recordingSaveError(saveCtx, err)
			}
			defer tests.close()
		}
		// Acquire first, then compare the public checked snapshot and bind our
		// retained objects to the same current names before any staging effects.
		if err := current(saveCtx); err != nil {
			return empty, err
		}
		check := func() error {
			if err := agent.check(); err != nil {
				return err
			}
			if tests != nil {
				return tests.check()
			}
			return nil
		}
		target := agent
		descriptor := app.FileRecordingTarget{Role: "agent", Path: executed.AgentPath, SelectionKey: executed.Tests.Selection.Key}
		patch := agentfile.ApplyRecordings
		if executed.Tests.State == "external" {
			target, patch = tests, agentfile.ApplyTestRecordings
			descriptor = app.FileRecordingTarget{Role: "tests", Path: executed.Tests.Selection.Path, SelectionKey: executed.Tests.Selection.Key}
		}
		candidate, err := patch(target.source, recordings)
		if err != nil {
			return empty, agentfile.ErrRecordingInvalid
		}
		compose := func(b []byte) (*agentfile.Pair, error) {
			if descriptor.Role == "tests" {
				// ComposePair uses ParseTests and validates every tool reference
				// against the original agent, never the agent parser on tests.
				return agentfile.ComposePair(executed.AgentPath, executed.Agent, executed.Tests.Selection, b, true)
			}
			a, err := agentfile.Parse(executed.AgentPath, b)
			if err != nil {
				return nil, err
			}
			return agentfile.ComposePair(executed.AgentPath, a, executed.Tests.Selection, nil, false)
		}
		savedPair, err := compose(candidate)
		if err != nil || revision(candidate) == target.expected || savedPair.SnapshotRevision == executed.SnapshotRevision {
			return empty, agentfile.ErrRecordingInvalid
		}
		resolved, err := filepath.EvalSymlinks(descriptor.Path)
		if err != nil {
			return empty, agentfile.ErrRecordingChanged
		}
		f := &Files{exact: resolved, validate: func(_ string, b []byte) error { _, err := compose(b); return err }}
		// After staging, parent timestamps include our own temporary file. Only
		// descriptor-bound IDs, file versions, raw revisions and absence apply.
		if _, err := f.editObservedChecked(saveCtx, descriptor.Path, target.expected, candidate, &target.identity, check, check); err != nil {
			return empty, recordingSaveError(saveCtx, err)
		}
		return app.PairRecordingSaveResult{Revision: revision(candidate), SnapshotRevision: savedPair.SnapshotRevision, AgentRevision: savedPair.Agent.Revision, Target: descriptor}, nil
	}, nil
}

// Retain the nearest existing parent even for an absent default directory. Open
// descriptors prevent identity reuse throughout staging and checked publication.
type heldPairSource struct {
	path, parentPath, name, expected string
	parent                           *os.Root
	file                             *os.File
	identity                         sourceIdentity
	source                           []byte
}

func holdPairSource(path, expected string) (*heldPairSource, error) {
	h := &heldPairSource{path: path, parentPath: filepath.Dir(path), expected: expected}
	for {
		parent, err := os.OpenRoot(h.parentPath)
		if err == nil {
			h.parent = parent
			break
		}
		if expected != "" || !os.IsNotExist(err) || filepath.Dir(h.parentPath) == h.parentPath {
			return nil, refusal("stale_revision")
		}
		h.parentPath = filepath.Dir(h.parentPath)
	}
	ok := false
	defer func() {
		if !ok {
			h.close()
		}
	}()
	var err error
	h.identity.parent, err = h.parent.Stat(".")
	if err != nil {
		return nil, refusal("io_failed")
	}
	h.name, err = filepath.Rel(h.parentPath, path)
	if err != nil {
		return nil, refusal("unsafe_path")
	}
	if expected != "" {
		h.file, h.source, h.identity.file, err = readTarget(h.parent, h.name)
		if err != nil {
			return nil, err
		}
	}
	if err := h.check(); err != nil {
		return nil, err
	}
	ok = true
	return h, nil
}

func (h *heldPairSource) close() {
	if h.file != nil {
		h.file.Close()
	}
	h.parent.Close()
}

func (h *heldPairSource) check() error {
	// observedParent checks both the held directory and its physical pathname.
	if !(&Files{}).observedParent(filepath.Join(h.parentPath, "unused"), h.parent, &h.identity) {
		return refusal("stale_revision")
	}
	name := h.name
	if h.file == nil {
		// If parents were absent, their first missing component must stay absent
		// too; pinning an ancestor must not adopt a newly created tests folder.
		for filepath.Dir(name) != "." {
			name = filepath.Dir(name)
		}
	}
	named, err := h.parent.Lstat(name)
	if h.file == nil {
		if os.IsNotExist(err) {
			return nil
		}
		return refusal("stale_revision")
	}
	info, statErr := h.file.Stat()
	if err != nil || statErr != nil || !named.Mode().IsRegular() || !os.SameFile(h.identity.file, named) ||
		!os.SameFile(h.identity.file, info) || named.Mode() != h.identity.file.Mode() || named.Size() != h.identity.file.Size() ||
		!named.ModTime().Equal(h.identity.file.ModTime()) || info.Mode() != h.identity.file.Mode() || info.Size() != h.identity.file.Size() ||
		!info.ModTime().Equal(h.identity.file.ModTime()) || !singleLink(h.file) {
		return refusal("stale_revision")
	}
	if _, err := h.file.Seek(0, io.SeekStart); err != nil {
		return refusal("io_failed")
	}
	b, err := io.ReadAll(io.LimitReader(h.file, maxFileBytes+1))
	if err != nil {
		return refusal("io_failed")
	}
	if revision(b) != h.expected {
		return refusal("stale_revision")
	}
	return nil
}
