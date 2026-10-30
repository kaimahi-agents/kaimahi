package mcp

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

// A binding owns checked MCP descriptors, not CLI observations or filename-based
// write permission. Its runner validator never reopens source contents.
type pairBinding struct {
	files               *Files
	pair                *agentfile.Pair
	sources             pairSources
	agentAbsent         *absentSource
	agent, tests        *historySource
	absent              *absentSource
	requested, override string
}

func (b *pairBinding) close() {
	if b.agent != nil {
		b.agent.close()
	}
	if b.tests != nil {
		b.tests.close()
	}
	if b.absent != nil {
		b.absent.parent.Close()
	}
	if b.agentAbsent != nil {
		b.agentAbsent.parent.Close()
	}
}
func (b *pairBinding) check(ctx context.Context) error {
	if err := cancelled(ctx); err != nil {
		return err
	}
	actual, err := b.files.rootRelativeSource(b.requested, false)
	if err != nil || b.files.physicalSource(actual) != b.sources.AgentPath {
		return refusal("stale_revision")
	}
	target, _ := agentfile.DefaultTestsPath(b.sources.AgentPath)
	if b.override != "" {
		target = b.override
	}
	if target != "" {
		selected, err := b.files.rootRelativeSource(target, true)
		if err != nil || b.files.physicalSource(selected) != b.sources.Tests.Selection.Path {
			return refusal("stale_revision")
		}
	}
	if b.agent != nil && !b.agent.unchanged(b.files) || b.agentAbsent != nil && !b.agentAbsent.unchanged(b.files) || b.tests != nil && !b.tests.unchanged(b.files) || b.absent != nil && !b.absent.unchanged(b.files) {
		return refusal("stale_revision")
	}
	return nil
}

type absentSource struct {
	path, probe, name string
	parent            *os.Root
	info              os.FileInfo
}

func (f *Files) holdAbsentSource(target string) (*absentSource, error) {
	normalized, err := f.rootRelativeSource(target, true)
	if err != nil {
		return nil, err
	}
	probe := normalized
	for {
		parent, err := f.parent(probe, false)
		if err == nil {
			info, err := parent.Stat(".")
			if err != nil {
				parent.Close()
				return nil, refusal("unsafe_path")
			}
			name, err := filepath.Rel(f.physicalSource(filepath.Dir(probe)), target)
			if err != nil || !filepath.IsLocal(name) {
				parent.Close()
				return nil, refusal("unsafe_path")
			}
			name = strings.Split(filepath.ToSlash(name), "/")[0]
			a := &absentSource{normalized, probe, name, parent, info}
			if !a.unchanged(f) {
				parent.Close()
				return nil, refusal("stale_revision")
			}
			return a, nil
		}
		if !sourceNotFound(err) {
			return nil, err
		}
		dir := filepath.Dir(probe)
		up := filepath.Dir(dir)
		if dir == up {
			return nil, refusal("unsafe_path")
		}
		probe = filepath.Join(up, filepath.Base(probe))
	}
}
func (a *absentSource) unchanged(f *Files) bool {
	normalized, err := f.rootRelativeSource(f.physicalSource(a.path), true)
	if err != nil || normalized != a.path || !f.observedParent(a.probe, a.parent, &sourceIdentity{parent: a.info}) {
		return false
	}
	info, err := a.parent.Stat(".")
	if err != nil || !info.ModTime().Equal(a.info.ModTime()) {
		return false
	}
	_, err = a.parent.Lstat(a.name)
	return os.IsNotExist(err)
}

// Only metadata is cached between requests; no FDs, source payload or durable
// authority survives a resource read. Refresh after process restart or eviction.
type sourceStamp struct {
	path, parent         string
	fileInfo, parentInfo os.FileInfo
}
type pairContext struct {
	version, snapshot string
	agent, tests      sourceStamp
}

func sourceStampFor(s *historySource) sourceStamp {
	return sourceStamp{s.path, filepath.Clean(s.parent.Name()), s.info, s.parentInfo}
}
func (b *pairBinding) stamp() pairContext {
	c := pairContext{snapshot: b.sources.SnapshotRevision, agent: sourceStampFor(b.agent)}
	if b.tests != nil {
		c.tests = sourceStampFor(b.tests)
	} else if b.absent != nil {
		c.tests = sourceStamp{b.absent.path, filepath.Clean(b.absent.parent.Name()), nil, b.absent.info}
	}
	return c
}
func (s sourceStamp) same(other sourceStamp) bool {
	if s.path != other.path || s.parent != other.parent {
		return false
	}
	if s.path == "" {
		return true
	}
	if !os.SameFile(s.parentInfo, other.parentInfo) {
		return false
	}
	if s.fileInfo == nil || other.fileInfo == nil {
		return s.fileInfo == nil && other.fileInfo == nil && s.parentInfo.ModTime().Equal(other.parentInfo.ModTime())
	}
	return os.SameFile(s.fileInfo, other.fileInfo) && s.fileInfo.Mode() == other.fileInfo.Mode() && s.fileInfo.Size() == other.fileInfo.Size() && s.fileInfo.ModTime().Equal(other.fileInfo.ModTime())
}
func (c pairContext) same(other pairContext) bool {
	return c.snapshot == other.snapshot && c.agent.same(other.agent) && c.tests.same(other.tests)
}
func (b *pairBinding) sourceVersion() string {
	current := b.stamp()
	f := b.files
	f.viewMu.Lock()
	defer f.viewMu.Unlock()
	for _, c := range f.pairContexts {
		if c.same(current) {
			return c.version
		}
	}
	current.version = rand.Text()
	if len(f.pairContexts) == 32 {
		f.pairContexts = append(f.pairContexts[:0], f.pairContexts[1:]...)
	}
	f.pairContexts = append(f.pairContexts, current)
	return current.version
}
func (b *pairBinding) requireVersion(expected string) error {
	current := b.stamp()
	f := b.files
	f.viewMu.Lock()
	defer f.viewMu.Unlock()
	for _, c := range f.pairContexts {
		if c.version == expected && c.same(current) {
			return nil
		}
	}
	return refusal("stale_revision")
}
