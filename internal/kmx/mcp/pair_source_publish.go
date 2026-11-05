package mcp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

// SourcePairPublishRequest is a trusted Go adapter binding, never inbound JSON.
// Validate binds ComposeOrkaSourcePair for complete native tuples; Current binds
// Edit to a checked native load of the same selection and SameSourceVersion.
// Preflight must encode the complete prospective ACK, including its newline and
// conservative envelope widths, within 64 KiB without output or advancing state.
// Callbacks perform no source writes, model, config or cluster work. Each receives
// isolated bytes; callback errors are returned intact.
type SourcePairPublishRequest struct {
	Intent                 string // "create" or "edit"
	AgentPath              string
	Tests                  agentfile.TestsSelection
	ExpectedRevision       string // mandatory for edit, empty for create
	AgentBytes, TestsBytes []byte
	TestsPresent           bool
	CreateDefaultTests     bool // trusted sparse-edit permission, not an inbound JSON field
	Validate               func(SourcePairTuple) error
	Current                func(context.Context) error // mandatory for edit, bound to checked native load
	Preflight              func(SourcePairTuple, SourcePairPublishResult) error
}
type SourcePairTuple struct {
	AgentPath                              string
	Tests                                  agentfile.TestsSelection
	AgentBytes, TestsBytes                 []byte
	TestsPresent                           bool
	AgentRevision, TestsRevision, Revision string // publisher-computed
}
type SourcePairWrite struct{ State, Revision string }
type SourcePairPublishResult struct {
	Agent, Tests SourcePairWrite
	Revision     string // only a complete, finally checked pair; empty on error/uncertainty
}

// PublishSourcePair validates and preflights a native pair under held source
// locks, then delegates publication to the shared descriptor engine. Create
// requires both default targets absent. Edit never deletes a source; only trusted
// CreateDefaultTests may materialize an absent, non-explicit default companion.
// Per-source facts survive errors; Revision is set only after complete checked
// publication, including a validated, preflighted no-op. No history or cache is
// retained, and this entry point registers no MCP operation.
func PublishSourcePair(ctx context.Context, req SourcePairPublishRequest) (SourcePairPublishResult, error) {
	result := SourcePairPublishResult{Agent: SourcePairWrite{State: "not_written"}, Tests: SourcePairWrite{State: "not_written"}}
	if err := cancelled(ctx); err != nil {
		return result, err
	}
	create := req.Intent == "create"
	if req.Validate == nil || req.Preflight == nil || !create && (req.Intent != "edit" || req.Current == nil || req.ExpectedRevision == "") ||
		create && (req.ExpectedRevision != "" || !req.TestsPresent || req.CreateDefaultTests || req.Tests.Explicit) ||
		!req.TestsPresent && len(req.TestsBytes) != 0 || req.CreateDefaultTests && (!req.TestsPresent || req.Tests.Explicit) {
		return result, refusal("invalid_options")
	}
	// Own inbound bytes before acquisition or any trusted callback can change them.
	req.AgentBytes, req.TestsBytes = bytes.Clone(req.AgentBytes), bytes.Clone(req.TestsBytes)
	b, err := acquireSourcePair(ctx, req)
	if err != nil {
		return result, err
	}
	defer b.close()
	changes := sourcePairChanges{req.AgentBytes, req.TestsBytes, req.TestsPresent}
	candidate := b.tuple(req.Tests, changes.agentBytes, changes.testsBytes, changes.testsPresent)
	facts, err := publishHeldSourcePair(ctx, b, changes, sourcePairPublishHooks{
		check: b.check, checkPublishing: b.checkPublishing, prepareAbsentParent: b.prepareAbsentParent,
		prepare: func(ctx context.Context, prospective sourcePairPublicationResult) error {
			if !create {
				if err := req.Current(ctx); err != nil {
					return err
				}
			}
			// Bind Current's checked load to these held names before validating bytes.
			// Do not rerun Current after our own stages have dirtied directory times.
			if err := b.check(ctx); err != nil {
				return err
			}
			defaultTests, _ := agentfile.DefaultTestsPath(b.agentPath)
			if !req.Tests.Explicit && b.testsPath != defaultTests {
				return refusal("invalid_options")
			}
			if !create {
				baseline := b.heldTuple(req.Tests)
				if err := req.Validate(baseline.copy()); err != nil {
					return err
				}
				if baseline.Revision != req.ExpectedRevision {
					return refusal("stale_revision")
				}
				if b.tests != nil && (!req.TestsPresent || req.CreateDefaultTests) || b.tests == nil && req.TestsPresent && !req.CreateDefaultTests {
					return refusal("invalid_options")
				}
			}
			if err := req.Validate(candidate.copy()); err != nil {
				return err
			}
			return req.Preflight(candidate.copy(), SourcePairPublishResult{prospective.Agent, prospective.Tests, candidate.Revision})
		},
	})
	result.Agent, result.Tests = facts.Agent, facts.Tests
	if err == nil {
		// The engine's final held-object check established these bytes and names.
		// Logical native identity belongs here, never in the raw writer.
		result.Revision = b.heldTuple(req.Tests).Revision
	}
	return result, err
}

