//go:build linux || darwin

package mcp

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func sourcesURI(path, tests string) string {
	u := "kmx://agent-sources/" + url.PathEscape(path)
	if tests != "" {
		u += "?tests=" + url.QueryEscape(tests)
	}
	return u
}

func TestSourceHistoryResourceAndLegacyVersionWithCompanion(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, root, base)
	source := []byte("# Agent\r\nPrompt Māori\r\n")
	original, err := f.Create(t.Context(), "a.agent.md", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Edit(t.Context(), "a.agent.md", original.Revision, append(append([]byte{}, source...), '\n')); err != nil {
		t.Fatal(err)
	}
	old := f.historyView(t.Context(), "a.agent.md").Versions[0]
	if err = os.WriteFile(filepath.Join(root, "a.agent.test.md"), []byte(historyTests), 0600); err != nil {
		t.Fatal(err)
	}
	cs := historyClient(t, f)
	result, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: sourcesURI("a.agent.md", "")})
	if err != nil {
		t.Fatal("complete source resource unavailable", err)
	}
	var view struct {
		Kind             string
		ReadOnly         bool
		Source           string
		SnapshotRevision string
		Tests            struct{ State, Source, Revision, SelectionKey string }
		History          AgentHistory
		SourceHistory    struct {
			SchemaVersion                         int
			Read, Execute, Edit, Capture, Restore bool
		}
	}
	if err = json.Unmarshal([]byte(result.Contents[0].Text), &view); err != nil {
		t.Fatal(err)
	}
	if view.Kind != "kmx.agent-sources" || !view.ReadOnly || view.Tests.State != "external" || view.Tests.Source != historyTests || view.Source != string(source)+"\n" || view.SnapshotRevision == "" || !view.SourceHistory.Read || view.SourceHistory.Execute || view.SourceHistory.Edit || view.SourceHistory.Capture || view.SourceHistory.Restore || view.SourceHistory.SchemaVersion != 1 {
		t.Fatalf("unsafe or partial sources: %+v", view)
	}
	if len(view.History.Versions) != 1 || view.History.Versions[0].Label != "Agent only—tests not retained" {
		t.Fatalf("legacy history completeness: %+v", view.History)
	}
	if _, err = cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: old.URI}); err != nil {
		t.Fatal("adding tests hid old incomplete history", err)
	}
	// Read capability does not enable any legacy writer/execution route.
	if _, err = f.Edit(t.Context(), "a.agent.md", revision([]byte(view.Source)), source); err == nil {
		t.Fatal("read-only pair capability enabled legacy edit")
	}
}

func TestSourceHistoryPairVersionRetainedPathsAreData(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, root, base)
	source := []byte("Prompt\n")
	if _, err := f.Create(t.Context(), "a.agent.md", source); err != nil {
		t.Fatal(err)
	}
	s := historyStoreForTest(t, base, root, true)
	id, _ := seedPairHistory(t, s, filepath.Join(root, "a.agent.md"), "a.agent.md", source, []byte(historyTests), 1)
	s.close()
	// Historical source must be readable even though no current tests source exists.
	cs := historyClient(t, f)
	u := "kmx://agent-pair-version/" + url.PathEscape("a.agent.md") + "/" + id
	result, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: u})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Kind     string
		Source   string
		Tests    struct{ Source, State string }
		ReadOnly bool
	}
	if json.Unmarshal([]byte(result.Contents[0].Text), &doc) != nil || doc.Kind != "kmx.agent-pair-version" || doc.Source != string(source) || doc.Tests.Source != historyTests || doc.Tests.State != "external" || !doc.ReadOnly {
		t.Fatalf("partial retained pair: %+v", doc)
	}
	if _, err = cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: agentVersionURI("a.agent.md", id)}); err == nil {
		t.Fatal("legacy version route returned half a pair")
	}
	if _, err = os.Stat(filepath.Join(root, "a.agent.test.md")); !os.IsNotExist(err) {
		t.Fatal("historical read created tests source")
	}
}

func TestSourceHistoryResourceOverridesBoundsAndConfinement(t *testing.T) {
	for _, scenario := range []string{"arbitrary-name", "outside-root", "symlink-parent", "invalid-content", "oversize-json", "repo-root-gap"} {
		t.Run(scenario, func(t *testing.T) {
			base, root := historyPaths(t)
			f := historyFiles(t, root, base)
			if _, err := f.Create(t.Context(), "a.agent.md", []byte("Prompt\n")); err != nil {
				t.Fatal(err)
			}
			selected := "selected.md"
			content := historyTests
			expected := ""
			switch scenario {
			case "outside-root":
				selected = filepath.Join(t.TempDir(), "elsewhere.md")
				expected = "unsafe_path"
			case "symlink-parent":
				outside := t.TempDir()
				_ = os.Symlink(outside, filepath.Join(root, "alias"))
				selected = "alias/suite.md"
				expected = "unsafe_path"
			case "invalid-content":
				content = "PRIVATE_NON_TEST_FILE\n"
				expected = "invalid_file"
			case "oversize-json":
				_ = os.WriteFile(filepath.Join(root, "a.agent.md"), []byte(strings.Repeat("<", 230000)), 0600)
				content = "---\nschemaVersion: 1\n---\n" + strings.Repeat("<", 230000) + "\n"
				expected = "invalid_file"
			case "repo-root-gap":
				agents := filepath.Join(root, ".github", "agents")
				_ = os.MkdirAll(agents, 0700)
				_ = os.WriteFile(filepath.Join(agents, "a.agent.md"), []byte("Prompt\n"), 0600)
				f = historyFiles(t, agents, base)
				selected = ""
				expected = "unsafe_path"
			}
			if selected != "" {
				name := selected
				if !filepath.IsAbs(name) {
					name = filepath.Join(root, name)
				}
				if err := os.WriteFile(name, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cs := historyClient(t, f)
			result, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: sourcesURI("a.agent.md", selected)})
			if expected == "" {
				if err != nil || !strings.Contains(result.Contents[0].Text, `"state":"external"`) {
					t.Fatalf("approved arbitrary-name tests selector failed: %v", err)
				}
			} else {
				if err != nil {
					t.Fatalf("fixed refusal lost through RPC: %v", err)
				}
				body := result.Contents[0].Text
				if !strings.Contains(body, `"kind":"kmx.source-history-error"`) || !strings.Contains(body, `"reasonCode":"`+expected+`"`) || strings.Contains(body, "PRIVATE_NON_TEST_FILE") {
					t.Fatalf("wanted fixed %s, got %s", expected, body)
				}
			}
		})
	}
}
