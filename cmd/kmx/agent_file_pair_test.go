package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
)

const cliPairTests = "---\nschemaVersion: 1\n---\n\n# Keep tests separate\n\n```yaml agent-test\nid: greeting\ninput: Ada\nassertions: [{id: check, type: contains, value: Hello from pair}]\n```\n"

func cliPairFiles(t *testing.T, repo bool) (string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "g.agent.md")
	if repo {
		file = filepath.Join(root, ".github", "agents", "g.agent.md")
	}
	tests, _ := agentfile.DefaultTestsPath(file)
	for _, dir := range []string{filepath.Dir(file), filepath.Dir(tests)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(file, []byte("---\nname: greeter\nmodel: fake\ntools: []\n---\nReply with exactly `Hello from pair`.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tests, []byte(cliPairTests), 0600); err != nil {
		t.Fatal(err)
	}
	return file, tests
}

func TestFileCommandsReadSelectedTests(t *testing.T) {
	for _, repo := range []bool{false, true} {
		for _, override := range []bool{false, true} {
			for _, op := range []string{"run", "test"} {
				t.Run(op+"/"+map[bool]string{false: "sibling", true: "repo"}[repo]+"/"+map[bool]string{false: "default", true: "override"}[override], func(t *testing.T) {
					t.Setenv("KMX_HOME", privateCommandHome(t))
					file, tests := cliPairFiles(t, repo)
					args := []string{op, file, "-o", "json"}
					if override {
						selected := filepath.Join(filepath.Dir(file), "chosen tests.md")
						if err := os.WriteFile(selected, []byte(strings.Replace(cliPairTests, "id: greeting", "id: selected", 1)), 0600); err != nil {
							t.Fatal(err)
						}
						tests = selected
						args = append(args, "--tests", tests)
					}
					before, err := os.ReadFile(file)
					if err != nil {
						t.Fatal(err)
					}
					expected, err := agentfile.LoadPair(file, map[bool]string{false: "", true: tests}[override])
					if err != nil {
						t.Fatal(err)
					}
					var out, diag bytes.Buffer
					deps, loads := testDependencies(&out, &diag)
					deps.fileRunner = localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: func() (string, error) { return privateCommandHome(t), nil }})
					if err := execute(args, deps); err != nil {
						t.Fatalf("%v: %s %s", err, out.String(), diag.String())
					}
					var result struct {
						State            string
						FullPass         bool
						File             struct{ Revision string }
						SnapshotRevision string
						Tests            struct{ Path, Revision, SelectionKey string }
						Examples         []struct{ ID string }
					}
					if err := json.Unmarshal(out.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					wantID := "greeting"
					if override {
						wantID = "selected"
					}
					if result.State != "complete" || result.FullPass != (op == "test") || result.File.Revision != expected.Agent.Revision || result.SnapshotRevision != expected.SnapshotRevision || result.Tests.Revision != expected.Tests.Revision || result.Tests.SelectionKey != expected.Tests.Selection.Key || result.Tests.Path == "" || !strings.Contains(tests, result.Tests.Path) || len(result.Examples) != 1 || result.Examples[0].ID != wantID || *loads != 0 {
						t.Fatalf("wrong selected pair: %s", out.String())
					}
					after, err := os.ReadFile(file)
					if err != nil || !bytes.Equal(before, after) {
						t.Fatalf("agent source changed: %v", err)
					}
				})
			}
		}
	}
}

func TestFileTestsFlagRequiresAgentAndNonemptySelection(t *testing.T) {
	file, tests := cliPairFiles(t, false)
	for _, args := range [][]string{{"test", "--tests", tests}, {"run", "--tests", tests}, {"test", file, "--tests", ""}, {"test", file, "--tests", tests + ".missing"}} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		err := execute(args, deps)
		if err == nil || exitCode(err) != 2 || *loads != 0 {
			t.Fatalf("args=%q err=%v output=%s", args, err, out.String())
		}
		if len(args) == 3 && !strings.Contains(err.Error(), "requires an explicit agent FILE") {
			t.Fatalf("selector must refuse before enumeration: %v", err)
		}
	}
}
