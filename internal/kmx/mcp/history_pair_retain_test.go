//go:build linux || darwin

package mcp

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

func retainedPair(t *testing.T, path string, source, tests []byte) *agentfile.Pair {
	t.Helper()
	a, err := agentfile.Parse(path, source)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := agentfile.DefaultTestsPath(path)
	p, err := agentfile.ComposePair(path, a, agentfile.TestsSelection{Path: target}, tests, tests != nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHistoryRetainPairDedupeOnlyLatestSameKind(t *testing.T) {
	base, root := historyPaths(t)
	s := historyStoreForTest(t, base, root, true)
	source := []byte("Prompt\n")
	p := retainedPair(t, filepath.Join(root, "a.agent.md"), source, []byte(historyTests))
	for i, paired := range []bool{true, true, false, true, true, false, false} {
		var err error
		if paired {
			err = s.retainPair(t.Context(), "a.agent.md", p, source)
		} else {
			err = s.retain(t.Context(), "a.agent.md", source)
		}
		if err != nil {
			t.Fatal(err)
		}
		rows, err := s.list("a.agent.md")
		if err != nil || len(rows) != []int{1, 1, 2, 3, 3, 4, 4}[i] {
			t.Fatalf("step %d collapsed mixed history: %+v %v", i, rows, err)
		}
	}
}

func TestHistoryRetainPairRejectsInvalidPreimageBeforeEffects(t *testing.T) {
	base, root := historyPaths(t)
	s := historyStoreForTest(t, base, root, true)
	s.limits.perFile = 1
	path := filepath.Join(root, "a.agent.md")
	source := []byte("---\ntools: [lookup]\n---\nPrompt\n")
	tests := []byte("---\nschemaVersion: 1\n---\n```yaml agent-test\nid: a\ninput: Ada\nassertions: [{id: a, type: toolCalled, tool: lookup}]\n```\n")
	p := retainedPair(t, path, source, tests)
	if err := s.retainPair(t.Context(), "a.agent.md", p, source); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(s.root.Name())
	if err != nil {
		t.Fatal(err)
	}
	crossSource := []byte("---\ntools: [other]\n---\nPrompt\n")
	crossAgent, err := agentfile.Parse(path, crossSource)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentfile.ParseTests(p.Tests.Selection.Path, tests); err != nil {
		t.Fatal(err)
	}
	if _, err := agentfile.ComposePair(path, crossAgent, p.Tests.Selection, tests, true); err == nil {
		t.Fatal("fixture must be independently valid but cross-invalid")
	}
	cross := *p
	cross.Agent = crossAgent
	for _, row := range []struct {
		name string
		pair *agentfile.Pair
		raw  []byte
	}{
		{"cross-invalid", &cross, crossSource},
		{"wrong-raw-source", p, []byte("Different prompt\n")},
	} {
		t.Run(row.name, func(t *testing.T) {
			var refusal *Error
			if err := s.retainPair(t.Context(), "a.agent.md", row.pair, row.raw); !errors.As(err, &refusal) || refusal.Code != "history_unavailable" {
				t.Fatalf("invalid preimage not classified: %v", err)
			}
			after, err := os.ReadDir(s.root.Name())
			if err != nil || len(after) != len(before) {
				t.Fatalf("invalid preimage changed inventory: %v", err)
			}
			for i := range before {
				if before[i].Name() != after[i].Name() {
					t.Fatal("invalid preimage pruned/published a record")
				}
			}
			rows, err := s.list("a.agent.md")
			if err != nil || len(rows) != 1 || rows[0].SnapshotRevision != p.SnapshotRevision {
				t.Fatalf("invalid preimage damaged history: %+v %v", rows, err)
			}
		})
	}
}

func TestHistoryRetainPairExactSourcesAndTestsOnlyChange(t *testing.T) {
	base, root := historyPaths(t)
	s := historyStoreForTest(t, base, root, true)
	source := []byte("# Agent\r\nPrompt Māori\r\n")
	path := filepath.Join(root, "never-created.agent.md")
	for i, tests := range [][]byte{[]byte(historyTests), []byte(historyTests + "\nChanged prose\r\n"), nil} {
		p := retainedPair(t, path, source, tests)
		if err := s.retainPair(t.Context(), "a.agent.md", p, source); err != nil {
			t.Fatal(err)
		}
		rows, err := s.list("a.agent.md")
		if err != nil || len(rows) != i+1 {
			t.Fatalf("distinct pair lost: %+v %v", rows, err)
		}
		r, err := s.get("a.agent.md", rows[0].VersionID)
		if err != nil || r.SchemaVersion != 2 || r.Pair == nil || !bytes.Equal(r.Source, source) || !bytes.Equal(r.Pair.Source, tests) {
			t.Fatalf("exact pair lost: %+v %v", r, err)
		}
		h := r.Pair
		if h.AgentPath != path || h.TestsPath != p.Tests.Selection.Path || h.Explicit != p.Tests.Selection.Explicit || h.TestsState != p.Tests.State || h.TestsRevision != p.Tests.Revision || h.SelectionKey != p.Tests.Selection.Key || h.SnapshotRevision != p.SnapshotRevision || rows[0].SnapshotRevision != p.SnapshotRevision || rows[0].SourceKind != "pair" {
			t.Fatalf("incomplete pair: %+v / %+v", h, rows[0])
		}
	}
}
