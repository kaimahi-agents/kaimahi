package mcp

import (
	"path/filepath"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

// v2 sources are data, never authority to reopen a retained path or restore it.
// Publication of new pair preimages belongs to the paired MCP writer.
type historyPair struct {
	AgentPath        string `json:"agentPath"`
	TestsPath        string `json:"testsPath"`
	Explicit         bool   `json:"explicit"`
	TestsState       string `json:"testsState"`
	TestsRevision    string `json:"testsRevision"`
	SelectionKey     string `json:"selectionKey"`
	SnapshotRevision string `json:"snapshotRevision"`
	Source           []byte `json:"source"`
}

func (r historyRecord) validSourceKind() bool {
	if r.SchemaVersion == 1 {
		return r.Pair == nil
	}
	if r.SchemaVersion != 2 || r.Pair == nil {
		return false
	}
	h := r.Pair
	if len(h.Source) > maxFileBytes || !utf8.Valid(h.Source) || (h.TestsState != "external" && len(h.Source) != 0) || !filepath.IsAbs(h.AgentPath) || !agentPath(h.AgentPath) {
		return false
	}
	// MCP may resolve a default alias to another physical target. Explicit is
	// selection intent, not identity; validate the retained binding without IO.
	agent, err := agentfile.Parse(h.AgentPath, r.Source)
	if err != nil {
		return false
	}
	pair, err := agentfile.ComposePair(h.AgentPath, agent, agentfile.TestsSelection{Path: h.TestsPath, Explicit: h.Explicit}, h.Source, h.TestsState == "external")
	return err == nil && pair.Tests.State == h.TestsState && pair.Tests.Revision == h.TestsRevision && pair.Tests.Selection.Key == h.SelectionKey && pair.SnapshotRevision == h.SnapshotRevision
}

func sourceVersion(r historyRecord) AgentVersion {
	v := AgentVersion{VersionID: r.VersionID, Revision: r.Revision, CapturedAt: r.CapturedAt, SourceBytes: len(r.Source), SourceKind: "agent_only", Label: "Agent only—tests not retained"}
	if p := r.Pair; p != nil {
		v.SourceKind, v.Label = "pair", "Agent and tests snapshot"
		v.SourceBytes += len(p.Source)
		v.SnapshotRevision, v.TestsState, v.TestsRevision, v.SelectionKey = p.SnapshotRevision, p.TestsState, p.TestsRevision, p.SelectionKey
	}
	return v
}

func pairChangeLabel(v AgentVersion, newer *agentfile.Pair) string {
	if v.SourceKind != "pair" || newer == nil {
		return v.Label
	}
	agent := v.Revision != newer.Agent.Revision
	tests := v.TestsState != newer.Tests.State || v.TestsRevision != newer.Tests.Revision || v.SelectionKey != newer.Tests.Selection.Key
	switch {
	case agent && tests:
		return "Both changed"
	case agent:
		return "Agent changed"
	case tests:
		return "Tests changed"
	default:
		return "No source change"
	}
}
