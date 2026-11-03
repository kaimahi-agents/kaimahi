//go:build linux || darwin

package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

const historyTests = "---\nschemaVersion: 1\n---\n# Prose stays\r\n\n```yaml agent-test\nid: greeting\ninput: Ada\nassertions: [{id: greeting-text, type: contains, value: Ada}]\n```\n"

func seedPairHistory(t *testing.T, s *historyStore, agentPath, key string, source, tests []byte, seq int) (string, *agentfile.Pair) {
	t.Helper()
	a, err := agentfile.Parse(agentPath, source)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := agentfile.DefaultTestsPath(agentPath)
	p, err := agentfile.ComposePair(agentPath, a, agentfile.TestsSelection{Path: target}, tests, tests != nil)
	if err != nil {
		t.Fatal(err)
	}
	return seedHistoryPair(t, s, key, source, p, seq), p
}

func seedHistoryPair(t *testing.T, s *historyStore, key string, source []byte, p *agentfile.Pair, seq int) string {
	t.Helper()
	var tests []byte
	if p.Tests.State == "external" {
		tests = p.Tests.Suite.Source
	}
	id := fmt.Sprintf("%020d-%032x", seq, seq)
	r := map[string]any{"schemaVersion": 2, "versionId": id, "fileKey": historyHash(key), "revision": p.Agent.Revision, "capturedAt": time.Now().UTC(), "source": source, "pair": map[string]any{"agentPath": p.AgentPath, "testsPath": p.Tests.Selection.Path, "explicit": p.Tests.Selection.Explicit, "testsState": p.Tests.State, "testsRevision": p.Tests.Revision, "selectionKey": p.Tests.Selection.Key, "snapshotRevision": p.SnapshotRevision, "source": tests}}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.root.Name(), id+".json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestHistoryPairRecordReadsExactSourcesAndAbsence(t *testing.T) {
	for _, tests := range [][]byte{[]byte(historyTests), nil} {
		t.Run(fmt.Sprint(tests == nil), func(t *testing.T) {
			base, root := historyPaths(t)
			s := historyStoreForTest(t, base, root, true)
			source := []byte("# Agent\r\nPrompt Māori\r\n")
			id, p := seedPairHistory(t, s, filepath.Join(root, "a.agent.md"), "a.agent.md", source, tests, 1)
			r, err := s.get("a.agent.md", id)
			if err != nil || string(r.Source) != string(source) {
				t.Fatalf("complete history lost: %+v %v", r, err)
			}
			b, _ := json.Marshal(r)
			var raw map[string]json.RawMessage
			_ = json.Unmarshal(b, &raw)
			var metadata map[string]json.RawMessage
			_ = json.Unmarshal(raw["pair"], &metadata)
			var snapshot string
			_ = json.Unmarshal(metadata["snapshotRevision"], &snapshot)
			var retained []byte
			_ = json.Unmarshal(metadata["source"], &retained)
			if snapshot != p.SnapshotRevision || string(retained) != string(tests) {
				t.Fatal("pair source/snapshot lost")
			}
			rows, err := s.list("a.agent.md")
			if err != nil || len(rows) != 1 {
				t.Fatalf("pair not listed: %+v %v", rows, err)
			}
			encoded, _ := json.Marshal(rows[0])
			if !strings.Contains(string(encoded), `"sourceKind":"pair"`) {
				t.Fatalf("pair mislabeled: %s", encoded)
			}
			if _, err := s.get("b.agent.md", id); err == nil {
				t.Fatal("shared suite crossed agent subject")
			}
		})
	}
}

func TestHistoryPairCorruptionAndLegacySeparation(t *testing.T) {
	for _, field := range []string{"snapshotRevision", "selectionKey", "testsRevision", "testsState", "source", "agentPath", "oversized-tests", "v1-pair", "missing-pair", "unknown-field", "trailing-json"} {
		t.Run(field, func(t *testing.T) {
			base, root := historyPaths(t)
			s := historyStoreForTest(t, base, root, true)
			id, _ := seedPairHistory(t, s, filepath.Join(root, "a.agent.md"), "a.agent.md", []byte("Prompt\n"), []byte(historyTests), 1)
			name := filepath.Join(s.root.Name(), id+".json")
			b, _ := os.ReadFile(name)
			var r map[string]any
			_ = json.Unmarshal(b, &r)
			pair := r["pair"].(map[string]any)
			switch field {
			case "oversized-tests":
				pair["source"] = []byte(strings.Repeat("x", maxFileBytes+1))
			case "v1-pair":
				r["schemaVersion"] = 1
			case "missing-pair":
				delete(r, "pair")
			case "unknown-field":
				pair["futureAuthority"] = true
			case "trailing-json":
			default:
				pair[field] = "broken"
			}
			b, _ = json.Marshal(r)
			if field == "trailing-json" {
				b = append(b, []byte(`{}`)...)
			}
			_ = os.WriteFile(name, b, 0600)
			if _, err := s.get("a.agent.md", id); err == nil {
				t.Fatal("corrupt pair became a snapshot")
			}
			if err := s.retain(t.Context(), "b.agent.md", []byte("legacy")); err != nil {
				t.Fatal(err)
			}
			rows, err := s.list("b.agent.md")
			if err != nil || len(rows) != 1 {
				t.Fatalf("bad pair poisoned legacy: %+v %v", rows, err)
			}
			encoded, _ := json.Marshal(rows[0])
			if !strings.Contains(string(encoded), "Agent only—tests not retained") {
				t.Fatalf("v1 claimed complete: %s", encoded)
			}
		})
	}
}

func TestHistoryLegacyRetainDoesNotDedupAgainstPair(t *testing.T) {
	base, root := historyPaths(t)
	s := historyStoreForTest(t, base, root, true)
	source := []byte("Prompt\n")
	seedPairHistory(t, s, filepath.Join(root, "a.agent.md"), "a.agent.md", source, nil, 1)
	if err := s.retain(t.Context(), "a.agent.md", source); err != nil {
		t.Fatal(err)
	}
	rows, err := s.list("a.agent.md")
	if err != nil || len(rows) != 2 {
		t.Fatalf("agent hash collapsed pair state: %+v %v", rows, err)
	}
	if err := s.retain(t.Context(), "a.agent.md", source); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.list("a.agent.md")
	if len(rows) != 2 {
		t.Fatal("v1 repeated preimage stopped deduplicating")
	}
}
