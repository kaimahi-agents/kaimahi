//go:build linux || darwin

package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSourceHistoryRootlessRepoAndExplicitAlias(t *testing.T) {
	for _, repo := range []bool{false, true} {
		t.Run(map[bool]string{false: "override", true: "repo"}[repo], func(t *testing.T) {
			base, root := historyPaths(t)
			f := historyFiles(t, "", base)
			path := filepath.Join(root, "a.agent.md")
			tests := filepath.Join(root, "selected.any")
			override := tests
			if repo {
				path = filepath.Join(root, ".github", "agents", "a.agent.md")
				tests, _ = agentfile.DefaultTestsPath(path)
				override = ""
			}
			for _, p := range []string{path, tests} {
				if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, []byte("Prompt\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(tests, []byte(historyTests), 0600); err != nil {
				t.Fatal(err)
			}
			if !repo {
				alias := filepath.Join(root, "alias.any")
				if err := os.Symlink(tests, alias); err != nil {
					t.Fatal(err)
				}
				override = alias
			}
			cs := historyClient(t, f)
			result, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: sourcesURI(path, override)})
			if err != nil {
				t.Fatal(err)
			}
			var d sourceDocument
			if err := json.Unmarshal([]byte(result.Contents[0].Text), &d); err != nil {
				t.Fatal(err)
			}
			if d.Kind != "kmx.agent-sources" || d.Tests.Path != tests || d.Tests.Source != historyTests || d.Tests.State != "external" || d.History.Status != "available" {
				t.Fatalf("rootless selection lost: %+v", d)
			}
			if _, err := os.Stat(base); !os.IsNotExist(err) {
				t.Fatal("source read created history")
			}
		})
	}
}

func TestSourceHistoryHeldReadsRefuseReplacement(t *testing.T) {
	for _, parent := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "parent"}[parent], func(t *testing.T) {
			base, root := historyPaths(t)
			f := historyFiles(t, root, base)
			if _, err := f.Create(t.Context(), "a.agent.md", []byte("Prompt\n")); err != nil {
				t.Fatal(err)
			}
			s, err := f.readHistorySource("a.agent.md", false)
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			if !s.unchanged(f) {
				t.Fatal("fresh read not checked")
			}
			path := filepath.Join(root, "a.agent.md")
			if parent {
				if err := os.Rename(root, root+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Rename(path, path+"-old"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("Prompt\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if s.unchanged(f) {
				t.Fatal("equal bytes concealed checked source replacement")
			}
		})
	}
}

func TestSourceHistoryUnavailableVersionsKeepFixedReason(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, root, base)
	if _, err := f.Create(t.Context(), "a.agent.md", []byte("Prompt\n")); err != nil {
		t.Fatal(err)
	}
	held := historyStoreForTest(t, base, root, true)
	id, _ := seedPairHistory(t, held, filepath.Join(root, "a.agent.md"), "a.agent.md", []byte("Prompt\n"), nil, 1)
	cs := historyClient(t, f)
	result, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: sourcesURI("a.agent.md", "")})
	if err != nil {
		t.Fatal(err)
	}
	var d sourceDocument
	if json.Unmarshal([]byte(result.Contents[0].Text), &d) != nil || d.Kind != "kmx.agent-sources" || d.History.Status != "unavailable" {
		t.Fatal("history lock hid current sources")
	}
	result, err = cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: pairVersionURI("a.agent.md", id)})
	if err != nil {
		t.Fatal(err)
	}
	var failure struct{ Kind, ReasonCode string }
	if json.Unmarshal([]byte(result.Contents[0].Text), &failure) != nil || failure.Kind != "kmx.source-history-error" || failure.ReasonCode != "history_unavailable" {
		t.Fatalf("history refusal not classified: %s", result.Contents[0].Text)
	}
}

func TestHistoryPairChangeLabelsAndMixedEviction(t *testing.T) {
	base, root := historyPaths(t)
	s := historyStoreForTest(t, base, root, true)
	source := []byte("Prompt\n")
	_, original := seedPairHistory(t, s, filepath.Join(root, "a.agent.md"), "a.agent.md", source, []byte(historyTests), 1)
	rows, _ := s.list("a.agent.md")
	row := rows[0]
	for _, change := range []string{"agent", "tests", "both", "selection", "none"} {
		t.Run(change, func(t *testing.T) {
			current := *original
			agent := *original.Agent
			current.Agent = &agent
			if change == "agent" || change == "both" {
				current.Agent.Revision = revision([]byte("new agent"))
			}
			if change == "tests" || change == "both" {
				current.Tests.Revision = revision([]byte("new tests"))
			}
			if change == "selection" {
				current.Tests.Selection.Key = revision([]byte("new selection"))
			}
			want := map[string]string{"agent": "Agent changed", "tests": "Tests changed", "both": "Both changed", "selection": "Tests changed", "none": "No source change"}[change]
			if got := pairChangeLabel(row, &current); got != want {
				t.Fatalf("%s != %s", got, want)
			}
		})
	}
	s.limits.perFile = 2
	for _, b := range [][]byte{[]byte("second"), []byte("third")} {
		if err := s.retain(t.Context(), "a.agent.md", b); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.list("a.agent.md")
	if err != nil || len(rows) != 2 || rows[1].SourceKind != "agent_only" {
		t.Fatalf("pair did not consume perfile capacity: %+v %v", rows, err)
	}
	s.limits.bytes = 500
	if err := s.retain(t.Context(), "b.agent.md", source); err != nil {
		t.Fatal(err)
	}
	entries, err := s.scan(true)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, e := range entries {
		total += e.info.Size()
	}
	if total > s.limits.bytes {
		t.Fatal("mixed history escaped encoded byte budget")
	}
}
