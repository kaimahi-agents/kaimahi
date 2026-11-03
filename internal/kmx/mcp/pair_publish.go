package mcp

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

type pairSourceAck struct {
	Status   string `json:"status"`
	Revision string `json:"revision,omitempty"`
}
type pairSaveResult struct {
	SchemaVersion    int                `json:"schemaVersion"`
	Kind             string             `json:"kind"`
	State            string             `json:"state"`
	ReasonCode       string             `json:"reasonCode,omitempty"`
	File             File               `json:"file"`
	Agent            pairSourceAck      `json:"agent"`
	Tests            pairSourceAck      `json:"tests"`
	SnapshotRevision string             `json:"snapshotRevision,omitempty"`
	SourceVersion    string             `json:"sourceVersion,omitempty"`
	History          *FileHistoryStatus `json:"history,omitempty"`
}

// Own stages change directory times; keep identity, source version, root and
// selected-absence checks. Runtime validation never uses this relaxed-time check.
func (b *pairBinding) checkPublishing(ctx context.Context) error {
	clone := *b
	for _, slot := range []**historySource{&clone.agent, &clone.tests} {
		if *slot != nil {
			h := **slot
			info, err := h.parent.Stat(".")
			if err != nil || !os.SameFile(info, h.parentInfo) {
				return refusal("stale_revision")
			}
			if _, err := h.file.Seek(0, 0); err != nil {
				return refusal("stale_revision")
			}
			body, err := io.ReadAll(io.LimitReader(h.file, maxFileBytes+1))
			if err != nil || !bytes.Equal(body, h.source) {
				return refusal("stale_revision")
			}
			h.parentInfo = info
			*slot = &h
		}
	}
	for _, slot := range []**absentSource{&clone.agentAbsent, &clone.absent} {
		if *slot != nil {
			h := **slot
			info, err := h.parent.Stat(".")
			if err != nil || !os.SameFile(info, h.info) {
				return refusal("stale_revision")
			}
			h.info = info
			*slot = &h
		}
	}
	return clone.check(ctx)
}

// Create only components absent in the checked binding, or components this same
// operation already created. Never adopt a concurrently appearing parent.
func (b *pairBinding) prepareAbsentParent(a *absentSource, created map[string]os.FileInfo) error {
	target := b.files.physicalSource(a.path)
	rel, err := filepath.Rel(filepath.Clean(a.parent.Name()), filepath.Dir(target))
	if err != nil || rel != "." && !filepath.IsLocal(rel) {
		return refusal("unsafe_path")
	}
	parent := a.parent
	defer func() {
		if parent != a.parent {
			parent.Close()
		}
	}()
	if rel != "." {
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			key := filepath.Join(parent.Name(), part)
			info, e := parent.Lstat(part)
			if os.IsNotExist(e) {
				if e = parent.Mkdir(part, 0755); e != nil {
					return refusal("stale_revision")
				}
			} else if e != nil || created[key] == nil || !os.SameFile(info, created[key]) || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return refusal("stale_revision")
			}
			child, e := parent.OpenRoot(part)
			if e != nil {
				return refusal("unsafe_path")
			}
			info, e = child.Stat(".")
			if e != nil {
				child.Close()
				return refusal("unsafe_path")
			}
			created[key] = info
			if parent != a.parent {
				parent.Close()
			}
			parent = child
		}
	}
	if parent != a.parent {
		a.parent.Close()
		a.parent = parent
	}
	a.probe = a.path
	a.name = filepath.Base(a.path)
	a.info, err = a.parent.Stat(".")
	return err
}

func (b *pairBinding) retainPairHistory(ctx context.Context) (*historyStore, *FileHistoryStatus) {
	unavailable := &FileHistoryStatus{"unavailable", "history_unavailable", "Saved, but the previous version could not be confirmed in history."}
	if b.files.historyDir == nil || b.pair == nil {
		return nil, unavailable
	}
	root, key, err := b.files.historyLocation(b.sources.AgentPath)
	var s *historyStore
	if err == nil {
		s, err = b.files.openHistory(ctx, root, true)
	}
	if err == nil {
		err = s.retainPair(ctx, key, b.pair, b.agent.source)
	}
	if err != nil {
		if s != nil {
			s.close()
		}
		return nil, unavailable
	}
	return s, &FileHistoryStatus{"retained", "history_retained", "Saved; the previous version is retained in local history."}
}

type pairPublication struct {
	role    string
	current **historySource
	absent  **absentSource
	source  []byte
	present bool
	ack     *pairSourceAck
	parent  *os.Root
	stage   string
}