func (b *sourcePairBinding) tuple(selection agentfile.TestsSelection, agentBytes, testsBytes []byte, present bool) SourcePairTuple {
	tuple := SourcePairTuple{AgentPath: b.agentPath, Tests: agentfile.TestsSelection{Path: b.testsPath, Explicit: selection.Explicit},
		AgentBytes: bytes.Clone(agentBytes), TestsBytes: bytes.Clone(testsBytes), TestsPresent: present, AgentRevision: revision(agentBytes)}
	state := "absent"
	if present {
		state, tuple.TestsRevision = "external", revision(testsBytes)
	}
	tuple.Tests.Key, tuple.Revision = agentfile.SourceRevisions(b.agentPath, tuple.AgentRevision, b.testsPath, state, tuple.TestsRevision)
	return tuple
}

func (b *sourcePairBinding) heldTuple(selection agentfile.TestsSelection) SourcePairTuple {
	var tests []byte
	if b.tests != nil {
		tests = b.tests.source
	}
	return b.tuple(selection, b.agent.source, tests, b.tests != nil)
}

func (tuple SourcePairTuple) copy() SourcePairTuple {
	tuple.AgentBytes, tuple.TestsBytes = bytes.Clone(tuple.AgentBytes), bytes.Clone(tuple.TestsBytes)
	return tuple
}

// sourcePairAcquireWindow lets tests swap the filesystem at exact observation
// boundaries ("before_open", "after_hold"); production never sets it.
var sourcePairAcquireWindow func(stage string)

func acquireWindow(stage string) {
	if sourcePairAcquireWindow != nil {
		sourcePairAcquireWindow(stage)
	}
}

// sourceAncestor is one retained, existing directory in a role's physical path.
type sourceAncestor struct {
	path string
	info os.FileInfo
}

// sourcePairBinding owns the two held source roles at fixed physical paths and
// the identity of every ancestor directory observed on the way to them. It holds
// descriptors and identities only: no parsed agent, history or cached version.
// created records directories this operation made, the only new ones it accepts.
type sourcePairBinding struct {
	agentPath, testsPath string
	agentAbsent          *absentSource
	agent, tests         *historySource
	absent               *absentSource
	chains               [2][]sourceAncestor // agent, tests; nearest first
	created              map[string]os.FileInfo
}

