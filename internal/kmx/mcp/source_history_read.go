package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

// Read descriptors remain held across both reads and composition. This adapter
// grants no publication capability and never bypasses the legacy execution fence.
type historySource struct {
	path             string
	parent           *os.Root
	file             *os.File
	info, parentInfo os.FileInfo
	source           []byte
	tests            bool
}

func sourceNotFound(err error) bool { var e *Error; return errors.As(err, &e) && e.Code == "not_found" }
func (f *Files) physicalSource(p string) string {
	if f.root != nil {
		return filepath.Join(f.root.Name(), filepath.FromSlash(p))
	}
	return p
}

func (f *Files) readHistorySource(p string, tests bool) (*historySource, error) {
	normalized, err := f.rootRelativeSource(p, tests)
	if err != nil {
		return nil, err
	}
	if !tests && !f.allowedPath(normalized) || tests && f.root != nil && !filepath.IsLocal(filepath.FromSlash(normalized)) {
		return nil, refusal("unsafe_path")
	}
	parent, err := f.parent(normalized, false)
	if err != nil {
		return nil, err
	}
	parentInfo, err := parent.Stat(".")
	if err != nil {
		parent.Close()
		return nil, refusal("unsafe_path")
	}
	file, b, info, err := readTarget(parent, filepath.Base(normalized))
	if err != nil {
		parent.Close()
		return nil, err
	}
	// Keep the checked parent traversal above, then retain its canonical spelling
	// for physical-path rechecks (for example ./checks.md versus checks.md).
	if tests && f.root != nil {
		normalized = filepath.ToSlash(filepath.Clean(filepath.FromSlash(normalized)))
	}
	return &historySource{normalized, parent, file, info, parentInfo, b, tests}, nil
}
func (s *historySource) close() { s.file.Close(); s.parent.Close() }
func (s *historySource) unchanged(f *Files) bool {
	normalized, err := f.rootRelativeSource(f.physicalSource(s.path), s.tests)
	if err != nil || normalized != s.path {
		return false
	}
	parent, err := f.parent(s.path, false)
	if err != nil {
		return false
	}
	defer parent.Close()
	directory, err := parent.Stat(".")
	if err != nil || !os.SameFile(directory, s.parentInfo) || !directory.ModTime().Equal(s.parentInfo.ModTime()) {
		return false
	}
	current, err := parent.Lstat(filepath.Base(s.path))
	if err != nil {
		return false
	}
	opened, err := s.file.Stat()
	return err == nil && os.SameFile(current, s.info) && os.SameFile(opened, s.info) && current.Mode() == s.info.Mode() && current.Size() == s.info.Size() && current.ModTime().Equal(s.info.ModTime()) && opened.Size() == s.info.Size() && opened.ModTime().Equal(s.info.ModTime()) && singleLink(s.file)
}

// Historical lookup intentionally skips ONLY requireSingleSource. A retained
// version is incomplete source data, not a current executable agent-only view.
func (f *Files) readHistoryAgent(ctx context.Context, p string) (*agentfile.Agent, []byte, error) {
	if err := cancelled(ctx); err != nil {
		return nil, nil, err
	}
	s, err := f.readHistorySource(p, false)
	if err != nil {
		return nil, nil, err
	}
	defer s.close()
	a, err := agentfile.Parse(s.path, s.source)
	if err != nil {
		return nil, nil, refusal("invalid_file")
	}
	if !s.unchanged(f) {
		return nil, nil, refusal("stale_revision")
	}
	if err := cancelled(ctx); err != nil {
		return nil, nil, err
	}
	return a, s.source, nil
}

func (f *Files) readSourcePair(ctx context.Context, p, override string) (*agentfile.Pair, []byte, error) {
	if err := cancelled(ctx); err != nil {
		return nil, nil, err
	}
	a, err := f.readHistorySource(p, false)
	if err != nil {
		return nil, nil, err
	}
	defer a.close()
	agent, err := agentfile.Parse(a.path, a.source)
	if err != nil {
		return nil, nil, refusal("invalid_file")
	}
	physical := f.physicalSource(a.path)
	target, _ := agentfile.DefaultTestsPath(physical)
	if override != "" {
		target = override
	}
	var tests *historySource
	var b []byte
	selection := agentfile.TestsSelection{Explicit: override != ""}
	if target != "" {
		relative, err := f.rootRelativeSource(target, true)
		if err != nil || f.root != nil && !filepath.IsLocal(filepath.FromSlash(relative)) {
			return nil, nil, refusal("unsafe_path")
		}
		selection.Path = f.physicalSource(relative)
		tests, err = f.readHistorySource(relative, true)
		if err != nil && (override != "" || !sourceNotFound(err)) {
			return nil, nil, err
		}
		if tests != nil {
			defer tests.close()
			b = tests.source
		}
	}
	pair, err := agentfile.ComposePair(physical, agent, selection, b, tests != nil)
	if err != nil {
		return nil, nil, refusal("invalid_file")
	}
	if !a.unchanged(f) || tests != nil && !tests.unchanged(f) {
		return nil, nil, refusal("stale_revision")
	}
	if target != "" && tests == nil {
		// Reprobe only the selected absent name. No fallback or external content read.
		relative, err := f.rootRelativeSource(selection.Path, true)
		if err != nil {
			return nil, nil, refusal("stale_revision")
		}
		stat := os.Lstat
		if f.root != nil {
			stat = f.root.Lstat
		}
		if _, err = stat(relative); !os.IsNotExist(err) {
			return nil, nil, refusal("stale_revision")
		}
	}
	if err := cancelled(ctx); err != nil {
		return nil, nil, err
	}
	return pair, a.source, nil
}