// Publication consumes the operation's checked descriptors, never an inbound
// arbitrary destination. Two renames/removals are not a transaction.
func (b *pairBinding) publish(ctx context.Context, desired *agentfile.Pair, agentBytes, testsBytes []byte) pairSaveResult {
	result := pairSaveResult{SchemaVersion: 3, Kind: "kmx.agent-pair-save", State: "not_saved", Agent: pairSourceAck{Status: "unchanged"}, Tests: pairSourceAck{Status: "unchanged"}}
	fail := func(err error) pairSaveResult {
		result.ReasonCode = errorResult(err).StructuredContent.(map[string]any)["reasonCode"].(string)
		if result.Agent.Status == "saved" || result.Tests.Status == "saved" {
			result.State = "partial"
		}
		if result.Agent.Status == "uncertain" || result.Tests.Status == "uncertain" {
			result.State = "uncertain"
		}
		if result.State == "not_saved" {
			result.History = nil
		}
		return result
	}
	mutations := []pairPublication{{"agent", &b.agent, &b.agentAbsent, agentBytes, true, &result.Agent, nil, ""}, {"tests", &b.tests, &b.absent, testsBytes, desired.Tests.State == "external", &result.Tests, nil, ""}}
	for i := range mutations {
		m := &mutations[i]
		h := *m.current
		if h == nil && m.present || h != nil && (!m.present || !bytes.Equal(h.source, m.source)) {
			m.ack.Status = "not_saved"
		}
	}
	// A small inbound encoding must not bypass the complete source-view budget.
	view := pairDocument(desired, agentBytes)
	view.SourceVersion = strings.Repeat("x", 128)
	view.Validation = "valid"
	view.History = &AgentHistory{Status: "unavailable", Versions: []AgentVersion{}}
	view.PairOperations = &pairOperationsCapability{SchemaVersion: 1, ToolSchemaVersion: 3, Execute: true, Create: true, Edit: true, Capture: true, Restore: true}
	if _, err := sourceResourceResult(ctx, "", view); err != nil {
		return fail(err)
	}
	var locked []*historySource
	for _, h := range []*historySource{b.agent, b.tests} {
		if h != nil {
			locked = append(locked, h)
		}
	}
	sort.Slice(locked, func(i, j int) bool { return locked[i].path < locked[j].path })
	for _, h := range locked {
		unlock, err := lockTarget(h.file)
		if err != nil {
			return fail(lockFailure(err))
		}
		defer unlock()
	}
	if err := b.check(ctx); err != nil {
		return fail(err)
	}
	changed := false
	created := map[string]os.FileInfo{}
	for i := range mutations {
		m := &mutations[i]
		h := *m.current
		if h == nil && !m.present || h != nil && m.present && bytes.Equal(h.source, m.source) {
			continue
		}
		changed = true
		m.ack.Status = "not_saved"
		if h != nil {
			m.parent = h.parent
		} else {
			if err := b.prepareAbsentParent(*m.absent, created); err != nil {
				return fail(err)
			}
			m.parent = (*m.absent).parent
		}
	}
	for i := range mutations {
		m := &mutations[i]
		if m.ack.Status == "unchanged" || !m.present {
			continue
		}
		if err := b.checkPublishing(ctx); err != nil {
			return fail(err)
		}
		mode, preserve := os.FileMode(0666), false
		if h := *m.current; h != nil {
			mode, preserve = h.info.Mode(), true
		}
		name, err := stage(m.parent, m.source, mode, preserve)
		if err != nil {
			return fail(err)
		}
		m.stage = name
		defer m.parent.Remove(name)
	}
	if changed && b.agent != nil {
		s, status := b.retainPairHistory(ctx)
		if s != nil {
			defer s.close()
		}
		result.History = status
	}
	for i := range mutations {
		m := &mutations[i]
		if m.ack.Status == "unchanged" {
			continue
		}
		if err := b.checkPublishing(ctx); err != nil {
			return fail(err)
		}
		h := *m.current
		var path string
		if h != nil {
			path = h.path
		} else {
			path = (*m.absent).path
		}
		name := filepath.Base(path)
		var err error
		var published os.FileInfo
		switch {
		case !m.present:
			err = m.parent.Remove(name)
		case h == nil:
			err = renameExclusiveObserved(m.parent, m.stage, name, &published)
		default:
			published, err = m.parent.Lstat(m.stage)
			if err == nil {
				err = m.parent.Rename(m.stage, name)
			}
		}
		if err != nil {
			if !os.IsExist(err) {
				m.ack.Status = "uncertain"
			}
			return fail(refusal("publication_failed"))
		}
		m.ack.Status = "saved"
		info, err := m.parent.Stat(".")
		if err != nil {
			m.ack.Status = "uncertain"
			return fail(refusal("io_failed"))
		}
		if h != nil {
			defer h.file.Close()
		}
		if !m.present {
			*m.current = nil
			*m.absent = &absentSource{path, path, name, m.parent, info}
		} else {
			file, body, observed, e := readTarget(m.parent, name)
			if e != nil || published == nil || !os.SameFile(observed, published) || !bytes.Equal(body, m.source) {
				if file != nil {
					file.Close()
				}
				m.ack.Status = "uncertain"
				return fail(refusal("stale_revision"))
			}
			*m.current = &historySource{path, m.parent, file, observed, info, m.source, m.role == "tests"}
			*m.absent = nil
			m.ack.Revision = revision(m.source)
		}
	}
	b.pair = desired
	b.sources = sourcesFor(desired)
	if err := b.checkPublishing(ctx); err != nil {
		return fail(err)
	}
	for _, h := range []*historySource{b.agent, b.tests} {
		if h != nil {
			info, err := h.parent.Stat(".")
			if err != nil {
				return fail(refusal("io_failed"))
			}
			h.parentInfo = info
		}
	}
	if b.absent != nil {
		info, err := b.absent.parent.Stat(".")
		if err != nil {
			return fail(refusal("io_failed"))
		}
		b.absent.info = info
	}
	result.State = "complete"
	result.File = File{Path: b.agent.path, Revision: desired.Agent.Revision}
	result.SnapshotRevision = desired.SnapshotRevision
	result.SourceVersion = b.sourceVersion()
	return result
}
