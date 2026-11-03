//go:build linux || darwin

package mcp

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

func TestSourceHistoryAcceptsStandardURIEncodings(t *testing.T) {
	base, root := historyPaths(t)
	root = filepath.Join(root, "c++ @ group")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "a@b.agent.md")
	tests, _ := agentfile.DefaultTestsPath(path)
	if err := os.WriteFile(path, []byte("Prompt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tests, []byte(historyTests), 0600); err != nil {
		t.Fatal(err)
	}
	f := historyFiles(t, "", base)
	cs := historyClient(t, f)
	component := strings.ReplaceAll(url.QueryEscape(path), "+", "%20")
	reserved := strings.ReplaceAll(path, " ", "%20")
	query := strings.ReplaceAll(url.QueryEscape(tests), "+", "%20")
	var first string
	for _, uri := range []string{"kmx://agent-sources/" + component, "kmx://agent-sources/" + reserved, "kmx://agent-sources/" + component + "?tests=" + query} {
		result, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: uri})
		if err != nil {
			t.Fatal(err)
		}
		var d sourceDocument
		if json.Unmarshal([]byte(result.Contents[0].Text), &d) != nil || d.Kind != "kmx.agent-sources" || d.Tests.Source != historyTests {
			t.Fatalf("advertised component/reserved encoding refused: %s", result.Contents[0].Text)
		}
		if first == "" {
			first = d.SnapshotRevision
		} else if first != d.SnapshotRevision {
			t.Fatal("URI spelling changed physical selection identity")
		}
	}
}

func TestSourceHistoryDotRelativeSelector(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, root, base)
	if _, err := f.Create(t.Context(), "a.agent.md", []byte("Prompt\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "checks.md"), []byte(historyTests), 0600); err != nil {
		t.Fatal(err)
	}
	cs := historyClient(t, f)
	var snapshot string
	for _, selected := range []string{"checks.md", "./checks.md"} {
		result, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: sourcesURI("a.agent.md", selected)})
		if err != nil {
			t.Fatal(err)
		}
		var d sourceDocument
		if json.Unmarshal([]byte(result.Contents[0].Text), &d) != nil || d.Kind != "kmx.agent-sources" || d.Tests.Path != filepath.Join(root, "checks.md") || d.Tests.Source != historyTests {
			t.Fatalf("ordinary dot selector refused: %s", result.Contents[0].Text)
		}
		if snapshot == "" {
			snapshot = d.SnapshotRevision
		} else if snapshot != d.SnapshotRevision {
			t.Fatal("dot spelling changed physical identity")
		}
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

func TestHistoryVerifiedDefaultAliasRoundTrip(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, root, base)
	source := []byte("Prompt\n")
	if _, err := f.Create(t.Context(), "a.agent.md", source); err != nil {
		t.Fatal(err)
	}
	tests := filepath.Join(root, "selected.any")
	if err := os.WriteFile(tests, []byte(historyTests), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(tests, filepath.Join(root, "a.agent.test.md")); err != nil {
		t.Fatal(err)
	}
	pair, b, err := f.readSourcePair(t.Context(), "a.agent.md", "")
	if err != nil {
		t.Fatal(err)
	}
	if pair.Tests.Selection.Explicit || pair.Tests.Selection.Path != tests {
		t.Fatal("fixture did not produce checked physical default alias")
	}
	s := historyStoreForTest(t, base, root, true)
	id := seedHistoryPair(t, s, "a.agent.md", b, pair, 1)
	rows, err := s.list("a.agent.md")
	if err != nil || len(rows) != 1 {
		t.Fatalf("verified default-alias snapshot dropped: %+v %v", rows, err)
	}
	record, err := s.get("a.agent.md", id)
	if err != nil || record.Pair == nil || record.Pair.TestsPath != tests || record.Pair.SnapshotRevision != pair.SnapshotRevision || string(record.Source) != string(source) || string(record.Pair.Source) != historyTests {
		t.Fatalf("verified source binding changed: %+v %v", record, err)
	}
	s.close()
	// Retrieval still uses retained bytes after the real target vanishes.
	if err := os.Remove(tests); err != nil {
		t.Fatal(err)
	}
	result, err := historyClient(t, f).ReadResource(t.Context(), &sdk.ReadResourceParams{URI: pairVersionURI("a.agent.md", id)})
	if err != nil {
		t.Fatal(err)
	}
	var d sourceDocument
	if json.Unmarshal([]byte(result.Contents[0].Text), &d) != nil || d.Kind != "kmx.agent-pair-version" || d.Tests.Source != historyTests {
		t.Fatal("historical alias became current read authority")
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