func (b *sourcePairBinding) close() {
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

// sourceRolePath accepts only absolute paths without parent traversal, then cleans.
func sourceRolePath(p string) (string, bool) {
	if !filepath.IsAbs(p) {
		return "", false
	}
	for _, part := range strings.Split(filepath.ToSlash(p[len(filepath.VolumeName(p)):]), "/") {
		if part == ".." {
			return "", false
		}
	}
	return filepath.Clean(p), true
}

// acquireSourcePair holds both roles of a native pair at the request's physical
// paths without reading their content as sources. Create holds absence for both;
// Edit holds the agent and the selected tests, or the default tests' absence.
func acquireSourcePair(ctx context.Context, req SourcePairPublishRequest) (*sourcePairBinding, error) {
	if err := cancelled(ctx); err != nil {
		return nil, err
	}
	create := req.Intent == "create"
	if !create && req.Intent != "edit" || create && req.Tests.Explicit {
		return nil, refusal("invalid_options")
	}
	agent, okAgent := sourceRolePath(req.AgentPath)
	tests, okTests := sourceRolePath(req.Tests.Path)
	if _, supported := agentfile.DefaultTestsPath(agent); !okAgent || !okTests || !supported || agent == tests {
		return nil, refusal("unsafe_path")
	}
	b := &sourcePairBinding{agentPath: agent, testsPath: tests}
	if err := b.hold(ctx, create, req.Tests.Explicit); err != nil {
		b.close()
		return nil, err
	}
	return b, nil
}

func (b *sourcePairBinding) hold(ctx context.Context, create, explicit bool) (err error) {
	agentNeed, testsNeed := "present", ""
	if create {
		agentNeed, testsNeed = "absent", "absent"
	} else if explicit {
		testsNeed = "present"
	}
	if b.agent, b.agentAbsent, b.chains[0], err = holdSourceRole(b.agentPath, agentNeed, false); err != nil {
		return err
	}
	if err = cancelled(ctx); err != nil {
		return err
	}
	if b.tests, b.absent, b.chains[1], err = holdSourceRole(b.testsPath, testsNeed, true); err != nil {
		return err
	}
	if b.agent != nil && b.tests != nil && os.SameFile(b.agent.info, b.tests.info) {
		return refusal("unsafe_path")
	}
	acquireWindow("after_hold")
	if err = cancelled(ctx); err != nil {
		return err
	}
	return b.check(ctx)
}

// walkSourceAncestors Lstats every ancestor, nearest first. Only a real not-exist
// is absence; a symlink, reparse point, non-directory or unreadable ancestor refuses.
func walkSourceAncestors(path string) ([]sourceAncestor, error) {
	var chain []sourceAncestor
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		switch {
		case err == nil:
			if !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
				return nil, refusal("unsafe_path")
			}
			chain = append(chain, sourceAncestor{p, info})
		case !errors.Is(err, fs.ErrNotExist):
			return nil, refusal("unsafe_path")
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	if len(chain) == 0 {
		return nil, refusal("unsafe_path")
	}
	return chain, nil
}

func ancestorInfo(chain []sourceAncestor, path string) os.FileInfo {
	for _, a := range chain {
		if a.path == path {
			return a.info
		}
	}
	return nil
}

// holdSourceRole walks the path, holds its nearest existing directory and then
// the source or its absence. need is "present", "absent" or "" (either).
func holdSourceRole(path, need string, tests bool) (*historySource, *absentSource, []sourceAncestor, error) {
	chain, err := walkSourceAncestors(path)
	if err != nil {
		return nil, nil, nil, err
	}
	acquireWindow("before_open")
	parent, err := os.OpenRoot(chain[0].path)
	if err != nil {
		return nil, nil, nil, refusal("unsafe_path")
	}
	fail := func(err error) (*historySource, *absentSource, []sourceAncestor, error) {
		parent.Close()
		return nil, nil, nil, err
	}
	pinned, err := parent.Stat(".")
	if err != nil || !os.SameFile(pinned, chain[0].info) {
		return fail(refusal("stale_revision"))
	}
	rel, err := filepath.Rel(chain[0].path, path)
	if err != nil {
		return fail(refusal("unsafe_path"))
	}
	name, _, deeper := strings.Cut(filepath.ToSlash(rel), "/")
	if need != "absent" && !deeper {
		file, body, info, err := readTarget(parent, name)
		if err == nil {
			return &historySource{path, parent, file, info, pinned, body, tests}, nil, chain, nil
		}
		if need == "present" || !sourceNotFound(err) {
			return fail(err)
		}
	}
	if need == "present" {
		return fail(refusal("not_found"))
	}
	switch _, err := parent.Lstat(name); {
	case err == nil && need == "absent":
		return fail(refusal("already_exists"))
	case err == nil:
		return fail(refusal("stale_revision"))
	case !errors.Is(err, fs.ErrNotExist):
		return fail(refusal("unsafe_path"))
	}
	return nil, &absentSource{path, path, name, parent, pinned}, chain, nil
}

// check is strict: directory times must match the held observation too.
func (b *sourcePairBinding) check(ctx context.Context) error {
	if err := cancelled(ctx); err != nil {
		return err
	}
	return b.verify(true)
}

// checkPublishing tolerates our own staging's directory-time changes only; every
// identity, ancestor, byte and absence check still applies.
func (b *sourcePairBinding) checkPublishing(ctx context.Context) error {
	if err := cancelled(ctx); err != nil {
		return err
	}
	return b.verify(false)
}

func (b *sourcePairBinding) verify(strict bool) error {
	stale := refusal("stale_revision")
	for i, r := range []struct {
		path   string
		file   *historySource
		absent *absentSource
	}{{b.agentPath, b.agent, b.agentAbsent}, {b.testsPath, b.tests, b.absent}} {
		if r.file == nil && r.absent == nil {
			continue
		}
		// Walk first: a symlink, reparse or irregular ancestor refuses even when
		// the named parent still resolves to the held descriptor.
		now, err := walkSourceAncestors(r.path)
		if err != nil {
			return err
		}
		for _, a := range now {
			want := ancestorInfo(b.chains[i], a.path)
			if want == nil {
				want = b.created[a.path]
			}
			if want == nil || !os.SameFile(want, a.info) {
				return stale
			}
		}
		for _, a := range b.chains[i] {
			if ancestorInfo(now, a.path) == nil {
				return stale
			}
		}
		var parent *os.Root
		var pinned os.FileInfo
		if r.file != nil {
			parent, pinned = r.file.parent, r.file.parentInfo
		} else {
			parent, pinned = r.absent.parent, r.absent.info
		}
		held, err := parent.Stat(".")
		if err != nil || !os.SameFile(held, pinned) || strict && !held.ModTime().Equal(pinned.ModTime()) {
			return stale
		}
		if named := ancestorInfo(now, filepath.Clean(parent.Name())); named == nil || !os.SameFile(named, held) {
			return stale
		}
		if r.file == nil {
			info, err := parent.Lstat(r.absent.name)
			if err == nil {
				// Only a directory this operation made may occupy the probe name.
				own := b.created[filepath.Join(parent.Name(), r.absent.name)]
				if own == nil || !os.SameFile(info, own) {
					return stale
				}
			} else if !errors.Is(err, fs.ErrNotExist) {
				return stale
			}
			continue
		}
		if !r.file.stillNamed(parent) {
			return stale
		}
	}
	return nil
}

// stillNamed compares the held descriptor, its name in the held parent and its
// bytes with the original observation.
func (s *historySource) stillNamed(parent *os.Root) bool {
	named, err := parent.Lstat(filepath.Base(s.path))
	opened, statErr := s.file.Stat()
	if err != nil || statErr != nil || !singleLink(s.file) {
		return false
	}
	for _, current := range []os.FileInfo{named, opened} {
		if !os.SameFile(current, s.info) || current.Mode() != s.info.Mode() || current.Size() != s.info.Size() || !current.ModTime().Equal(s.info.ModTime()) {
			return false
		}
	}
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(s.file, maxFileBytes+1))
	return err == nil && bytes.Equal(body, s.source)
}

// prepareAbsentParent creates this operation's missing directories, checking both
// roles' ancestor chains against held identities and the created map before each.
func (b *sourcePairBinding) prepareAbsentParent(a *absentSource, created map[string]os.FileInfo) error {
	b.created = created
	return prepareParents(a, a.path, created, func() error { return b.verify(false) })
}
